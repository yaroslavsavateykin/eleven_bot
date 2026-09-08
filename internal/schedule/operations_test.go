package schedule

import (
	"context"
	"group411/internal/db"
	"path/filepath"
	"testing"
	"time"
)

func TestLifecycleImmediateApply(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, q := range []string{
		`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','',''),(2,'other',-2,'UTC','other','','')`,
		`INSERT INTO users(id,telegram_user_id,created_at,updated_at) VALUES(1,10,'',''),(2,20,'','')`,
	} {
		if _, err = d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s := Service{DB: d, GroupID: 1, TZ: time.UTC}
	start := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	e := Event{GroupID: 1, Kind: "event", Category: "event", Title: "Meeting", StartsAt: start, EndsAt: &end, Timezone: "UTC", Status: "active", Tags: []string{"a", "b"}}
	created, err := s.Apply(ctx, Proposal{Operation: "create", Event: e}, -1, 1)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := s.Apply(ctx, Proposal{Operation: "create", Event: e}, -1, 1)
	if err != nil || replayed.ID != created.ID {
		t.Fatal("message replay was not idempotent", replayed, err)
	}
	before := s.Get(ctx, created.ID)
	updated := before
	updated.Title = "Moved"
	updated.StartsAt = start.Add(2 * time.Hour)
	newEnd := updated.StartsAt.Add(30 * time.Minute)
	updated.EndsAt = &newEnd
	updated.Tags = []string{"c"}
	p := Proposal{Operation: "update", Event: updated, Before: Snapshot(before)}
	if _, err = s.Apply(ctx, p, -1, 2); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(ctx, p, -1, 3); err == nil {
		t.Fatal("stale accepted")
	}
	current := s.Get(ctx, created.ID)
	if len(current.Tags) != 1 || current.Tags[0] != "c" {
		t.Fatal("tags not replaced")
	}
	var key string
	d.QueryRow("SELECT dedupe_key FROM events WHERE id=?", current.ID).Scan(&key)
	if key != Canonical(current) {
		t.Fatal("stale dedupe key")
	}
	collision := e
	collision.Title = "Overlap"
	collision.StartsAt = current.StartsAt
	collision.EndsAt = current.EndsAt
	conflicting, err := s.Apply(ctx, Proposal{Operation: "create", Event: collision}, -1, 4)
	if err != nil || conflicting.ID == 0 || len(conflicting.Warnings) != 1 || conflicting.Warnings[0].Event.ID != current.ID || !conflicting.Warnings[0].StartsAt.Equal(current.StartsAt) {
		t.Fatalf("overlapping create: %#v %v", conflicting, err)
	}
	var active int
	if err = d.QueryRow("SELECT count(*) FROM events WHERE group_id=1 AND status='active'").Scan(&active); err != nil || active != 2 {
		t.Fatalf("overlapping create was not persisted: %d %v", active, err)
	}
	thirdStart := current.StartsAt.Add(40 * time.Minute)
	thirdEnd := thirdStart.Add(30 * time.Minute)
	third, err := s.Apply(ctx, Proposal{Operation: "create", Event: Event{GroupID: 1, Kind: "event", Category: "event", Title: "Second overlap", StartsAt: thirdStart, EndsAt: &thirdEnd, Timezone: "UTC", Status: "active"}}, -1, 5)
	if err != nil || third.ID == 0 {
		t.Fatalf("second overlapping event: %#v %v", third, err)
	}
	beforeCollision := s.Get(ctx, conflicting.ID)
	updatedCollision := beforeCollision
	updatedCollision.Title = "Overlap updated"
	updatedCollision.StartsAt = current.StartsAt.Add(15 * time.Minute)
	updatedEnd := updatedCollision.StartsAt.Add(35 * time.Minute)
	updatedCollision.EndsAt = &updatedEnd
	updated, err = s.Apply(ctx, Proposal{Operation: "update", Event: updatedCollision, Before: Snapshot(beforeCollision)}, -1, 6)
	if err != nil || len(updated.Warnings) != 2 || updated.Warnings[0].Event.ID != current.ID || !updated.Warnings[0].StartsAt.Equal(current.StartsAt) || updated.Warnings[1].Event.ID != third.ID || !updated.Warnings[1].StartsAt.Equal(thirdStart) {
		t.Fatalf("overlapping update: %#v %v", updated, err)
	}
	if got := s.Get(ctx, conflicting.ID); got.Title != "Overlap updated" || !got.StartsAt.Equal(updatedCollision.StartsAt) {
		t.Fatalf("overlapping update was not persisted: %#v", got)
	}
	if _, err = s.Apply(ctx, Proposal{Operation: "cancel", Event: current, Before: Snapshot(current)}, -1, 7); err != nil {
		t.Fatal(err)
	}
	if s.Get(ctx, current.ID).Status != "cancelled" {
		t.Fatal("not soft cancelled")
	}
	var n int
	d.QueryRow("SELECT count(*) FROM change_log").Scan(&n)
	if n != 6 {
		t.Fatalf("changelog %d", n)
	}
	d.QueryRow("SELECT count(*) FROM event_sources WHERE telegram_chat_id=-1").Scan(&n)
	if n != 6 {
		t.Fatalf("sources %d", n)
	}
	for _, table := range []string{"event_proposals", "pending_intents", "jobs"} {
		if err = d.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s records %d: %v", table, n, err)
		}
	}
	other := s
	other.GroupID = 2
	if other.Get(ctx, current.ID).ID != 0 {
		t.Fatal("cross-group lookup")
	}
}

func TestOverlapAndRecurrence(t *testing.T) {
	start := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	e := Event{ID: 1, GroupID: 1, Kind: "event", Category: "event", Title: "A", StartsAt: start, EndsAt: &end, Timezone: "UTC", Status: "active"}
	for _, tc := range []struct {
		name   string
		offset time.Duration
		group  int64
		want   int
	}{{"overlap", 30 * time.Minute, 1, 1}, {"boundary", time.Hour, 1, 0}, {"other group", 0, 2, 0}, {"previous boundary", -time.Hour, 1, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			b := e
			b.ID = 2
			b.GroupID = tc.group
			b.StartsAt = start.Add(tc.offset)
			be := b.StartsAt.Add(time.Hour)
			b.EndsAt = &be
			c, err := conflicts(e, []Event{b})
			if err != nil || len(c) != tc.want {
				t.Fatalf("conflicts %v %v", c, err)
			}
		})
	}
	r := "FREQ=WEEKLY;COUNT=4"
	e.RRule = &r
	if err := Validate(e); err != nil {
		t.Fatal(err)
	}
	b := e
	b.ID = 2
	b.RRule = nil
	b.StartsAt = start.AddDate(0, 0, 7).Add(30 * time.Minute)
	be := b.StartsAt.Add(time.Hour)
	b.EndsAt = &be
	c, err := conflicts(e, []Event{b})
	if err != nil || len(c) != 1 || !c[0].StartsAt.Equal(b.StartsAt) {
		t.Fatal("recurring conflict missing", err)
	}
	times, err := starts(e, start.AddDate(0, 0, 7), start.AddDate(0, 0, 8))
	if err != nil || len(times) != 1 || !times[0].Equal(start.AddDate(0, 0, 7)) {
		t.Fatal("wrong occurrence", times, err)
	}
	for _, rule := range []string{"FREQ=SECONDLY;COUNT=3", "FREQ=WEEKLY", "FREQ=YEARLY;COUNT=4"} {
		e.RRule = &rule
		if Validate(e) == nil {
			t.Fatal("unbounded/unsafe rule accepted", rule)
		}
	}
}

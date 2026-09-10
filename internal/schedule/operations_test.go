package schedule

import (
	"context"
	"github.com/teambition/rrule-go"
	"group411/internal/db"
	"path/filepath"
	"strings"
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

func TestAllDayNormalizationAndConflictExclusion(t *testing.T) {
	start := time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)
	end := start.Add(95 * time.Minute)
	lesson := Event{ID: 1, GroupID: 1, Kind: "lesson", Category: "lesson", Title: "Lesson", StartsAt: start, EndsAt: &end, Timezone: "UTC", Status: "active"}
	deadline := Event{ID: 2, GroupID: 1, Kind: "deadline", Category: "deadline", Title: "Documents", StartsAt: start, Timezone: "UTC", AllDay: true, Status: "active"}
	normalized, err := NormalizeAllDay(deadline, time.UTC)
	if err != nil || normalized.StartsAt.Hour() != 0 || normalized.EndsAt.Sub(normalized.StartsAt) != 24*time.Hour {
		t.Fatalf("normalized=%#v err=%v", normalized, err)
	}
	if got, err := conflicts(normalized, []Event{lesson}); err != nil || len(got) != 0 {
		t.Fatalf("all-day conflict=%#v err=%v", got, err)
	}
	if got, err := conflicts(lesson, []Event{normalized}); err != nil || len(got) != 0 {
		t.Fatalf("timed conflict with all-day=%#v err=%v", got, err)
	}
}

func TestApplyPersistsTimedConflictAndReturnsWarning(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "conflict-warning.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d, GroupID: 1, TZ: time.UTC}
	start := time.Now().UTC().AddDate(0, 0, 1).Truncate(time.Minute)
	firstEnd := start.Add(95 * time.Minute)
	if _, err = s.Apply(ctx, Proposal{Operation: "create", Event: Event{GroupID: 1, Kind: "lesson", Category: "lesson", Title: "Первая пара", StartsAt: start, EndsAt: &firstEnd, Timezone: "UTC"}}, -1, 1); err != nil {
		t.Fatal(err)
	}
	secondStart := start.Add(30 * time.Minute)
	secondEnd := secondStart.Add(95 * time.Minute)
	second, err := s.Apply(ctx, Proposal{Operation: "create", Event: Event{GroupID: 1, Kind: "lesson", Category: "lesson", Title: "Вторая пара", StartsAt: secondStart, EndsAt: &secondEnd, Timezone: "UTC"}}, -1, 2)
	if err != nil || second.ID == 0 || len(second.Warnings) != 1 || second.Warnings[0].Event.Title != "Первая пара" {
		t.Fatalf("event=%#v err=%v", second, err)
	}
}

func TestRenameMergesExactDuplicateIntoTarget(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "update-duplicate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d, GroupID: 1, TZ: time.UTC}
	start := time.Now().UTC().AddDate(0, 0, 1).Truncate(time.Minute)
	end := start.Add(time.Hour)
	first, err := s.Apply(ctx, Proposal{Operation: "create", Event: Event{GroupID: 1, Kind: "lesson", Category: "lesson", Title: "Практикум", StartsAt: start, EndsAt: &end, Timezone: "UTC"}}, -1, 1)
	if err != nil {
		t.Fatal(err)
	}
	secondStart := start.Add(2 * time.Hour)
	secondEnd := secondStart.Add(time.Hour)
	second, err := s.Apply(ctx, Proposal{Operation: "create", Event: Event{GroupID: 1, Kind: "lesson", Category: "lesson", Title: "Черновик", StartsAt: secondStart, EndsAt: &secondEnd, Timezone: "UTC"}}, -1, 2)
	if err != nil {
		t.Fatal(err)
	}
	updated := second
	updated.Title = first.Title
	updated.StartsAt = first.StartsAt
	updated.EndsAt = first.EndsAt
	renamed, err := s.Apply(ctx, Proposal{Operation: "update", Event: updated, Before: Snapshot(second)}, -1, 3)
	if err != nil || renamed.ID != second.ID || renamed.Title != "Практикум" || renamed.MergedDuplicateID != first.ID || len(renamed.Warnings) != 0 {
		t.Fatalf("renamed=%#v err=%v", renamed, err)
	}
	if status := s.Get(ctx, first.ID).Status; status != "cancelled" {
		t.Fatalf("duplicate status=%q", status)
	}
}

func TestPrivateProposalQueuesAnnouncement(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "announcement.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d, GroupID: 1, TZ: time.UTC}
	start := time.Now().UTC().AddDate(0, 0, 1).Truncate(time.Minute)
	end := start.Add(time.Hour)
	e := Event{GroupID: 1, Kind: "event", Category: "event", Title: "private", StartsAt: start, EndsAt: &end, Timezone: "UTC"}
	if _, err = s.Apply(ctx, Proposal{Operation: "create", Event: e, Announce: true}, 100, 1); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err = d.QueryRow(`SELECT count(*) FROM group_announcement_changes`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("pending=%d err=%v", pending, err)
	}
	e.Title = "group"
	if _, err = s.Apply(ctx, Proposal{Operation: "create", Event: e}, -1, 2); err != nil {
		t.Fatal(err)
	}
	if err = d.QueryRow(`SELECT count(*) FROM group_announcement_changes`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("group change queued: %d %v", pending, err)
	}
}

func TestApplyAllRollsBackWholeBatch(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "batch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d, GroupID: 1, TZ: time.UTC}
	start := time.Now().UTC().AddDate(0, 0, 1).Truncate(time.Minute)
	end := start.Add(time.Hour)
	valid := Proposal{Operation: "create", Event: Event{GroupID: 1, Kind: "event", Category: "event", Title: "valid", StartsAt: start, EndsAt: &end, Timezone: "UTC"}}
	invalid := valid
	invalid.Event.Title = ""
	if _, err = s.ApplyAll(ctx, []Proposal{valid, invalid}, -1, 99); err == nil {
		t.Fatal("invalid batch accepted")
	}
	var count int
	if err = d.QueryRow(`SELECT count(*) FROM events`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial batch persisted: %d %v", count, err)
	}
}

func TestApplyAllReplayReturnsPersistedEvents(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "replay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d, GroupID: 1, TZ: time.UTC}
	start := time.Now().UTC().AddDate(0, 0, 1).Truncate(time.Minute)
	end := start.Add(time.Hour)
	p := Proposal{Operation: "create", Event: Event{GroupID: 1, Kind: "event", Category: "event", Title: "persisted", StartsAt: start, EndsAt: &end, Timezone: "UTC"}}
	first, err := s.ApplyAll(ctx, []Proposal{p}, -1, 100)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.ApplyAll(ctx, []Proposal{p}, -1, 100)
	if err != nil || len(replay) != 1 || replay[0].ID != first[0].ID || replay[0].Title != "persisted" || !replay[0].StartsAt.Equal(start) {
		t.Fatalf("replay=%#v first=%#v err=%v", replay, first, err)
	}
}

func TestAcademicWeekParityAlternatesFromConfiguredReference(t *testing.T) {
	s := Service{TZ: time.UTC, WeekParity: WeekParityConfig{ReferenceWeekStart: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), ReferenceParity: "even"}}
	for _, tc := range []struct {
		date time.Time
		want string
	}{
		{time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC), "even"},
		{time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC), "odd"},
		{time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC), "even"},
	} {
		got, err := s.AcademicWeekParity(tc.date)
		if err != nil || got != tc.want {
			t.Fatalf("date=%s parity=%q err=%v", tc.date, got, err)
		}
	}
}

func TestApplyNormalizesAcademicWeekParitySeries(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "parity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d, GroupID: 1, TZ: time.UTC, WeekParity: WeekParityConfig{ReferenceWeekStart: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), ReferenceParity: "odd"}}
	rule := "FREQ=WEEKLY;INTERVAL=2;BYDAY=TU;COUNT=6"
	start := time.Date(2026, 9, 8, 10, 50, 0, 0, time.UTC) // Tuesday in the configured odd week.
	end := start.Add(95 * time.Minute)
	e := Event{GroupID: 1, Kind: "lesson", Category: "lesson", Title: "Квантовая химия", StartsAt: start, EndsAt: &end, Timezone: "UTC", RRule: &rule}
	created, err := s.Apply(ctx, Proposal{Operation: "create", Event: e, WeekParity: "even"}, 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 9, 15, 10, 50, 0, 0, time.UTC); !created.StartsAt.Equal(want) || !created.EndsAt.Equal(want.Add(95*time.Minute)) {
		t.Fatalf("normalized event=%#v want start=%s", created, want)
	}
	if parity, err := s.AcademicWeekParity(created.StartsAt); err != nil || parity != "even" {
		t.Fatalf("saved parity=%q err=%v", parity, err)
	}
	var payload string
	if err = d.QueryRow(`SELECT payload_json FROM change_log WHERE entity_id=?`, created.ID).Scan(&payload); err != nil || !strings.Contains(payload, `"week_parity":"even"`) || !strings.Contains(payload, "2026-09-15") {
		t.Fatalf("changelog=%q err=%v", payload, err)
	}
	noConfig := s
	noConfig.WeekParity = WeekParityConfig{}
	if _, err = noConfig.Apply(ctx, Proposal{Operation: "create", Event: e, WeekParity: "even"}, 10, 2); err == nil || !strings.Contains(err.Error(), "Не настроено") {
		t.Fatalf("missing parity configuration accepted: %v", err)
	}
	oneTime := e
	oneTime.Title = "Консультация"
	oneTime.RRule = nil
	oneTime.StartsAt = start
	oneTime.EndsAt = &end
	single, err := s.Apply(ctx, Proposal{Operation: "create", Event: oneTime, WeekParity: "even"}, 10, 3)
	if err != nil || !single.StartsAt.Equal(time.Date(2026, 9, 15, 10, 50, 0, 0, time.UTC)) || single.RRule != nil {
		t.Fatalf("one-time parity event=%#v err=%v", single, err)
	}
	before := s.Get(ctx, created.ID)
	updated := before
	updated.RRule = &rule
	updated.StartsAt = created.StartsAt
	updatedEnd := updated.StartsAt.Add(95 * time.Minute)
	updated.EndsAt = &updatedEnd
	changed, err := s.Apply(ctx, Proposal{Operation: "update", Event: updated, Before: Snapshot(before), WeekParity: "odd"}, 10, 4)
	if err != nil || changed.ID != created.ID || !changed.StartsAt.Equal(time.Date(2026, 9, 22, 10, 50, 0, 0, time.UTC)) {
		t.Fatalf("even-to-odd update=%#v err=%v", changed, err)
	}
	var count int
	if err = d.QueryRow(`SELECT count(*) FROM events WHERE title='Квантовая химия' AND status='active'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("parity update created a second series: count=%d err=%v", count, err)
	}
}

func TestApplyBoundsUnboundedParitySeriesToDefaultHorizon(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "horizon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d, GroupID: 1, TZ: time.UTC, WeekParity: WeekParityConfig{ReferenceWeekStart: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), ReferenceParity: "odd"}}
	rule := "FREQ=WEEKLY;INTERVAL=2;BYDAY=FR"
	start := time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC) // Friday in the current odd week.
	end := start.Add(95 * time.Minute)
	created, err := s.Apply(ctx, Proposal{Operation: "create", WeekParity: "even", Event: Event{GroupID: 1, Kind: "lesson", Category: "lesson", Title: "География", StartsAt: start, EndsAt: &end, Timezone: "UTC", RRule: &rule}}, 10, 1)
	if err != nil || created.RRule == nil || !strings.Contains(*created.RRule, "UNTIL=") || created.RecurrenceHorizon != "default" {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	if want := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC); !created.StartsAt.Equal(want) || !created.EndsAt.Equal(want.Add(95*time.Minute)) {
		t.Fatalf("start=%s end=%s", created.StartsAt, created.EndsAt)
	}
	opt, err := rrule.StrToROption(*created.RRule)
	if err != nil || opt.Until.IsZero() || opt.Until.Sub(created.StartsAt) > time.Duration(DefaultRecurrenceHorizonWeeks*7)*24*time.Hour+time.Second {
		t.Fatalf("rule=%q opt=%#v err=%v", *created.RRule, opt, err)
	}
	r, _ := rrule.NewRRule(*opt)
	r.DTStart(created.StartsAt)
	for occurrence := r.After(created.StartsAt, true); !occurrence.IsZero(); occurrence = r.After(occurrence, false) {
		parity, parityErr := s.AcademicWeekParity(occurrence)
		if parityErr != nil || parity != "even" {
			t.Fatalf("occurrence=%s parity=%q err=%v", occurrence, parity, parityErr)
		}
	}
}

func TestApplyUsesSemesterEndForUnboundedSeries(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "semester.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	semesterStart := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	semesterEnd := time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC)
	s := Service{DB: d, GroupID: 1, TZ: time.UTC, Semester: Semester{Start: semesterStart, End: semesterEnd}}
	rule := "FREQ=WEEKLY;BYDAY=TU"
	start := time.Date(2026, 9, 15, 10, 50, 0, 0, time.UTC)
	end := start.Add(95 * time.Minute)
	created, err := s.Apply(ctx, Proposal{Operation: "create", Event: Event{GroupID: 1, Kind: "lesson", Category: "lesson", Title: "Физхимия", StartsAt: start, EndsAt: &end, Timezone: "UTC", RRule: &rule}}, 10, 1)
	if err != nil || created.RecurrenceHorizon != "semester" || created.RRule == nil || !strings.Contains(*created.RRule, "UNTIL=20261220T235959Z") {
		t.Fatalf("created=%#v err=%v", created, err)
	}
}

func TestApplyImportSkipsInvalidOperations(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "import.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d, GroupID: 1, TZ: time.UTC}
	start := time.Now().UTC().AddDate(0, 0, 2).Truncate(time.Minute)
	end := start.Add(time.Hour)
	valid := Proposal{Operation: "create", SourceIndex: 1, SourceText: "Точная встреча", Event: Event{GroupID: 1, Kind: "event", Category: "event", Title: "Точная встреча", StartsAt: start, EndsAt: &end, Timezone: "UTC"}}
	invalid := valid
	invalid.SourceIndex, invalid.SourceText, invalid.Event.Title = 2, "Практикум без даты", ""
	result, err := s.ApplyImport(ctx, []Proposal{valid, invalid}, 10, 1)
	if err != nil || len(result.Events) != 1 || len(result.Skipped) != 1 || result.Skipped[0].Proposal.SourceIndex != 2 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	var count int
	if err = d.QueryRow(`SELECT count(*) FROM events WHERE status='active'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestApplyUsesDefaultHorizonOutsideConfiguredSemester(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "outside-semester.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d, GroupID: 1, TZ: time.UTC, Semester: Semester{Start: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC)}}
	rule := "FREQ=WEEKLY;BYDAY=FR"
	start := time.Date(2027, 1, 8, 9, 0, 0, 0, time.UTC)
	end := start.Add(95 * time.Minute)
	created, err := s.Apply(ctx, Proposal{Operation: "create", Event: Event{GroupID: 1, Kind: "lesson", Category: "lesson", Title: "География", StartsAt: start, EndsAt: &end, Timezone: "UTC", RRule: &rule}}, 10, 1)
	if err != nil || created.RecurrenceHorizon != "default" || strings.Contains(*created.RRule, "20261225") {
		t.Fatalf("created=%#v err=%v", created, err)
	}
}

func TestApplyImportDeduplicatesGeneratedRecurrenceHorizon(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "semantic-dedupe.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d, GroupID: 1, TZ: time.UTC, WeekParity: WeekParityConfig{ReferenceWeekStart: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), ReferenceParity: "even"}}
	rule := "FREQ=WEEKLY;INTERVAL=2;BYDAY=FR"
	start := time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC)
	end := start.Add(95 * time.Minute)
	p := Proposal{Operation: "create", WeekParity: "even", Event: Event{GroupID: 1, Kind: "lesson", Category: "lesson", Title: "География", StartsAt: start, EndsAt: &end, Timezone: "UTC", RRule: &rule}}
	first, err := s.ApplyImport(ctx, []Proposal{p}, 10, 1)
	if err != nil || len(first.Events) != 1 || first.Events[0].RecurrenceHorizon != "default" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := s.ApplyImport(ctx, []Proposal{p}, 10, 2)
	if err != nil || len(second.Events) != 0 || len(second.Skipped) != 1 {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	var count int
	if err = d.QueryRow(`SELECT count(*) FROM events WHERE status='active'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestApplyImportHidesDuplicateDedupeKeyError(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "dedupe-key.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d, GroupID: 1, TZ: time.UTC}
	start := time.Now().UTC().AddDate(0, 0, 1).Truncate(time.Minute)
	end := start.Add(time.Hour)
	p := Proposal{Operation: "create", SourceText: "повтор", Event: Event{GroupID: 1, Kind: "lesson", Category: "lesson", Title: "ВМС", StartsAt: start, EndsAt: &end, Timezone: "UTC"}}
	if _, err = s.ApplyImport(ctx, []Proposal{p}, 10, 1); err != nil {
		t.Fatal(err)
	}
	result, err := s.ApplyImport(ctx, []Proposal{p}, 10, 2)
	if err != nil || len(result.Events) != 0 || len(result.Skipped) != 1 || result.Skipped[0].Reason != "без изменений" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

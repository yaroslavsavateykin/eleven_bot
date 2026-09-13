package schedule

import (
	"context"
	"group411/internal/db"
	"path/filepath"
	"testing"
	"time"
)

func TestInvocationReplaySurvivesRestartAndChangedSnapshot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "replay.db")
	d, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d, GroupID: 1, TZ: time.UTC}
	start := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	p := Proposal{Operation: "create", Event: Event{GroupID: 1, Kind: "event", Category: "event", Title: "test", StartsAt: start, EndsAt: &end, Timezone: "UTC"}, Announce: true}
	first, err := s.Apply(WithInvocation(ctx, "telegram:1:1/create"), p, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	d, err = db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s.DB = d
	replay, err := s.Apply(WithInvocation(ctx, "telegram:1:1/create"), p, 0, 0)
	if err != nil || replay.ID != first.ID {
		t.Fatalf("%+v %v", replay, err)
	}
	current := s.Get(ctx, first.ID)
	changed := current
	changed.Title = "changed"
	update := Proposal{Operation: "update", Event: changed, Before: Snapshot(current), Announce: true}
	if _, err = s.Apply(WithInvocation(ctx, "telegram:1:2/update"), update, 0, 0); err != nil {
		t.Fatal(err)
	}
	update.Before = Snapshot(s.Get(ctx, first.ID))
	if _, err = s.Apply(WithInvocation(ctx, "telegram:1:2/update"), update, 0, 0); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = d.QueryRow("SELECT count(*) FROM change_log").Scan(&count); err != nil || count != 2 {
		t.Fatalf("duplicate changes %d %v", count, err)
	}
	if _, err = s.Apply(WithInvocation(ctx, "telegram:1:3/update"), update, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err = d.QueryRow("SELECT count(*) FROM change_log").Scan(&count); err != nil || count != 3 {
		t.Fatalf("new message suppressed %d %v", count, err)
	}
}

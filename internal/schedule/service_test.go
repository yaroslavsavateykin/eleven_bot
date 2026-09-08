package schedule

import (
	"context"
	"group411/internal/db"
	"path/filepath"
	"testing"
	"time"
)

func TestOneOffAndDuplicate(t *testing.T) {
	ctx := context.Background()
	d, e := db.Open(ctx, filepath.Join(t.TempDir(), "app.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, e = d.Exec("INSERT INTO groups(name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES('411',-1,'Europe/Moscow','411',?,?)", now, now); e != nil {
		t.Fatal(e)
	}
	s := Service{DB: d, GroupID: 1, TZ: time.FixedZone("MSK", 3*3600)}
	start := time.Date(2026, 9, 9, 9, 40, 0, 0, time.UTC)
	e1, dup, e := s.Create(ctx, Event{Kind: "lesson", Title: "Квантовая химия", StartsAt: start}, "test", "x", "text")
	if e != nil || dup || e1.ID == 0 {
		t.Fatalf("create: %#v %v %v", e1, dup, e)
	}
	if e1.Category != "lesson" {
		t.Fatal("missing default category")
	}
	replayed, replayDup, replayErr := s.Create(ctx, Event{Kind: "event", Title: "Different payload", StartsAt: start.Add(time.Hour)}, "test", "x", "")
	if replayErr != nil || !replayDup || replayed.ID != e1.ID {
		t.Fatal("source replay changed identity", replayErr)
	}
	invalid := Event{Title: "Invalid", StartsAt: start, Category: "invalid"}
	if _, _, err := s.Create(ctx, invalid, "test", "", ""); err == nil {
		t.Fatal("invalid category accepted")
	}
	explicit := Event{Kind: "lesson", Title: "Assessment", StartsAt: start, Category: "exam"}
	created, _, err := s.Create(ctx, explicit, "test", "", "")
	if err != nil || created.Category != "exam" || created.Kind != "lesson" {
		t.Fatalf("category persistence: %v", err)
	}
	explicit.Category = "test"
	duplicate, isDup, err := s.Create(ctx, explicit, "test", "", "")
	if err != nil || !isDup || duplicate.ID != created.ID || duplicate.Category != "exam" {
		t.Fatal("category changed dedupe identity")
	}
	listed, err := s.List(ctx, start.Add(-time.Hour), start.Add(time.Hour), nil)
	if err != nil || len(listed) != 2 {
		t.Fatalf("list: %v", err)
	}
	_, dup, e = s.Create(ctx, Event{Kind: "lesson", Title: "квантовая  химия!", StartsAt: start}, "test", "y", "text")
	if e != nil || !dup {
		t.Fatalf("dedupe=%v err=%v", dup, e)
	}
}
func TestRecurring(t *testing.T) {
	e := Event{StartsAt: time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)}
	r := "FREQ=WEEKLY;INTERVAL=2"
	e.RRule = &r
	if !occurrences(e, time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC), time.UTC) {
		t.Fatal("biweekly occurrence missing")
	}
}

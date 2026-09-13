package schedule

import (
	"context"
	"group411/internal/db"
	"path/filepath"
	"testing"
	"time"
)

func TestExcludeOccurrencePreservesSeries(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "exclusions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d, GroupID: 1, TZ: time.UTC}
	start := time.Date(2026, 9, 15, 9, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	rule := "FREQ=WEEKLY;COUNT=5"
	e, _, err := s.Create(ctx, Event{Kind: "lesson", Title: "Практикум", StartsAt: start, EndsAt: &end, Timezone: "UTC", RRule: &rule}, "test", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = s.ExcludeOccurrence(ctx, e.ID, "2026-09-29", true); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.List(ctx, start, start.AddDate(0, 2, 0), nil)
	if err != nil || len(list) != 4 {
		t.Fatalf("count=%d %v", len(list), err)
	}
	for _, e := range list {
		if e.StartsAt.Day() == 29 {
			t.Fatal("excluded occurrence visible")
		}
	}
	if s.Get(ctx, e.ID).Status != "active" {
		t.Fatal("series cancelled")
	}
	local := s.Get(ctx, e.ID)
	replacement := local
	replacement.ID = 0
	replacement.RRule = nil
	replacement.ExcludedDates = nil
	replacement.StartsAt = time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	replacementEnd := replacement.StartsAt.Add(time.Hour)
	replacement.EndsAt = &replacementEnd
	warnings, err := s.OverlapWarnings(ctx, replacement)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("excluded occurrence still conflicts: %v %v", warnings, err)
	}
	if _, err = s.ExcludeOccurrence(ctx, e.ID, "2026-09-30", true); err == nil {
		t.Fatal("non-occurrence accepted")
	}
	var count int
	d.QueryRow("SELECT count(*) FROM change_log WHERE kind='event_exclude_occurrence'").Scan(&count)
	if count != 1 {
		t.Fatal("duplicate changelog", count)
	}
	current := s.Get(ctx, e.ID)
	updated := current
	updated.Title = "Практикум renamed"
	if _, err = s.Apply(ctx, Proposal{Operation: "update", Event: updated, Before: Snapshot(current)}, 1, 2); err != nil {
		t.Fatal("snapshot after exclusion", err)
	}
}

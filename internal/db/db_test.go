package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestCategoryMigration(t *testing.T) {
	ctx := context.Background()
	for _, legacy := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "test.db")
		if legacy {
			d, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			for _, version := range []string{"001_init.sql", "002_parse_errors.sql"} {
				b, err := migrations.ReadFile("migrations/" + version)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = d.Exec(string(b)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = d.Exec("CREATE TABLE schema_migrations(version TEXT PRIMARY KEY, applied_at TEXT NOT NULL); INSERT INTO schema_migrations VALUES('001_init.sql','now'),('002_parse_errors.sql','now'); INSERT INTO groups VALUES(1,'test',1,'UTC','test','now','now')"); err != nil {
				t.Fatal(err)
			}
			for i, title := range []string{"КР: химия", "КОНТРОЛЬНАЯ", "Проверочная", "Экзамен", "Дедлайн", "Кристаллы", "Обычная пара"} {
				if _, err = d.Exec("INSERT INTO events(id,group_id,kind,title,starts_at,timezone,source_type,dedupe_key,created_at,updated_at) VALUES(?,1,'lesson',?,'2026-09-09T08:00:00Z','UTC','test',?,'before','before')", i+1, title, title); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = d.Exec(`INSERT INTO users(id,telegram_user_id,created_at,updated_at) VALUES(1,123,'',''); INSERT INTO pending_intents VALUES(1,1,'add','{}','2099-01-01T00:00:00Z','before')`); err != nil {
				t.Fatal(err)
			}
			d.Close()
		}
		for reopen := 0; reopen < 2; reopen++ {
			d, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			if legacy {
				var intent string
				if err = d.QueryRow("SELECT intent_type FROM pending_intents WHERE user_id=1").Scan(&intent); err != nil || intent != "event" {
					t.Fatal("pending migration", intent, err)
				}
				for i, want := range []string{"test", "test", "quiz", "exam", "deadline", "lesson", "lesson"} {
					var got, key, title, updated string
					if err := d.QueryRow("SELECT category,dedupe_key,title,updated_at FROM events WHERE id=?", i+1).Scan(&got, &key, &title, &updated); err != nil {
						t.Fatal(err)
					}
					if got != want || key != title || updated != "before" {
						t.Fatalf("migration row %d: %s", i, got)
					}
				}
				if _, err := d.Exec("UPDATE events SET category='invalid'"); err == nil {
					t.Fatal("missing category constraint")
				}
			}
			d.Close()
		}
	}
}

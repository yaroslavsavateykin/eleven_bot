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

func TestMessageGraphMigrationPreservesLegacyReply(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	d, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"001_init.sql", "002_parse_errors.sql", "003_event_category.sql", "004_event_operations.sql", "005_multi_event_sources.sql"} {
		b, er := migrations.ReadFile("migrations/" + version)
		if er != nil {
			t.Fatal(er)
		}
		if _, er = d.Exec(string(b)); er != nil {
			t.Fatal(er)
		}
	}
	if _, err = d.Exec("CREATE TABLE schema_migrations(version TEXT PRIMARY KEY, applied_at TEXT NOT NULL); INSERT INTO schema_migrations SELECT '001_init.sql','now' UNION ALL SELECT '002_parse_errors.sql','now' UNION ALL SELECT '003_event_category.sql','now' UNION ALL SELECT '004_event_operations.sql','now' UNION ALL SELECT '005_multi_event_sources.sql','now'; INSERT INTO groups VALUES(1,'test',-1,'UTC','test','now','now'); INSERT INTO users(id,telegram_user_id,created_at,updated_at) VALUES(1,1,'now','now'); INSERT INTO messages(id,group_id,telegram_chat_id,telegram_message_id,user_id,sent_at,kind,text,reply_to_message_id,created_at) VALUES(1,1,-1,10,1,'now','text','user',NULL,'now'),(2,1,-1,11,1,'now','text','reply',10,'now')"); err != nil {
		t.Fatal(err)
	}
	d.Close()
	d, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var sender string
	var raw, internal sql.NullInt64
	if err = d.QueryRow("SELECT sender_type,reply_to_telegram_message_id,reply_to_message_id FROM messages WHERE id=2").Scan(&sender, &raw, &internal); err != nil {
		t.Fatal(err)
	}
	if sender != "user" || !raw.Valid || raw.Int64 != 10 || !internal.Valid || internal.Int64 != 1 {
		t.Fatalf("wrong migrated graph: %q %#v %#v", sender, raw, internal)
	}
}

package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"group411/internal/category"
	"io/fs"
	_ "modernc.org/sqlite"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrations embed.FS

func Open(ctx context.Context, path string) (*sql.DB, error) {
	d, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA foreign_keys = ON", "PRAGMA journal_mode = WAL", "PRAGMA busy_timeout = 5000"} {
		if _, err = d.ExecContext(ctx, q); err != nil {
			d.Close()
			return nil, fmt.Errorf("sqlite pragma: %w", err)
		}
	}
	if err = apply(ctx, d); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}
func apply(ctx context.Context, d *sql.DB) error {
	if _, err := d.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)"); err != nil {
		return err
	}
	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, f := range files {
		v := strings.TrimPrefix(f, "migrations/")
		var n int
		if err := d.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version=?", v).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		b, err := migrations.ReadFile(f)
		if err != nil {
			return err
		}
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, string(b)); err == nil && v == "003_event_category.sql" {
			err = backfillCategories(ctx, tx)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version,applied_at) VALUES(?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))", v)
		}
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", v, err)
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func backfillCategories(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT id,kind,title FROM events")
	if err != nil {
		return err
	}
	type entry struct {
		id       int64
		category string
	}
	var entries []entry
	for rows.Next() {
		var id int64
		var kind, title string
		if err := rows.Scan(&id, &kind, &title); err != nil {
			rows.Close()
			return err
		}
		value, _ := category.Resolve("", kind, title)
		entries = append(entries, entry{id, value})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range entries {
		if _, err := tx.ExecContext(ctx, "UPDATE events SET category=? WHERE id=?", e.category, e.id); err != nil {
			return err
		}
	}
	return nil
}

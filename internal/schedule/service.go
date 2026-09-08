package schedule

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"github.com/teambition/rrule-go"
	"group411/internal/category"
	"sort"
	"strings"
	"time"
	"unicode"
)

type Event struct {
	ID          int64      `json:"id"`
	GroupID     int64      `json:"group_id"`
	Kind        string     `json:"kind"`
	Category    string     `json:"category"`
	Title       string     `json:"title"`
	Description *string    `json:"description,omitempty"`
	Location    *string    `json:"location,omitempty"`
	StartsAt    time.Time  `json:"starts_at"`
	EndsAt      *time.Time `json:"ends_at,omitempty"`
	Timezone    string     `json:"timezone"`
	AllDay      bool       `json:"all_day"`
	RRule       *string    `json:"rrule,omitempty"`
	Status      string     `json:"status"`
	Tags        []string   `json:"tags,omitempty"`
	Warnings    []Conflict `json:"warnings,omitempty"`
}
type Service struct {
	DB      *sql.DB
	GroupID int64
	TZ      *time.Location
}

func Canonical(e Event) string {
	// Category is metadata, not identity; preserve existing persisted dedupe keys.
	s := fmt.Sprintf("%d|%s|%s|%s|%s|%s", e.GroupID, e.Kind, norm(e.Title), e.StartsAt.In(time.FixedZone("", 0)).Format("2006-01-02 15:04"), value(e.RRule), norm(value(e.Location)))
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
func norm(s string) string {
	s = strings.ToLower(strings.ReplaceAll(s, "ё", "е"))
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}
func value(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
func (s Service) Create(ctx context.Context, e Event, source, external, raw string) (Event, bool, error) {
	if e.GroupID == 0 {
		e.GroupID = s.GroupID
	}
	if e.GroupID != s.GroupID {
		return e, false, fmt.Errorf("wrong group")
	}
	if e.Kind == "" {
		e.Kind = "event"
	}
	if e.Status == "" {
		e.Status = "active"
	}
	resolved, err := category.Resolve(e.Category, e.Kind, e.Title)
	if err != nil {
		return e, false, err
	}
	e.Category = resolved
	if e.Title == "" || e.StartsAt.IsZero() {
		return e, false, fmt.Errorf("title and starts_at are required")
	}
	if e.RRule != nil {
		if _, err := rrule.StrToRRule(*e.RRule); err != nil {
			return e, false, fmt.Errorf("invalid rrule: %w", err)
		}
	}
	e.StartsAt = e.StartsAt.UTC()
	if e.EndsAt != nil {
		v := e.EndsAt.UTC()
		if !v.After(e.StartsAt) {
			return e, false, fmt.Errorf("ends_at must be after starts_at")
		}
		e.EndsAt = &v
	}
	if e.Timezone == "" {
		e.Timezone = s.TZ.String()
	}
	key := Canonical(e)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return e, false, err
	}
	defer tx.Rollback()
	var id int64
	if external != "" {
		var groupID int64
		err = tx.QueryRowContext(ctx, "SELECT e.id,e.group_id FROM event_sources es JOIN events e ON e.id=es.event_id WHERE es.source_type=? AND es.external_id=?", source, external).Scan(&id, &groupID)
		if err == nil {
			if groupID != s.GroupID {
				return e, false, fmt.Errorf("source belongs to another group")
			}
			if err = tx.Commit(); err != nil {
				return e, false, err
			}
			return s.Get(ctx, id), true, nil
		}
		if err != sql.ErrNoRows {
			return e, false, err
		}
	}
	err = tx.QueryRowContext(ctx, "SELECT id FROM events WHERE group_id=? AND dedupe_key=? AND deleted_at IS NULL", e.GroupID, key).Scan(&id)
	dup := err == nil
	if err != nil && err != sql.ErrNoRows {
		return e, false, err
	}
	if !dup {
		r, er := tx.ExecContext(ctx, "INSERT INTO events(group_id,kind,title,description,location,starts_at,ends_at,timezone,all_day,rrule,status,source_type,dedupe_key,created_at,updated_at,category) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", e.GroupID, e.Kind, e.Title, e.Description, e.Location, e.StartsAt.Format(time.RFC3339Nano), timePtr(e.EndsAt), e.Timezone, boolInt(e.AllDay), e.RRule, e.Status, source, key, now, now, e.Category)
		if er != nil {
			return e, false, er
		}
		id, _ = r.LastInsertId()
	}
	if external != "" {
		_, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO event_sources(event_id,source_type,external_id,raw_text,created_at) VALUES(?,?,?,?,?)", id, source, external, raw, now)
	} else {
		_, err = tx.ExecContext(ctx, "INSERT INTO event_sources(event_id,source_type,raw_text,created_at) VALUES(?,?,?,?)", id, source, raw, now)
	}
	if err != nil {
		return e, false, err
	}
	if !dup {
		for _, tag := range e.Tags {
			tag = norm(tag)
			if tag == "" {
				continue
			}
			if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO tags(name) VALUES(?)", tag); err != nil {
				return e, false, err
			}
			if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO event_tags(event_id,tag_id) SELECT ?,id FROM tags WHERE name=?", id, tag); err != nil {
				return e, false, err
			}
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO change_log(group_id,kind,entity_type,entity_id,payload_json,created_at) VALUES(?,?,?,?,?,?)", e.GroupID, "event_created", "event", fmt.Sprint(id), "{}", now)
		if err != nil {
			return e, false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return e, false, err
	}
	created := s.Get(ctx, id)
	created.Warnings, err = s.OverlapWarnings(ctx, created)
	if err != nil {
		return e, false, err
	}
	return created, dup, nil
}
func timePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format(time.RFC3339Nano)
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func (s Service) Get(ctx context.Context, id int64) Event {
	e, _ := s.event(ctx, "WHERE id=?", id)
	if e.GroupID != s.GroupID {
		return Event{}
	}
	return e
}
func (s Service) event(ctx context.Context, where string, arg any) (Event, error) {
	var e Event
	var st, en sql.NullString
	var ad int
	err := s.DB.QueryRowContext(ctx, "SELECT id,group_id,kind,title,description,location,starts_at,ends_at,timezone,all_day,rrule,status,category FROM events "+where, arg).Scan(&e.ID, &e.GroupID, &e.Kind, &e.Title, &e.Description, &e.Location, &st, &en, &e.Timezone, &ad, &e.RRule, &e.Status, &e.Category)
	if err != nil {
		return e, err
	}
	e.StartsAt, _ = time.Parse(time.RFC3339Nano, st.String)
	if en.Valid {
		v, _ := time.Parse(time.RFC3339Nano, en.String)
		e.EndsAt = &v
	}
	e.AllDay = ad == 1
	rows, err := s.DB.QueryContext(ctx, "SELECT t.name FROM tags t JOIN event_tags et ON et.tag_id=t.id WHERE et.event_id=? ORDER BY t.name", e.ID)
	if err != nil {
		return e, err
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		rows.Scan(&t)
		e.Tags = append(e.Tags, t)
	}
	return e, nil
}
func (s Service) List(ctx context.Context, from, to time.Time, tags []string) ([]Event, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id FROM events WHERE group_id=? AND status='active' AND deleted_at IS NULL AND starts_at<? ORDER BY starts_at", s.GroupID, to.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var out []Event
	for _, id := range ids {
		e, err := s.event(ctx, "WHERE id=?", id)
		if err != nil {
			return nil, err
		}
		times, err := starts(e, from, to)
		if err != nil {
			return nil, err
		}
		for _, t := range times {
			occurrence := e
			occurrence.StartsAt = t
			if e.EndsAt != nil {
				end := t.Add(e.EndsAt.Sub(e.StartsAt))
				occurrence.EndsAt = &end
			}
			out = append(out, occurrence)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartsAt.Before(out[j].StartsAt) })
	return out, nil
}
func occurrences(e Event, from, to time.Time, loc *time.Location) bool {
	if e.RRule == nil {
		return !e.StartsAt.Before(from) && e.StartsAt.Before(to)
	}
	r, err := rrule.StrToRRule(*e.RRule)
	if err != nil {
		return false
	}
	r.DTStart(e.StartsAt.In(loc))
	return len(r.Between(from.In(loc), to.In(loc), true)) > 0
}

type Status struct {
	Current *Event  `json:"current,omitempty"`
	Next    *Event  `json:"next,omitempty"`
	Today   []Event `json:"today"`
}

func (s Service) CurrentStatus(ctx context.Context, now time.Time) (Status, error) {
	local := now.In(s.TZ)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, s.TZ)
	events, err := s.List(ctx, start, start.AddDate(0, 0, 1), nil)
	if err != nil {
		return Status{}, err
	}
	z := Status{Today: events}
	for i := range events {
		e := events[i]
		if e.Kind != "lesson" {
			continue
		}
		end := e.StartsAt.Add(95 * time.Minute)
		if e.EndsAt != nil {
			end = *e.EndsAt
		}
		if !now.Before(e.StartsAt) && now.Before(end) {
			z.Current = &e
		}
		if now.Before(e.StartsAt) && (z.Next == nil || e.StartsAt.Before(z.Next.StartsAt)) {
			z.Next = &e
		}
	}
	return z, nil
}

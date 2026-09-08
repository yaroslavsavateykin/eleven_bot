package schedule

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/teambition/rrule-go"
	"group411/internal/category"
)

// Proposal always contains a full replacement; Before guards against a changed target.
type Proposal struct {
	Operation string `json:"operation"`
	Event     Event  `json:"event"`
	Before    string `json:"before"`
	Inferred  bool   `json:"inferred"`
}

func Snapshot(e Event) string { b, _ := json.Marshal(e); return string(b) }

func Validate(e Event) error {
	if strings.TrimSpace(e.Title) == "" || len(e.Title) > 500 || e.StartsAt.IsZero() {
		return fmt.Errorf("invalid title or start")
	}
	switch e.Kind {
	case "lesson", "deadline", "event", "note", "other":
	default:
		return fmt.Errorf("invalid kind")
	}
	if _, err := category.Resolve(e.Category, e.Kind, e.Title); err != nil {
		return err
	}
	if _, err := time.LoadLocation(e.Timezone); err != nil {
		return fmt.Errorf("invalid timezone")
	}
	if e.EndsAt == nil || !e.EndsAt.After(e.StartsAt) || e.EndsAt.Sub(e.StartsAt) > 24*time.Hour {
		return fmt.Errorf("duration must be 1 minute to 24 hours")
	}
	if e.EndsAt.Sub(e.StartsAt) < time.Minute {
		return fmt.Errorf("duration too short")
	}
	if len(e.Tags) > 12 || len(value(e.Description)) > 2000 || len(value(e.Location)) > 300 {
		return fmt.Errorf("event metadata too large")
	}
	for _, t := range e.Tags {
		if len(t) > 80 {
			return fmt.Errorf("tag too long")
		}
	}
	if e.RRule != nil {
		if len(*e.RRule) > 300 || strings.ContainsAny(*e.RRule, "\r\n") {
			return fmt.Errorf("invalid recurrence")
		}
		opt, err := rrule.StrToROption(*e.RRule)
		if err != nil {
			return fmt.Errorf("invalid recurrence")
		}
		if opt.Freq != rrule.DAILY && opt.Freq != rrule.WEEKLY && opt.Freq != rrule.MONTHLY && opt.Freq != rrule.YEARLY {
			return fmt.Errorf("recurrence must be daily or slower")
		}
		if len(opt.Byhour) > 1 || len(opt.Byminute) > 1 || len(opt.Bysecond) > 1 || opt.Count > 366 || opt.Interval > 366 {
			return fmt.Errorf("recurrence too complex")
		}
		// ponytail: finite one-year series keep conflict checks exhaustive; extend the ceiling with a bounded recurrence scheduler.
		ceiling := e.StartsAt.AddDate(1, 0, 0)
		if opt.Count <= 0 && opt.Until.IsZero() {
			return fmt.Errorf("recurrence requires COUNT or UNTIL within one year")
		}
		r, err := rrule.NewRRule(*opt)
		if err != nil {
			return err
		}
		loc, _ := time.LoadLocation(e.Timezone)
		r.DTStart(e.StartsAt.In(loc))
		if r.After(e.StartsAt, true).IsZero() {
			return fmt.Errorf("recurrence has no occurrences")
		}
		next := r.After(ceiling, false)
		if !next.IsZero() {
			return fmt.Errorf("recurrence exceeds one year")
		}
	}
	return nil
}

func (s Service) Candidates(ctx context.Context) ([]Event, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id FROM events WHERE group_id=? AND status='active' AND deleted_at IS NULL ORDER BY starts_at,id", s.GroupID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(ids))
	for _, id := range ids {
		e, err := s.event(ctx, "WHERE id=?", id)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func eventEnd(e Event) time.Time {
	if e.EndsAt != nil {
		return *e.EndsAt
	}
	return e.StartsAt.Add(95 * time.Minute)
}

func starts(e Event, from, to time.Time) ([]time.Time, error) {
	if e.RRule == nil {
		if !e.StartsAt.Before(from) && e.StartsAt.Before(to) {
			return []time.Time{e.StartsAt}, nil
		}
		return nil, nil
	}
	opt, err := rrule.StrToROption(*e.RRule)
	if err != nil {
		return nil, err
	}
	if opt.Freq != rrule.DAILY && opt.Freq != rrule.WEEKLY && opt.Freq != rrule.MONTHLY && opt.Freq != rrule.YEARLY {
		return nil, fmt.Errorf("unsupported existing recurrence")
	}
	loc, err := time.LoadLocation(e.Timezone)
	if err != nil {
		return nil, err
	}
	r, err := rrule.NewRRule(*opt)
	if err != nil {
		return nil, err
	}
	r.DTStart(e.StartsAt.In(loc))
	var out []time.Time
	for t := r.After(from, true); !t.IsZero() && t.Before(to); t = r.After(t, false) {
		out = append(out, t)
		if len(out) > 2000 {
			return nil, fmt.Errorf("recurrence too dense")
		}
	}
	return out, nil
}

type Conflict struct {
	Event    Event     `json:"event"`
	StartsAt time.Time `json:"starts_at"`
}

func conflicts(e Event, existing []Event) ([]Conflict, error) {
	duration := eventEnd(e).Sub(e.StartsAt)
	end := e.StartsAt.Add(time.Nanosecond)
	if e.RRule != nil {
		end = e.StartsAt.AddDate(1, 0, 0).Add(time.Nanosecond)
	}
	occurrences, err := starts(e, e.StartsAt, end)
	if err != nil {
		return nil, err
	}
	var found []Conflict
	for _, other := range existing {
		if other.ID == e.ID || other.GroupID != e.GroupID || other.Status != "active" {
			continue
		}
		od := eventEnd(other).Sub(other.StartsAt)
		if od <= 0 {
			od = 95 * time.Minute
		}
		times, err := starts(other, e.StartsAt.Add(-od), end.Add(duration))
		if err != nil {
			return nil, err
		}
		var startsAt time.Time
		for _, a := range occurrences {
			for _, b := range times {
				if a.Before(b.Add(od)) && b.Before(a.Add(duration)) {
					startsAt = b
					break
				}
			}
			if !startsAt.IsZero() {
				break
			}
		}
		if !startsAt.IsZero() {
			found = append(found, Conflict{Event: other, StartsAt: startsAt})
		}
	}
	return found, nil
}

func (s Service) OverlapWarnings(ctx context.Context, e Event) ([]Conflict, error) {
	existing, err := s.Candidates(ctx)
	if err != nil {
		return nil, err
	}
	return conflicts(e, existing)
}

// Apply serializes target, conflict, and idempotency checks with the write.
func (s Service) Apply(ctx context.Context, p Proposal, chatID int64, messageID int) (Event, error) {
	e := p.Event
	var warnings []Conflict
	if e.GroupID != s.GroupID {
		return e, fmt.Errorf("wrong group")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return e, err
	}
	sourceID := fmt.Sprintf("%d:%d:%s", chatID, messageID, raw)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, err
	}
	defer tx.Rollback()
	var existingID int64
	err = tx.QueryRowContext(ctx, "SELECT e.id FROM event_sources es JOIN events e ON e.id=es.event_id WHERE es.source_type='telegram' AND es.external_id=? AND e.group_id=?", sourceID, s.GroupID).Scan(&existingID)
	if err == nil {
		if err = tx.Commit(); err != nil {
			return e, err
		}
		e = s.Get(ctx, existingID)
		if e.Status == "active" {
			e.Warnings, err = s.OverlapWarnings(ctx, e)
			if err != nil {
				return e, err
			}
		}
		return e, nil
	}
	if err != sql.ErrNoRows {
		return e, err
	}
	// Read complete snapshots inside the transaction, including tags.
	rows, err := tx.QueryContext(ctx, "SELECT id,kind,category,title,description,location,starts_at,ends_at,timezone,all_day,rrule,status FROM events WHERE group_id=? AND deleted_at IS NULL", s.GroupID)
	if err != nil {
		return e, err
	}
	var all []Event
	for rows.Next() {
		v := Event{GroupID: s.GroupID}
		var st string
		var en sql.NullString
		if err = rows.Scan(&v.ID, &v.Kind, &v.Category, &v.Title, &v.Description, &v.Location, &st, &en, &v.Timezone, &v.AllDay, &v.RRule, &v.Status); err != nil {
			rows.Close()
			return e, err
		}
		v.StartsAt, err = time.Parse(time.RFC3339Nano, st)
		if err != nil {
			rows.Close()
			return e, err
		}
		if en.Valid {
			t, er := time.Parse(time.RFC3339Nano, en.String)
			if er != nil {
				rows.Close()
				return e, er
			}
			v.EndsAt = &t
		}
		all = append(all, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return e, err
	}
	for i := range all {
		r, er := tx.QueryContext(ctx, "SELECT t.name FROM tags t JOIN event_tags et ON et.tag_id=t.id WHERE et.event_id=? ORDER BY t.name", all[i].ID)
		if er != nil {
			return e, er
		}
		for r.Next() {
			var tag string
			if er = r.Scan(&tag); er != nil {
				r.Close()
				return e, er
			}
			all[i].Tags = append(all[i].Tags, tag)
		}
		er = r.Err()
		r.Close()
		if er != nil {
			return e, er
		}
	}
	if p.Operation != "create" {
		matched := false
		for _, v := range all {
			if v.ID == e.ID && v.Status == "active" && Snapshot(v) == p.Before {
				matched = true
			}
		}
		if !matched {
			return e, fmt.Errorf("событие изменилось; повторите /event")
		}
	}
	if p.Operation != "cancel" {
		if err = Validate(e); err != nil {
			return e, err
		}
		c, er := conflicts(e, all)
		if er != nil {
			return e, er
		}
		warnings = c
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	switch p.Operation {
	case "create":
		e.Status = "active"
		res, er := tx.ExecContext(ctx, "INSERT INTO events(group_id,kind,category,title,description,location,starts_at,ends_at,timezone,all_day,rrule,status,source_type,dedupe_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,'active','telegram',?,?,?)", s.GroupID, e.Kind, e.Category, e.Title, e.Description, e.Location, e.StartsAt.UTC().Format(time.RFC3339Nano), timePtr(e.EndsAt), e.Timezone, e.AllDay, e.RRule, Canonical(e), now, now)
		if er != nil {
			return e, er
		}
		e.ID, err = res.LastInsertId()
	case "update":
		_, err = tx.ExecContext(ctx, "UPDATE events SET kind=?,category=?,title=?,description=?,location=?,starts_at=?,ends_at=?,timezone=?,all_day=?,rrule=?,dedupe_key=?,updated_at=? WHERE id=? AND group_id=?", e.Kind, e.Category, e.Title, e.Description, e.Location, e.StartsAt.UTC().Format(time.RFC3339Nano), timePtr(e.EndsAt), e.Timezone, e.AllDay, e.RRule, Canonical(e), now, e.ID, s.GroupID)
	case "cancel":
		e.Status = "cancelled"
		_, err = tx.ExecContext(ctx, "UPDATE events SET status='cancelled',updated_at=? WHERE id=? AND group_id=?", now, e.ID, s.GroupID)
	default:
		return e, fmt.Errorf("invalid operation")
	}
	if err != nil {
		return e, err
	}
	if p.Operation != "cancel" {
		if _, err = tx.ExecContext(ctx, "DELETE FROM event_tags WHERE event_id=?", e.ID); err != nil {
			return e, err
		}
		for _, tag := range e.Tags {
			tag = norm(tag)
			if tag == "" {
				continue
			}
			if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO tags(name) VALUES(?)", tag); err != nil {
				return e, err
			}
			if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO event_tags(event_id,tag_id) SELECT ?,id FROM tags WHERE name=?", e.ID, tag); err != nil {
				return e, err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO event_sources(event_id,source_type,telegram_chat_id,telegram_message_id,external_id,created_at) VALUES(?,'telegram',?,?,?,?)", e.ID, chatID, messageID, sourceID, now); err != nil {
		return e, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO change_log(group_id,kind,entity_type,entity_id,payload_json,created_at) VALUES(?,?,'event',?,?,?)", s.GroupID, "event_"+p.Operation, fmt.Sprint(e.ID), raw, now); err != nil {
		return e, err
	}
	if err = tx.Commit(); err != nil {
		return e, err
	}
	e.Warnings = warnings
	return e, nil
}

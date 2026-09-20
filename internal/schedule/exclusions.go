package schedule

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// ExcludeOccurrence removes one local calendar occurrence, never the series.
func (s Service) ExcludeOccurrence(ctx context.Context, id int64, date string, announce bool) (Event, error) {
	return s.ExcludeOccurrenceVersioned(ctx, id, date, "", announce)
}

// ExcludeOccurrenceVersioned removes one occurrence with the same optimistic
// concurrency and provenance guarantees as other external mutations.
func (s Service) ExcludeOccurrenceVersioned(ctx context.Context, id int64, date, expectedVersion string, announce bool) (Event, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, err
	}
	defer tx.Rollback()
	e, err := eventTx(ctx, tx, id, s.GroupID)
	if err != nil {
		return Event{}, err
	}
	if e.Status != "active" || e.RRule == nil {
		return Event{}, fmt.Errorf("active recurring series required")
	}
	mutation := mutationContext(ctx)
	sourceType := mutation.SourceType
	if sourceType == "" {
		sourceType = "telegram"
	}
	if mutation.ExternalID != "" {
		var existingID int64
		err = tx.QueryRowContext(ctx, "SELECT event_id FROM event_sources WHERE source_type=? AND external_id=?", sourceType, mutation.ExternalID).Scan(&existingID)
		if err == nil {
			if existingID != id {
				return Event{}, fmt.Errorf("source belongs to another event")
			}
			if err = tx.Commit(); err != nil {
				return Event{}, err
			}
			return s.Get(ctx, id), nil
		}
		if err != sql.ErrNoRows {
			return Event{}, err
		}
	}
	if expectedVersion != "" && expectedVersion != Version(e) {
		return e, ErrStaleVersion
	}
	loc, err := time.LoadLocation(e.Timezone)
	if err != nil {
		return Event{}, err
	}
	day, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return Event{}, fmt.Errorf("occurrence_date must be YYYY-MM-DD")
	}
	e.ExcludedDates = nil
	occurrences, err := starts(e, day, day.AddDate(0, 0, 1))
	if err != nil {
		return Event{}, err
	}
	if len(occurrences) != 1 {
		return Event{}, fmt.Errorf("date must identify exactly one series occurrence")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO event_exclusions(event_id,occurrence_date,created_at) VALUES(?,?,?)", id, date, now)
	if err != nil {
		return Event{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return Event{}, err
	}
	if n > 0 {
		occurrence := e
		occurrence.StartsAt = occurrences[0]
		if e.EndsAt != nil {
			end := occurrence.StartsAt.Add(e.EndsAt.Sub(e.StartsAt))
			occurrence.EndsAt = &end
		}
		occurrence.RRule = nil
		raw, err := json.Marshal(Proposal{Operation: "exclude_occurrence", Event: occurrence})
		if err != nil {
			return Event{}, err
		}
		change, err := tx.ExecContext(ctx, "INSERT INTO change_log(group_id,kind,entity_type,entity_id,payload_json,created_at) VALUES(?,'event_exclude_occurrence','event',?,?,?)", s.GroupID, fmt.Sprint(id), string(raw), now)
		if err != nil {
			return Event{}, err
		}
		if announce {
			changeID, err := change.LastInsertId()
			if err != nil {
				return Event{}, err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO group_announcement_changes(change_id,group_id,created_at) VALUES(?,?,?)", changeID, s.GroupID, now); err != nil {
				return Event{}, err
			}
		}
		if mutation.SourceType == "hermes" {
			if _, err = tx.ExecContext(ctx, "INSERT INTO admin_schedule_notifications(group_id,change_id,created_at) VALUES(?,?,?)", s.GroupID, changeID(change), now); err != nil {
				return Event{}, err
			}
		}
	}
	if mutation.ExternalID != "" {
		if _, err = tx.ExecContext(ctx, "INSERT INTO event_sources(event_id,source_type,external_id,raw_text,created_at) VALUES(?,?,?,?,?)", id, sourceType, mutation.ExternalID, mutation.SourceRef, now); err != nil {
			return Event{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Event{}, err
	}
	return s.Get(ctx, id), nil
}

type exclusionQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadExclusions(ctx context.Context, q exclusionQuery, id int64) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT occurrence_date FROM event_exclusions WHERE event_id=? ORDER BY occurrence_date", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var dates []string
	for rows.Next() {
		var date string
		if err = rows.Scan(&date); err != nil {
			return nil, err
		}
		dates = append(dates, date)
	}
	return dates, rows.Err()
}

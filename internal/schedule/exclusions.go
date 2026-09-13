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
	}
	if err = tx.Commit(); err != nil {
		return Event{}, err
	}
	return e, nil
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

package schedule

import (
	"fmt"
	"strings"
	"time"

	"github.com/teambition/rrule-go"
)

// normalizeRecurrence makes inferred recurring schedules finite. Explicit user
// limits always win; semester end is used only when no limit was supplied.
func (s Service) normalizeRecurrence(e Event) (Event, error) {
	if e.RRule == nil {
		return e, nil
	}
	rule := *e.RRule
	e.RRule = &rule
	if e.RecurrenceHorizon == "default" || e.RecurrenceHorizon == "semester" {
		return e, nil
	}
	opt, err := rrule.StrToROption(*e.RRule)
	if err != nil {
		return e, fmt.Errorf("invalid recurrence")
	}
	if opt.Count > 0 {
		e.RecurrenceHorizon = "explicit_count"
		return e, nil
	}
	if !opt.Until.IsZero() {
		e.RecurrenceHorizon = "explicit_until"
		return e, nil
	}
	loc := s.TZ
	if loc == nil {
		loc = time.UTC
	}
	start := e.StartsAt.In(loc)
	until := start.AddDate(0, 0, DefaultRecurrenceHorizonWeeks*7)
	e.RecurrenceHorizon = "default"
	if s.Semester.Contains(start, loc) {
		end := s.Semester.End.In(loc)
		until = time.Date(end.Year(), end.Month(), end.Day(), 23, 59, 59, 0, loc)
		e.RecurrenceHorizon = "semester"
	}
	// RRULE UNTIL is UTC. Keep the existing components rather than reserializing
	// so DTSTART remains solely in the event record.
	*e.RRule = strings.TrimSuffix(*e.RRule, ";") + ";UNTIL=" + until.UTC().Format("20060102T150405Z")
	return e, nil
}

// recurrenceIdentity excludes a server-generated limit from series identity.
func recurrenceIdentity(e Event) string {
	if e.RRule == nil {
		return ""
	}
	parts := rruleParts(*e.RRule)
	if e.RecurrenceHorizon == "default" || e.RecurrenceHorizon == "semester" {
		delete(parts, "UNTIL")
	}
	keys := []string{"FREQ", "INTERVAL", "BYDAY", "BYMONTHDAY", "BYMONTH", "COUNT", "UNTIL"}
	var out []string
	for _, key := range keys {
		if value := parts[key]; value != "" {
			out = append(out, key+"="+value)
		}
	}
	return strings.Join(out, ";")
}

// SameSemanticEvent compares import candidates without treating a generated
// recurrence horizon as a user-visible difference.
func SameSemanticEvent(left, right Event) bool {
	return strings.EqualFold(strings.TrimSpace(left.Title), strings.TrimSpace(right.Title)) &&
		left.StartsAt.Equal(right.StartsAt) && left.AllDay == right.AllDay && left.Category == right.Category &&
		sameEventEnd(left.EndsAt, right.EndsAt) && recurrenceIdentity(left) == recurrenceIdentity(right)
}

func sameEventEnd(left, right *time.Time) bool {
	return left == nil && right == nil || left != nil && right != nil && left.Equal(*right)
}

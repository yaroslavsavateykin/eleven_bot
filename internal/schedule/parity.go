package schedule

import (
	"fmt"
	"strings"
	"time"
)

// normalizeWeekParity anchors a parity-dependent event on the requested
// academic week. Recurring parity events have a restricted RRULE shape.
func (s Service) normalizeWeekParity(e Event, parity string) (Event, error) {
	if parity != "even" && parity != "odd" {
		return e, fmt.Errorf("invalid academic week parity")
	}
	if !s.WeekParity.Configured() {
		return e, fmt.Errorf("Не настроено, какая учебная неделя считается чётной. Укажите одну известную чётную или нечётную неделю.")
	}
	loc := s.TZ
	if loc == nil {
		loc = time.UTC
	}
	if e.RRule != nil {
		parts := rruleParts(*e.RRule)
		if parts["FREQ"] != "WEEKLY" || parts["INTERVAL"] != "2" || parts["BYDAY"] == "" || len(strings.Split(parts["BYDAY"], ",")) != 1 {
			return e, fmt.Errorf("academic week parity requires WEEKLY recurrence with INTERVAL=2 and one BYDAY")
		}
		day := []string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}[e.StartsAt.In(loc).Weekday()]
		if parts["BYDAY"] != day {
			return e, fmt.Errorf("recurrence BYDAY does not match DTSTART")
		}
	}
	actual, err := s.AcademicWeekParity(e.StartsAt)
	if err != nil {
		return e, err
	}
	if actual == parity {
		return e, nil
	}
	e.StartsAt = e.StartsAt.AddDate(0, 0, 7)
	if e.EndsAt != nil {
		end := e.EndsAt.AddDate(0, 0, 7)
		e.EndsAt = &end
	}
	return e, nil
}

func rruleParts(rule string) map[string]string {
	parts := make(map[string]string)
	for _, part := range strings.Split(rule, ";") {
		key, value, ok := strings.Cut(part, "=")
		if ok {
			parts[key] = value
		}
	}
	return parts
}

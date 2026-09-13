package schedule

import (
	"fmt"
	"strings"
	"time"
)

// RecurrenceSpec is the semantic recurrence contract used at application
// boundaries. RRULE is compiled deterministically only at persistence time.
type RecurrenceSpec struct {
	Frequency string     `json:"frequency"`
	Interval  int        `json:"interval,omitempty"`
	Weekdays  []string   `json:"weekdays,omitempty"`
	Count     *int       `json:"count,omitempty"`
	Until     *time.Time `json:"until,omitempty"`
}

func (r RecurrenceSpec) Compile() (*string, error) {
	freq := strings.ToUpper(strings.TrimSpace(r.Frequency))
	switch freq {
	case "DAILY", "WEEKLY", "MONTHLY", "YEARLY":
	default:
		return nil, fmt.Errorf("invalid recurrence frequency")
	}
	interval := r.Interval
	if interval == 0 {
		interval = 1
	}
	if interval < 1 || interval > 366 {
		return nil, fmt.Errorf("invalid recurrence interval")
	}
	if r.Count != nil && (*r.Count < 1 || *r.Count > 366) {
		return nil, fmt.Errorf("invalid recurrence count")
	}
	if r.Count != nil && r.Until != nil {
		return nil, fmt.Errorf("recurrence cannot have both count and until")
	}
	parts := []string{"FREQ=" + freq}
	if interval > 1 {
		parts = append(parts, fmt.Sprintf("INTERVAL=%d", interval))
	}
	if len(r.Weekdays) > 0 {
		if freq != "WEEKLY" {
			return nil, fmt.Errorf("weekdays require weekly recurrence")
		}
		seen := map[string]bool{}
		days := make([]string, 0, len(r.Weekdays))
		for _, day := range r.Weekdays {
			day = strings.ToUpper(strings.TrimSpace(day))
			if !map[string]bool{"MO": true, "TU": true, "WE": true, "TH": true, "FR": true, "SA": true, "SU": true}[day] || seen[day] {
				return nil, fmt.Errorf("invalid recurrence weekday")
			}
			seen[day] = true
			days = append(days, day)
		}
		parts = append(parts, "BYDAY="+strings.Join(days, ","))
	}
	if r.Count != nil {
		parts = append(parts, fmt.Sprintf("COUNT=%d", *r.Count))
	}
	if r.Until != nil {
		parts = append(parts, "UNTIL="+r.Until.UTC().Format("20060102T150405Z"))
	}
	rule := strings.Join(parts, ";")
	return &rule, nil
}

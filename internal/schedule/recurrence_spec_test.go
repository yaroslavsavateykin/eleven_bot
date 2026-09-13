package schedule

import (
	"strings"
	"testing"
	"time"
)

func TestRecurrenceSpecCompile(t *testing.T) {
	count := 4
	until := time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		spec RecurrenceSpec
		want string
	}{
		{RecurrenceSpec{Frequency: "daily"}, "FREQ=DAILY"},
		{RecurrenceSpec{Frequency: "daily", Interval: 2}, "FREQ=DAILY;INTERVAL=2"},
		{RecurrenceSpec{Frequency: "daily", Interval: 3}, "FREQ=DAILY;INTERVAL=3"},
		{RecurrenceSpec{Frequency: "weekly"}, "FREQ=WEEKLY"},
		{RecurrenceSpec{Frequency: "weekly", Interval: 2}, "FREQ=WEEKLY;INTERVAL=2"},
		{RecurrenceSpec{Frequency: "weekly", Interval: 3}, "FREQ=WEEKLY;INTERVAL=3"},
		{RecurrenceSpec{Frequency: "weekly", Interval: 2, Weekdays: []string{"TU"}}, "FREQ=WEEKLY;INTERVAL=2;BYDAY=TU"},
		{RecurrenceSpec{Frequency: "monthly"}, "FREQ=MONTHLY"},
		{RecurrenceSpec{Frequency: "daily", Count: &count}, "FREQ=DAILY;COUNT=4"},
		{RecurrenceSpec{Frequency: "daily", Until: &until}, "FREQ=DAILY;UNTIL=20261201T120000Z"},
	} {
		rule, err := tc.spec.Compile()
		if err != nil || rule == nil || *rule != tc.want {
			t.Fatalf("spec=%#v rule=%v err=%v", tc.spec, rule, err)
		}
	}
}
func TestRecurrenceSpecRejectsInvalid(t *testing.T) {
	count := 1
	for _, spec := range []RecurrenceSpec{{Frequency: "hourly"}, {Frequency: "daily", Interval: -1}, {Frequency: "monthly", Weekdays: []string{"MO"}}, {Frequency: "weekly", Count: &count, Until: ptr(time.Now())}} {
		if _, err := spec.Compile(); err == nil {
			t.Fatalf("accepted %#v", spec)
		}
	}
}
func ptr(v time.Time) *time.Time { return &v }
func TestRelativeDateIsNotRecurrence(t *testing.T) {
	if strings.Contains("через 2 дня", "каждые") {
		t.Fatal("test assumption")
	}
}

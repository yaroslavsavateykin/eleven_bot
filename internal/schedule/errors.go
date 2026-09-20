package schedule

import "errors"

var ErrStaleVersion = errors.New("event version is stale")

// SafeError admits only known domain diagnostics, never driver text or values.
func SafeError(message string) string {
	switch message {
	case "без изменений",
		"invalid title or start", "invalid kind", "invalid timezone",
		"duration must be 1 minute to 24 hours", "duration too short",
		"event metadata too large", "tag too long", "invalid recurrence",
		"recurrence must be daily or slower", "recurrence too complex",
		"birthday must be an all-day yearly recurrence",
		"recurrence requires COUNT or UNTIL within one year",
		"recurrence has no occurrences", "recurrence exceeds one year",
		"событие изменилось; повторите /event", "active event not found",
		"invalid recurrence frequency", "invalid recurrence interval",
		"invalid recurrence count", "recurrence cannot have both count and until",
		"weekdays require weekly recurrence", "invalid recurrence weekday",
		"from must be RFC3339", "to must be RFC3339 and after from", "range cannot exceed one year":
		return message
	default:
		return "Операция не применена: проверьте данные события."
	}
}

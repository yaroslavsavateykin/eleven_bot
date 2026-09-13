package schedule

// SafeError admits only known domain diagnostics, never driver text or values.
func SafeError(message string) string {
	switch message {
	case "без изменений", "invalid title or start", "invalid kind", "invalid timezone", "duration must be 1 minute to 24 hours", "duration too short", "event metadata too large", "tag too long", "invalid recurrence", "birthday must be an all-day yearly recurrence", "recurrence requires COUNT or UNTIL within one year", "recurrence exceeds one year", "событие изменилось; повторите /event", "active event not found":
		return message
	default:
		return "Операция не применена: проверьте данные события."
	}
}

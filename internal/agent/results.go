package agent

import (
	"encoding/json"
	"group411/internal/schedule"
)

func compactEvent(e schedule.Event) map[string]any {
	out := map[string]any{"id": e.ID, "title": e.Title, "kind": e.Kind, "starts_at": e.StartsAt, "ends_at": e.EndsAt, "all_day": e.AllDay, "status": e.Status}
	if e.RRule != nil {
		out["recurrence"] = *e.RRule
	}
	if e.Location != nil {
		out["location"] = *e.Location
	}
	if e.MergedDuplicateID != 0 {
		out["merged_duplicate_id"] = e.MergedDuplicateID
	}
	warnings := make([]any, 0, min(len(e.Warnings), 5))
	for i, w := range e.Warnings {
		if i == 5 {
			break
		}
		warnings = append(warnings, map[string]any{"title": w.Event.Title, "starts_at": w.StartsAt})
	}
	if len(warnings) > 0 {
		out["warnings"] = warnings
		out["warning_count"] = len(e.Warnings)
	}
	return out
}
func eventResult(e schedule.Event) (ToolResult, error) {
	raw, err := json.Marshal(compactEvent(e))
	return ToolResult{Content: string(raw)}, err
}
func batchResult(key string, r schedule.ImportResult) (ToolResult, error) {
	events := make([]any, 0, len(r.Events))
	for _, e := range r.Events {
		events = append(events, compactEvent(e))
	}
	skipped := make([]any, 0, len(r.Skipped))
	for _, s := range r.Skipped {
		skipped = append(skipped, map[string]any{"title": s.Proposal.Event.Title, "code": "not_applied", "message": schedule.SafeError(s.Reason)})
	}
	raw, err := json.Marshal(map[string]any{key: events, "skipped": skipped})
	return ToolResult{Content: string(raw)}, err
}

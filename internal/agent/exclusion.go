package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"group411/internal/schedule"
	"io"
)

type ScheduleExcludeTool struct {
	Schedule schedule.Service
	Announce bool
}

func (t ScheduleExcludeTool) Name() string { return "schedule_exclude_occurrence" }
func (t ScheduleExcludeTool) Description() string {
	return "Exclude exactly one date from a recurring series, preserving all other dates. Find the series ID with schedule_query first. For replacement exclude the original date and create a separate event. Never use schedule_cancel for a single occurrence."
}
func (t ScheduleExcludeTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["target_id","occurrence_date"],"properties":{"target_id":{"type":"integer","minimum":1},"occurrence_date":{"type":"string","description":"Local date YYYY-MM-DD in the series timezone"}},"additionalProperties":false}`)
}
func (t ScheduleExcludeTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var args struct {
		TargetID int64  `json:"target_id"`
		Date     string `json:"occurrence_date"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&args); err != nil || d.Decode(new(any)) != io.EOF || args.TargetID <= 0 || args.Date == "" {
		return ToolResult{}, &ArgumentError{"target_id and occurrence_date (YYYY-MM-DD) are required; unknown fields are not allowed"}
	}
	_, err := t.Schedule.ExcludeOccurrence(ctx, args.TargetID, args.Date, t.Announce)
	if err != nil {
		return ToolResult{}, err
	}
	data, err := json.Marshal(map[string]any{"series_id": args.TargetID, "excluded_date": args.Date, "series_preserved": true})
	return ToolResult{Content: string(data)}, err
}

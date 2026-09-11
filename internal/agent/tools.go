package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"group411/internal/schedule"
)

type ScheduleTodayTool struct{ Schedule schedule.Service }

func (t ScheduleTodayTool) Name() string { return "schedule_today" }
func (t ScheduleTodayTool) Description() string {
	return "Returns today's schedule and current/next event."
}
func (t ScheduleTodayTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false}`)
}
func (t ScheduleTodayTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	if err := emptyArgs(raw); err != nil {
		return ToolResult{}, err
	}
	st, err := t.Schedule.CurrentStatus(ctx, time.Now())
	if err != nil {
		return ToolResult{}, err
	}
	var rows []string
	for _, e := range st.Today {
		rows = append(rows, fmt.Sprintf("#%d %s %s", e.ID, e.StartsAt.In(t.Schedule.TZ).Format("02.01 15:04"), e.Title))
	}
	if len(rows) == 0 {
		return ToolResult{Content: "Сегодня событий нет."}, nil
	}
	return ToolResult{Content: strings.Join(rows, "\n")}, nil
}

type ScheduleStatusTool struct{ Schedule schedule.Service }

func (t ScheduleStatusTool) Name() string        { return "schedule_status" }
func (t ScheduleStatusTool) Description() string { return "Returns current and next schedule status." }
func (t ScheduleStatusTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false}`)
}
func (t ScheduleStatusTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	if err := emptyArgs(raw); err != nil {
		return ToolResult{}, err
	}
	st, err := t.Schedule.CurrentStatus(ctx, time.Now())
	if err != nil {
		return ToolResult{}, err
	}
	result := "Сейчас пар нет."
	if st.Current != nil {
		result = "Сейчас: " + st.Current.Title
	}
	if st.Next != nil {
		result += " Следующая: " + st.Next.Title + " в " + st.Next.StartsAt.In(t.Schedule.TZ).Format("15:04")
	}
	return ToolResult{Content: result}, nil
}

// ScheduleSearchTool exposes event lookup without granting mutation rights.
type ScheduleSearchTool struct{ Schedule schedule.Service }

func (t ScheduleSearchTool) Name() string { return "schedule_search" }
func (t ScheduleSearchTool) Description() string {
	return "Lists active schedule events; use it to identify an event before updating or cancelling it."
}

type ScheduleWeekParityTool struct{ Schedule schedule.Service }

func (t ScheduleWeekParityTool) Name() string { return "schedule_week_parity" }
func (t ScheduleWeekParityTool) Description() string {
	return "Returns the configured academic-week parity for today and next week."
}
func (t ScheduleWeekParityTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false}`)
}
func (t ScheduleWeekParityTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	if err := emptyArgs(raw); err != nil {
		return ToolResult{}, err
	}
	now := time.Now().In(t.Schedule.TZ)
	current, err := t.Schedule.AcademicWeekParity(now)
	if err != nil {
		return ToolResult{Content: err.Error()}, nil
	}
	next, err := t.Schedule.AcademicWeekParity(now.AddDate(0, 0, 7))
	if err != nil {
		return ToolResult{}, err
	}
	return ToolResult{Content: "Сейчас " + parityRussian(current) + " учебная неделя. Следующая " + parityRussian(next) + "."}, nil
}

func parityRussian(parity string) string {
	if parity == "even" {
		return "чётная"
	}
	return "нечётная"
}
func (t ScheduleSearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false}`)
}
func (t ScheduleSearchTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	if err := emptyArgs(raw); err != nil {
		return ToolResult{}, err
	}
	events, err := t.Schedule.Candidates(ctx)
	if err != nil {
		return ToolResult{}, err
	}
	if len(events) == 0 {
		return ToolResult{Content: "Активных событий нет."}, nil
	}
	data, err := json.Marshal(events)
	if err != nil {
		return ToolResult{}, err
	}
	return ToolResult{Content: string(data)}, nil
}

// EventOperationTool applies one already structured proposal. It deliberately
// delegates all validation, optimistic target checks, conflicts and persistence
// to schedule.Service instead of reimplementing schedule rules in the agent.
type EventOperationTool struct {
	Schedule  schedule.Service
	Operation string
}

func (t EventOperationTool) Name() string { return "event_" + t.Operation }
func (t EventOperationTool) Description() string {
	return "Applies one structured " + t.Operation + " proposal after server-side schedule validation."
}
func (t EventOperationTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["proposal"],"properties":{"proposal":{"type":"object","required":["operation","event"],"properties":{"operation":{"type":"string","enum":["create","update","cancel"]},"before":{"type":"string"},"week_parity":{"type":"string","enum":["","even","odd"]},"event":{"type":"object","properties":{"id":{"type":"integer"},"kind":{"type":"string","enum":["lesson","deadline","event","note","other"]},"category":{"type":"string"},"title":{"type":"string"},"description":{"type":["string","null"]},"location":{"type":["string","null"]},"starts_at":{"type":"string","description":"RFC3339 timestamp, e.g. 2026-09-17T12:40:00+03:00"},"ends_at":{"type":["string","null"],"description":"RFC3339 timestamp after starts_at"},"timezone":{"type":"string"},"all_day":{"type":"boolean"},"rrule":{"type":["string","null"]},"tags":{"type":"array","items":{"type":"string"}}}}}},"additionalProperties":false}`)
}
func (t EventOperationTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var args struct {
		Proposal schedule.Proposal `json:"proposal"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil || dec.Decode(new(any)) != io.EOF {
		if err == nil {
			err = fmt.Errorf("trailing JSON")
		}
		return ToolResult{}, fmt.Errorf("invalid event tool arguments: %w", err)
	}
	if args.Proposal.Operation != t.Operation {
		return ToolResult{}, fmt.Errorf("operation does not match tool")
	}
	args.Proposal.Event.GroupID = t.Schedule.GroupID
	event, err := t.Schedule.Apply(ctx, args.Proposal, 0, 0)
	if err != nil {
		return ToolResult{}, err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return ToolResult{}, err
	}
	return ToolResult{Content: string(data)}, nil
}

func emptyArgs(raw json.RawMessage) error {
	var args map[string]json.RawMessage
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil {
		return fmt.Errorf("invalid arguments")
	}
	if len(args) != 0 {
		return fmt.Errorf("arguments are not allowed")
	}
	return nil
}

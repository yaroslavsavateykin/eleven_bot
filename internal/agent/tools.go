package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"group411/internal/conversation"
	"group411/internal/schedule"
)

type ScheduleQueryTool struct{ Schedule schedule.Service }

func (t ScheduleQueryTool) Name() string { return "schedule_query" }
func (t ScheduleQueryTool) Description() string {
	return "Queries compact schedule occurrences. Use date ranges for today, tomorrow or a week; use query and limit=1 for the next matching event."
}
func (t ScheduleQueryTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"from":{"type":"string","description":"RFC3339 timestamp"},"to":{"type":"string","description":"RFC3339 timestamp, exclusive"},"query":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":20}},"additionalProperties":false}`)
}
func (t ScheduleQueryTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var args struct {
		From, To, Query string
		Limit           int
	}
	if err := decode(raw, &args); err != nil {
		return ToolResult{}, err
	}
	from, to, err := queryRange(args.From, args.To, t.Schedule.TZ)
	if err != nil {
		return ToolResult{}, err
	}
	events, err := t.Schedule.List(ctx, from, to, nil)
	if err != nil {
		return ToolResult{}, err
	}
	if args.Query != "" {
		filtered := events[:0]
		for _, event := range events {
			if matchesQuery(event.Title+" "+value(event.Description)+" "+value(event.Location), args.Query) {
				filtered = append(filtered, event)
			}
		}
		events = filtered
	}
	limit := args.Limit
	if limit <= 0 || limit > 20 {
		limit = 20
	}
	if len(events) > limit {
		events = events[:limit]
	}
	type result struct {
		ID       int64      `json:"id"`
		Title    string     `json:"title"`
		StartsAt time.Time  `json:"starts_at"`
		EndsAt   *time.Time `json:"ends_at,omitempty"`
		Location *string    `json:"location,omitempty"`
	}
	out := make([]result, 0, len(events))
	for _, event := range events {
		out = append(out, result{event.ID, event.Title, event.StartsAt, event.EndsAt, event.Location})
	}
	data, err := json.Marshal(out)
	return ToolResult{Content: string(data)}, err
}

// matchesQuery first preserves exact phrase matching, then accepts inflectional
// variants such as "радиохимия" and "радиохимии" without broad fuzzy matching.
func matchesQuery(text, query string) bool {
	text, query = normalizeSearch(text), normalizeSearch(query)
	if strings.Contains(text, query) {
		return true
	}
	for _, queryWord := range strings.Fields(query) {
		matched := false
		for _, textWord := range strings.Fields(text) {
			if commonPrefixRunes(queryWord, textWord) >= 5 {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return query != ""
}

func normalizeSearch(text string) string {
	text = strings.ToLower(strings.ReplaceAll(text, "ё", "е"))
	return strings.Map(func(r rune) rune {
		if r >= 'а' && r <= 'я' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return ' '
	}, text)
}

func commonPrefixRunes(a, b string) int {
	ar, br := []rune(a), []rune(b)
	n := len(ar)
	if len(br) < n {
		n = len(br)
	}
	for i := 0; i < n; i++ {
		if ar[i] != br[i] {
			return i
		}
	}
	return n
}

func queryRange(fromText, toText string, loc *time.Location) (time.Time, time.Time, error) {
	if loc == nil {
		loc = time.UTC
	}
	if fromText == "" {
		return time.Now().In(loc), time.Now().In(loc).AddDate(0, 0, 90), nil
	}
	from, err := time.Parse(time.RFC3339, fromText)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("from must be RFC3339")
	}
	if toText == "" {
		return from, from.AddDate(0, 0, 90), nil
	}
	to, err := time.Parse(time.RFC3339, toText)
	if err != nil || !to.After(from) {
		return time.Time{}, time.Time{}, fmt.Errorf("to must be RFC3339 and after from")
	}
	if to.After(from.AddDate(1, 0, 0)) {
		return time.Time{}, time.Time{}, fmt.Errorf("range cannot exceed one year")
	}
	return from, to, nil
}

type ScheduleMutationTool struct {
	Schedule  schedule.Service
	Operation string
	Announce  bool
}

func (t ScheduleMutationTool) Name() string { return "schedule_" + t.Operation }
func (t ScheduleMutationTool) Description() string {
	return "Safely " + t.Operation + " a schedule event through server-side validation."
}
func (t ScheduleMutationTool) Schema() json.RawMessage {
	if t.Operation == "cancel" {
		return json.RawMessage(`{"type":"object","required":["target_id"],"properties":{"target_id":{"type":"integer"}},"additionalProperties":false}`)
	}
	return json.RawMessage(`{"type":"object","required":["event"],"properties":{"target_id":{"type":"integer"},"event":{"type":"object","required":["kind","title","starts_at","ends_at","timezone"],"properties":{"kind":{"type":"string","enum":["lesson","deadline","event","note","other"]},"category":{"type":"string"},"title":{"type":"string"},"description":{"type":["string","null"]},"location":{"type":["string","null"]},"starts_at":{"type":"string","description":"RFC3339"},"ends_at":{"type":["string","null"],"description":"RFC3339"},"timezone":{"type":"string"},"all_day":{"type":"boolean"},"recurrence":{"type":["object","null"],"properties":{"frequency":{"type":"string","enum":["daily","weekly","monthly","yearly"]},"interval":{"type":"integer"},"weekdays":{"type":"array","items":{"type":"string","enum":["MO","TU","WE","TH","FR","SA","SU"]}},"count":{"type":["integer","null"]},"until":{"type":["string","null"]}},"additionalProperties":false},"tags":{"type":"array","items":{"type":"string"}}},"additionalProperties":false}},"additionalProperties":false}`)
}
func (t ScheduleMutationTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	if t.Operation == "cancel" {
		var args struct {
			TargetID int64 `json:"target_id"`
		}
		if err := decode(raw, &args); err != nil || args.TargetID <= 0 {
			return ToolResult{}, fmt.Errorf("invalid cancel target_id")
		}
		return t.apply(ctx, t.Schedule.Get(ctx, args.TargetID))
	}
	var args struct {
		TargetID int64      `json:"target_id"`
		Event    eventInput `json:"event"`
	}
	if err := decode(raw, &args); err != nil {
		return ToolResult{}, err
	}
	e, err := args.Event.Event()
	if err != nil {
		return ToolResult{}, err
	}
	if t.Operation == "update" {
		if args.TargetID <= 0 {
			return ToolResult{}, fmt.Errorf("update requires target_id")
		}
		current := t.Schedule.Get(ctx, args.TargetID)
		if current.ID == 0 || current.Status != "active" {
			return ToolResult{}, fmt.Errorf("active event not found")
		}
		e.ID, e.GroupID = current.ID, t.Schedule.GroupID
		return t.applyProposal(ctx, schedule.Proposal{Operation: "update", Event: e, Before: schedule.Snapshot(current), Announce: t.Announce})
	}
	if t.Operation != "create" {
		return ToolResult{}, fmt.Errorf("invalid schedule operation")
	}
	e.GroupID = t.Schedule.GroupID
	return t.applyProposal(ctx, schedule.Proposal{Operation: "create", Event: e, Announce: t.Announce})
}
func (t ScheduleMutationTool) apply(ctx context.Context, current schedule.Event) (ToolResult, error) {
	if current.ID == 0 || current.Status != "active" {
		return ToolResult{}, fmt.Errorf("active event not found")
	}
	return t.applyProposal(ctx, schedule.Proposal{Operation: "cancel", Event: current, Before: schedule.Snapshot(current), Announce: t.Announce})
}
func (t ScheduleMutationTool) applyProposal(ctx context.Context, proposal schedule.Proposal) (ToolResult, error) {
	event, err := t.Schedule.Apply(ctx, proposal, 0, 0)
	if err != nil {
		return ToolResult{}, err
	}
	data, err := json.Marshal(event)
	return ToolResult{Content: string(data)}, err
}

type eventInput struct {
	Kind        string                   `json:"kind"`
	Category    string                   `json:"category"`
	Title       string                   `json:"title"`
	Description *string                  `json:"description"`
	Location    *string                  `json:"location"`
	StartsAt    time.Time                `json:"starts_at"`
	EndsAt      *time.Time               `json:"ends_at"`
	Timezone    string                   `json:"timezone"`
	AllDay      bool                     `json:"all_day"`
	Recurrence  *schedule.RecurrenceSpec `json:"recurrence"`
	Tags        []string                 `json:"tags"`
}

func (in eventInput) Event() (schedule.Event, error) {
	e := schedule.Event{Kind: in.Kind, Category: in.Category, Title: in.Title, Description: in.Description, Location: in.Location, StartsAt: in.StartsAt, EndsAt: in.EndsAt, Timezone: in.Timezone, AllDay: in.AllDay, Tags: in.Tags, Status: "active"}
	if in.Recurrence != nil {
		rule, err := in.Recurrence.Compile()
		if err != nil {
			return e, err
		}
		e.RRule = rule
	}
	return e, nil
}

type GroupSearchTool struct {
	Conversation conversation.Service
	GroupID      int64
}

func (t GroupSearchTool) Name() string { return "group_search" }
func (t GroupSearchTool) Description() string {
	return "Searches persisted group messages for factual group history. Never infer facts not present in results."
}
func (t GroupSearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["query"],"properties":{"query":{"type":"string"},"from":{"type":"string"},"to":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":20}},"additionalProperties":false}`)
}
func (t GroupSearchTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var args struct {
		Query, From, To string
		Limit           int
	}
	if err := decode(raw, &args); err != nil {
		return ToolResult{}, err
	}
	var from, to *time.Time
	if args.From != "" {
		v, err := time.Parse(time.RFC3339, args.From)
		if err != nil {
			return ToolResult{}, fmt.Errorf("from must be RFC3339")
		}
		from = &v
	}
	if args.To != "" {
		v, err := time.Parse(time.RFC3339, args.To)
		if err != nil {
			return ToolResult{}, fmt.Errorf("to must be RFC3339")
		}
		to = &v
	}
	results, err := t.Conversation.Search(ctx, t.GroupID, args.Query, from, to, args.Limit)
	if err != nil {
		return ToolResult{}, err
	}
	data, err := json.Marshal(results)
	return ToolResult{Content: string(data)}, err
}

func decode(raw json.RawMessage, target any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil || dec.Decode(new(any)) != io.EOF {
		return fmt.Errorf("invalid tool arguments")
	}
	return nil
}
func value(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

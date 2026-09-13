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
	return "Finds schedule occurrences and recurring event series. Put only the subject/name into query, never weekday, date, pair number, or an action. Use the returned ID to update or cancel an event."
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
		if len(events) == 0 {
			events, err = t.matchSeries(ctx, args.Query)
			if err != nil {
				return ToolResult{}, err
			}
		}
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

func (t ScheduleQueryTool) matchSeries(ctx context.Context, query string) ([]schedule.Event, error) {
	words := subjectWords(query)
	if len(words) == 0 {
		return nil, nil
	}
	candidates, err := t.Schedule.Candidates(ctx)
	if err != nil {
		return nil, err
	}
	bestScore := 0
	matched := make([]schedule.Event, 0, len(candidates))
	for _, event := range candidates {
		score := 0
		for _, word := range words {
			for _, candidate := range strings.Fields(normalizeSearch(event.Title + " " + value(event.Description) + " " + value(event.Location))) {
				if commonPrefixRunes(word, candidate) >= 5 {
					score++
					break
				}
			}
		}
		if score > bestScore {
			bestScore = score
			matched = matched[:0]
		}
		if score > 0 && score == bestScore {
			matched = append(matched, event)
		}
	}
	return matched, nil
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

func subjectWords(query string) []string {
	ignored := map[string]bool{
		"в": true, "во": true, "на": true, "по": true, "и": true, "с": true, "до": true,
		"понедельник": true, "вторник": true, "среда": true, "четверг": true, "пятница": true, "суббота": true, "воскресенье": true,
		"следующий": true, "ближайший": true, "пара": true, "пары": true, "первой": true, "второй": true, "третьей": true, "четвертой": true, "пятой": true,
		"убери": true, "удали": true, "отмени": true, "исправь": true, "измени": true, "перенеси": true, "длину": true, "чтобы": true, "был": true,
	}
	var words []string
	for _, word := range strings.Fields(normalizeSearch(query)) {
		if len([]rune(word)) >= 5 && !ignored[word] {
			words = append(words, word)
		}
	}
	return words
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
		return time.Time{}, time.Time{}, &ArgumentError{"from must be RFC3339"}
	}
	if toText == "" {
		return from, from.AddDate(0, 0, 90), nil
	}
	to, err := time.Parse(time.RFC3339, toText)
	if err != nil || !to.After(from) {
		return time.Time{}, time.Time{}, &ArgumentError{"to must be RFC3339 and after from"}
	}
	if to.After(from.AddDate(1, 0, 0)) {
		return time.Time{}, time.Time{}, &ArgumentError{"range cannot exceed one year"}
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
	if t.Operation == "cancel" {
		return "Cancel an entire event or entire recurring series. NEVER use for one date in a series; use schedule_exclude_occurrence instead."
	}
	if t.Operation == "create_batch" {
		return "Creates multiple independent schedule events in one server-side batch. Use for pasted lists such as birthdays; valid events are saved while duplicates or invalid rows are reported."
	}
	if t.Operation == "update_batch" {
		return "Updates multiple existing schedule events in one server-side batch. Use after schedule_query when the same change applies to every found record."
	}
	if t.Operation == "create" {
		return `Создаёт одно событие расписания. Пример аргументов: {"event":{"kind":"lesson","category":"lesson","title":"Семинар по экономике","starts_at":"2026-09-14T15:00:00+03:00","ends_at":"2026-09-14T16:35:00+03:00","timezone":"Europe/Moscow","location":"235","recurrence":{"frequency":"weekly","interval":2,"weekdays":["MO"]}}}. category можно не указывать — сервер определит его сам по kind. timezone — часовой пояс группы.`
	}
	if t.Operation == "cancel" {
		return "Cancels an existing schedule event by target_id."
	}
	return "Safely " + t.Operation + " a schedule event through server-side validation."
}
func (t ScheduleMutationTool) schemaBase() json.RawMessage {
	if t.Operation == "cancel" {
		return json.RawMessage(`{"type":"object","required":["target_id"],"properties":{"target_id":{"type":"integer"}},"additionalProperties":false}`)
	}
	if t.Operation == "update" {
		return json.RawMessage(`{"type":"object","required":["target_id","changes"],"properties":{"target_id":{"type":"integer"},"changes":{"type":"object","properties":{"title":{"type":"string"},"starts_at":{"type":"string","description":"RFC3339"},"ends_at":{"type":"string","description":"RFC3339"},"location":{"type":["string","null"]},"description":{"type":["string","null"]},"all_day":{"type":"boolean"},"tags":{"type":"array","items":{"type":"string"}}},"additionalProperties":false}},"additionalProperties":false}`)
	}
	if t.Operation == "update_batch" {
		return json.RawMessage(`{"type":"object","required":["updates"],"properties":{"updates":{"type":"array","minItems":1,"items":{"type":"object","required":["target_id","changes"],"properties":{"target_id":{"type":"integer"},"changes":{"type":"object","properties":{"title":{"type":"string"},"starts_at":{"type":"string"},"ends_at":{"type":"string"},"location":{"type":["string","null"]},"description":{"type":["string","null"]},"all_day":{"type":"boolean"},"tags":{"type":"array","items":{"type":"string"}}},"additionalProperties":false}},"additionalProperties":false}}},"additionalProperties":false}`)
	}
	if t.Operation == "create_batch" {
		return json.RawMessage(`{"type":"object","required":["events"],"properties":{"events":{"type":"array","minItems":1,"items":{"type":"object","required":["kind","title","starts_at"],"properties":{"kind":{"type":"string","enum":["lesson","deadline","event","note","other","birthday"]},"category":{"type":"string","enum":["lesson","event","test","quiz","exam","deadline","other"],"description":"Категория занятия; можно не указывать — сервер определит по kind."},"title":{"type":"string"},"description":{"type":["string","null"]},"location":{"type":["string","null"]},"starts_at":{"type":"string"},"ends_at":{"type":["string","null"]},"timezone":{"type":"string","description":"Часовой пояс группы, например Europe/Moscow."},"all_day":{"type":"boolean"},"recurrence":{"type":["object","null"]},"tags":{"type":"array","items":{"type":"string"}}},"additionalProperties":false}}},"additionalProperties":false}`)
	}
	return json.RawMessage(`{"type":"object","required":["event"],"properties":{"target_id":{"type":"integer"},"event":{"type":"object","required":["kind","title","starts_at","ends_at","timezone"],"properties":{"kind":{"type":"string","enum":["lesson","deadline","event","note","other","birthday"]},"category":{"type":"string","enum":["lesson","event","test","quiz","exam","deadline","other"],"description":"Категория занятия; можно не указывать — сервер определит по kind."},"title":{"type":"string"},"description":{"type":["string","null"]},"location":{"type":["string","null"]},"starts_at":{"type":"string","description":"RFC3339"},"ends_at":{"type":["string","null"],"description":"RFC3339"},"timezone":{"type":"string","description":"Часовой пояс группы, например Europe/Moscow."},"all_day":{"type":"boolean"},"recurrence":{"type":["object","null"],"properties":{"frequency":{"type":"string","enum":["daily","weekly","monthly","yearly"]},"interval":{"type":"integer"},"weekdays":{"type":"array","items":{"type":"string","enum":["MO","TU","WE","TH","FR","SA","SU"]}},"count":{"type":["integer","null"]},"until":{"type":["string","null"]}},"additionalProperties":false},"tags":{"type":"array","items":{"type":"string"}}},"additionalProperties":false}},"additionalProperties":false}`)
}
func (t ScheduleMutationTool) Execute(ctx context.Context, raw json.RawMessage) (result ToolResult, err error) {
	defer func() {
		if err == nil {
			return
		}
		if _, ok := err.(*ArgumentError); ok {
			return
		}
		// Convert any domain or driver error into a safe model-facing message.
		// The typed validation errors above remain classified as invalid arguments.
		err = &ExecutionError{schedule.SafeError(err.Error())}
	}()
	if events, found, err := t.Schedule.InvocationResult(ctx); err != nil {
		return ToolResult{}, err
	} else if found {
		if len(events) != 1 {
			return ToolResult{}, fmt.Errorf("invalid persisted mutation result")
		}
		return eventResult(events[0])
	}
	if t.Operation == "cancel" {
		var args struct {
			TargetID int64 `json:"target_id"`
		}
		if err := decode(raw, &args); err != nil || args.TargetID <= 0 {
			return ToolResult{}, fmt.Errorf("invalid cancel target_id")
		}
		return t.apply(ctx, t.Schedule.Get(ctx, args.TargetID))
	}
	if t.Operation == "update" {
		return t.update(ctx, raw)
	}
	if t.Operation == "create_batch" {
		return t.createBatch(ctx, raw)
	}
	if t.Operation == "update_batch" {
		return t.updateBatch(ctx, raw)
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
	if t.Operation != "create" {
		return ToolResult{}, fmt.Errorf("invalid schedule operation")
	}
	e.GroupID = t.Schedule.GroupID
	return t.applyProposal(ctx, schedule.Proposal{Operation: "create", Event: e, Announce: t.Announce})
}

func (t ScheduleMutationTool) createBatch(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var args struct {
		Events []eventInput `json:"events"`
	}
	if err := decode(raw, &args); err != nil || len(args.Events) == 0 || len(args.Events) > 100 {
		return ToolResult{}, fmt.Errorf("invalid batch events")
	}
	entries := args.Events
	proposals := make([]schedule.Proposal, 0, len(entries))
	for _, input := range entries {
		if input.Kind == "" || strings.TrimSpace(input.Title) == "" || input.StartsAt.IsZero() {
			return ToolResult{}, fmt.Errorf("kind, title and starts_at are required")
		}
		if input.Kind == "birthday" {
			input = t.normalizeBirthday(input)
		}
		event, err := input.Event()
		if err != nil {
			return ToolResult{}, err
		}
		event.GroupID = t.Schedule.GroupID
		proposals = append(proposals, schedule.Proposal{Operation: "create", Event: event, Announce: t.Announce})
	}
	result, err := t.Schedule.ApplyImport(ctx, proposals, 0, 0)
	if err != nil {
		return ToolResult{}, err
	}
	return batchResult("created", result)
}

// Birthday lists commonly omit the birth year and end of the all-day interval.
// The calendar only needs month/day, so the server supplies deterministic values.
func (t ScheduleMutationTool) normalizeBirthday(input eventInput) eventInput {
	loc := t.Schedule.TZ
	if loc == nil {
		loc = time.UTC
	}
	if input.Timezone == "" {
		input.Timezone = loc.String()
	}
	if input.StartsAt.IsZero() {
		input.StartsAt = time.Date(time.Now().In(loc).Year(), 1, 1, 0, 0, 0, 0, loc)
	}
	if input.StartsAt.Year() <= 1 {
		now := time.Now().In(loc)
		input.StartsAt = time.Date(now.Year(), input.StartsAt.Month(), input.StartsAt.Day(), 0, 0, 0, 0, loc)
	}
	if input.EndsAt == nil {
		end := input.StartsAt.AddDate(0, 0, 1)
		input.EndsAt = &end
	}
	input.AllDay = true
	return input
}

func (t ScheduleMutationTool) update(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var args updateInput
	if err := decode(raw, &args); err != nil || args.TargetID <= 0 {
		return ToolResult{}, fmt.Errorf("invalid update arguments")
	}
	proposal, err := t.updateProposal(ctx, args)
	if err != nil {
		return ToolResult{}, err
	}
	return t.applyProposal(ctx, proposal)
}

type updateInput struct {
	TargetID int64 `json:"target_id"`
	Changes  struct {
		Title       *string        `json:"title"`
		StartsAt    *time.Time     `json:"starts_at"`
		EndsAt      *time.Time     `json:"ends_at"`
		Location    nullableString `json:"location"`
		Description nullableString `json:"description"`
		AllDay      *bool          `json:"all_day"`
		Tags        *[]string      `json:"tags"`
	} `json:"changes"`
}

type nullableString struct {
	Set   bool
	Value *string
}

func (s *nullableString) UnmarshalJSON(raw []byte) error {
	s.Set = true
	return json.Unmarshal(raw, &s.Value)
}

func (t ScheduleMutationTool) updateProposal(ctx context.Context, args updateInput) (schedule.Proposal, error) {
	current := t.Schedule.Get(ctx, args.TargetID)
	if current.ID == 0 || current.Status != "active" {
		return schedule.Proposal{}, fmt.Errorf("active event not found")
	}
	updated := current
	if args.Changes.Title != nil {
		updated.Title = *args.Changes.Title
	}
	if args.Changes.StartsAt != nil {
		updated.StartsAt = *args.Changes.StartsAt
	}
	if args.Changes.EndsAt != nil {
		updated.EndsAt = args.Changes.EndsAt
	}
	if args.Changes.Location.Set {
		updated.Location = args.Changes.Location.Value
	}
	if args.Changes.Description.Set {
		updated.Description = args.Changes.Description.Value
	}
	if args.Changes.AllDay != nil {
		updated.AllDay = *args.Changes.AllDay
	}
	if args.Changes.Tags != nil {
		updated.Tags = *args.Changes.Tags
	}
	return schedule.Proposal{Operation: "update", Event: updated, Before: schedule.Snapshot(current), Announce: t.Announce}, nil
}

func (t ScheduleMutationTool) updateBatch(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var args struct {
		Updates []updateInput `json:"updates"`
	}
	if err := decode(raw, &args); err != nil || len(args.Updates) == 0 {
		return ToolResult{}, fmt.Errorf("invalid batch updates")
	}
	proposals := make([]schedule.Proposal, 0, len(args.Updates))
	for index, update := range args.Updates {
		if events, found, err := t.Schedule.InvocationResult(schedule.ItemInvocation(ctx, index)); err != nil {
			return ToolResult{}, err
		} else if found && len(events) == 1 {
			// ApplyImport checks this item's receipt before validating its snapshot.
			proposals = append(proposals, schedule.Proposal{Operation: "update", Event: events[0]})
			continue
		}
		proposal, err := t.updateProposal(ctx, update)
		if err != nil {
			return ToolResult{}, err
		}
		proposals = append(proposals, proposal)
	}
	result, err := t.Schedule.ApplyImport(ctx, proposals, 0, 0)
	if err != nil {
		return ToolResult{}, err
	}
	return batchResult("updated", result)
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
	return eventResult(event)
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
	if e.Kind == "birthday" {
		e.Category = "other"
		e.AllDay = true
		if in.Recurrence == nil {
			rule := "FREQ=YEARLY"
			e.RRule = &rule
		}
	}
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
			return ToolResult{}, &ArgumentError{"from must be RFC3339"}
		}
		from = &v
	}
	if args.To != "" {
		v, err := time.Parse(time.RFC3339, args.To)
		if err != nil {
			return ToolResult{}, &ArgumentError{"to must be RFC3339"}
		}
		to = &v
	}
	results, err := t.Conversation.Search(ctx, t.GroupID, args.Query, from, to, args.Limit)
	if err != nil {
		return ToolResult{}, err
	}
	for i := range results {
		if len([]rune(results[i].Text)) > 600 {
			results[i].Text = string([]rune(results[i].Text)[:600]) + "…"
		}
	}
	data, err := json.Marshal(results)
	return ToolResult{Content: string(data)}, err
}

func decode(raw json.RawMessage, target any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if err := dec.Decode(target); err != nil || dec.Decode(new(any)) != io.EOF {
		return &ArgumentError{"Аргументы должны быть одним валидным JSON-объектом."}
	}
	return nil
}
func value(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

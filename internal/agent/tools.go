package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"

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
	if t.Operation == "create_batch" {
		return "Creates multiple independent schedule events in one server-side batch. Use for pasted lists such as birthdays; valid events are saved while duplicates or invalid rows are reported."
	}
	if t.Operation == "update_batch" {
		return "Updates multiple existing schedule events in one server-side batch. Use after schedule_query when the same change applies to every found record."
	}
	return "Safely " + t.Operation + " a schedule event through server-side validation."
}
func (t ScheduleMutationTool) Schema() json.RawMessage {
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
		return json.RawMessage(`{"type":"object","required":["events"],"properties":{"events":{"type":"array","minItems":1,"items":{"type":"object","required":["kind","title","starts_at"],"properties":{"kind":{"type":"string","enum":["lesson","deadline","event","note","other","birthday"]},"category":{"type":"string"},"title":{"type":"string"},"description":{"type":["string","null"]},"location":{"type":["string","null"]},"starts_at":{"type":"string"},"ends_at":{"type":["string","null"]},"timezone":{"type":"string"},"all_day":{"type":"boolean"},"recurrence":{"type":["object","null"]},"tags":{"type":"array","items":{"type":"string"}}},"additionalProperties":false}}},"additionalProperties":false}`)
	}
	return json.RawMessage(`{"type":"object","required":["event"],"properties":{"target_id":{"type":"integer"},"event":{"type":"object","required":["kind","title","starts_at","ends_at","timezone"],"properties":{"kind":{"type":"string","enum":["lesson","deadline","event","note","other","birthday"]},"category":{"type":"string"},"title":{"type":"string"},"description":{"type":["string","null"]},"location":{"type":["string","null"]},"starts_at":{"type":"string","description":"RFC3339"},"ends_at":{"type":["string","null"],"description":"RFC3339"},"timezone":{"type":"string"},"all_day":{"type":"boolean"},"recurrence":{"type":["object","null"],"properties":{"frequency":{"type":"string","enum":["daily","weekly","monthly","yearly"]},"interval":{"type":"integer"},"weekdays":{"type":"array","items":{"type":"string","enum":["MO","TU","WE","TH","FR","SA","SU"]}},"count":{"type":["integer","null"]},"until":{"type":["string","null"]}},"additionalProperties":false},"tags":{"type":"array","items":{"type":"string"}}},"additionalProperties":false}},"additionalProperties":false}`)
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
	entries, err := batchEntries(raw)
	if err != nil {
		return ToolResult{}, err
	}
	proposals := make([]schedule.Proposal, 0, len(entries))
	for _, entry := range entries {
		input, err := decodeFlexibleEvent(entry, true, t.Schedule.TZ)
		if err != nil {
			return ToolResult{}, err
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
	data, err := json.Marshal(struct {
		Created []schedule.Event            `json:"created"`
		Skipped []schedule.SkippedOperation `json:"skipped,omitempty"`
	}{Created: result.Events, Skipped: result.Skipped})
	return ToolResult{Content: string(data)}, err
}

func batchEntries(raw json.RawMessage) ([]json.RawMessage, error) {
	data := bytes.TrimSpace(raw)
	for len(data) > 0 && data[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return nil, fmt.Errorf("invalid batch arguments")
		}
		data = []byte(text)
	}
	var entries []json.RawMessage
	if json.Unmarshal(data, &entries) == nil && len(entries) > 0 {
		return entries, nil
	}
	var container map[string]json.RawMessage
	if err := json.Unmarshal(data, &container); err != nil {
		return nil, fmt.Errorf("invalid batch arguments")
	}
	for _, key := range []string{"events", "birthdays", "items", "data", "results"} {
		if value := container[key]; len(value) > 0 {
			if nested, err := batchEntries(value); err == nil && len(nested) > 0 {
				return nested, nil
			}
		}
	}
	return nil, fmt.Errorf("batch events are required")
}

// decodeFlexibleEvent accepts common model aliases while the domain layer still
// validates the final event before persistence.
func decodeFlexibleEvent(raw json.RawMessage, birthdayDefault bool, loc *time.Location) (eventInput, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return eventInput{}, fmt.Errorf("event must be an object")
	}
	alias := func(target, source string) {
		if len(fields[target]) == 0 && len(fields[source]) > 0 {
			fields[target] = fields[source]
		}
	}
	alias("title", "name")
	alias("title", "text")
	alias("starts_at", "date")
	alias("starts_at", "start")
	alias("ends_at", "end")
	if birthdayDefault && len(fields["kind"]) == 0 {
		fields["kind"] = json.RawMessage(`"birthday"`)
	}
	if date, ok := fields["starts_at"]; ok {
		var value string
		if json.Unmarshal(date, &value) == nil {
			parsed, err := flexibleTime(value, loc)
			if err != nil {
				return eventInput{}, fmt.Errorf("invalid event date %q", value)
			}
			fields["starts_at"], _ = json.Marshal(parsed.Format(time.RFC3339))
		}
	}
	data, _ := json.Marshal(fields)
	var input eventInput
	if err := decode(data, &input); err != nil {
		return eventInput{}, err
	}
	if strings.TrimSpace(input.Title) == "" || input.StartsAt.IsZero() {
		return eventInput{}, fmt.Errorf("event title and date are required")
	}
	return input, nil
}

func flexibleTime(value string, loc *time.Location) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	if loc == nil {
		loc = time.UTC
	}
	for _, layout := range []string{"2006-01-02", "02.01.2006", "2.1.2006"} {
		if parsed, err := time.ParseInLocation(layout, value, loc); err == nil {
			return parsed, nil
		}
	}
	parts := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(parts) == 2 || len(parts) == 3 {
		months := map[string]time.Month{"января": time.January, "февраля": time.February, "марта": time.March, "апреля": time.April, "мая": time.May, "июня": time.June, "июля": time.July, "августа": time.August, "сентября": time.September, "октября": time.October, "ноября": time.November, "декабря": time.December}
		month, ok := months[parts[1]]
		if !ok {
			return time.Time{}, fmt.Errorf("unsupported date")
		}
		day, err := strconv.Atoi(parts[0])
		if err != nil {
			return time.Time{}, fmt.Errorf("unsupported date")
		}
		year := time.Now().In(loc).Year()
		if len(parts) == 3 {
			year, err = strconv.Atoi(parts[2])
			if err != nil {
				return time.Time{}, fmt.Errorf("unsupported date")
			}
		}
		parsed := time.Date(year, month, day, 0, 0, 0, 0, loc)
		if parsed.Month() == month && parsed.Day() == day {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported date")
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
		Title       *string    `json:"title"`
		StartsAt    *time.Time `json:"starts_at"`
		EndsAt      *time.Time `json:"ends_at"`
		Location    **string   `json:"location"`
		Description **string   `json:"description"`
		AllDay      *bool      `json:"all_day"`
		Tags        *[]string  `json:"tags"`
	} `json:"changes"`
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
	if args.Changes.Location != nil {
		updated.Location = *args.Changes.Location
	}
	if args.Changes.Description != nil {
		updated.Description = *args.Changes.Description
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
	for _, update := range args.Updates {
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
	data, err := json.Marshal(struct {
		Updated []schedule.Event            `json:"updated"`
		Skipped []schedule.SkippedOperation `json:"skipped,omitempty"`
	}{Updated: result.Events, Skipped: result.Skipped})
	return ToolResult{Content: string(data)}, err
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

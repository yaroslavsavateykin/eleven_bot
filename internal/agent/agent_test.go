package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"group411/internal/conversation"
	"group411/internal/db"
	"group411/internal/schedule"
)

type fakeClient struct{ answers []string }

func (f *fakeClient) Complete(context.Context, string, string) (string, error) {
	answer := f.answers[0]
	f.answers = f.answers[1:]
	return answer, nil
}

type fakeTool struct {
	name  string
	calls int
}

func (t *fakeTool) Name() string          { return t.name }
func (*fakeTool) Description() string     { return "test" }
func (*fakeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *fakeTool) Execute(context.Context, json.RawMessage) (ToolResult, error) {
	t.calls++
	return ToolResult{Content: "ok"}, nil
}
func testInput() Conversation {
	return Conversation{Messages: []conversation.Message{{SenderType: conversation.SenderUser, Text: "объясни реакцию первого порядка"}}, Now: time.Now(), Timezone: "UTC"}
}

func TestAgentAnswersWithoutTool(t *testing.T) {
	result, err := (Agent{Client: &fakeClient{answers: []string{`{"reply":"Объяснение","tool_calls":[]}`}}}).Run(context.Background(), testInput())
	if err != nil || result.Reply != "Объяснение" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}
func TestAgentExecutesOnlyRegisteredTool(t *testing.T) {
	tool := &fakeTool{name: "schedule_query"}
	client := &fakeClient{answers: []string{`{"reply":"","tool_calls":[{"name":"schedule_query","arguments":{}}]}`, `{"reply":"Завтра пара.","tool_calls":[]}`}}
	var progress []string
	result, err := (Agent{Client: client, Tools: []Tool{tool}, Progress: func(text string) { progress = append(progress, text) }}).Run(context.Background(), testInput())
	if err != nil || result.Reply == "" || tool.calls != 1 || len(progress) != 1 || progress[0] != "Ищу нужное занятие в расписании…" {
		t.Fatalf("result=%#v calls=%d progress=%v err=%v", result, tool.calls, progress, err)
	}
}
func TestWriteToolsAreNotAvailableInGroupMode(t *testing.T) {
	read, write := &fakeTool{name: "schedule_query"}, &fakeTool{name: "schedule_create"}
	a := Agent{Tools: []Tool{read}, AdminTools: []Tool{write}}
	if len(a.ToolsFor(ModeGroup)) != 1 || len(a.ToolsFor(ModeGroupWrite)) != 2 || len(a.ToolsFor(ModeAdminPrivate)) != 2 {
		t.Fatal("wrong tool registry")
	}
}

func TestAgentDoesNotRepeatIdenticalToolCall(t *testing.T) {
	tool := &fakeTool{name: "schedule_query"}
	client := &fakeClient{answers: []string{
		`{"reply":"","tool_calls":[{"name":"schedule_query","arguments":{"query":"радиохимия"}}]}`,
		`{"reply":"","tool_calls":[{"name":"schedule_query","arguments":{"query":"радиохимия"}}]}`,
		`{"reply":"Нужно уточнить дату.","tool_calls":[]}`,
	}}
	result, err := (Agent{Client: client, Tools: []Tool{tool}}).Run(context.Background(), testInput())
	if err != nil || result.Reply != "Нужно уточнить дату." || tool.calls != 1 {
		t.Fatalf("result=%#v calls=%d err=%v", result, tool.calls, err)
	}
}

func TestAgentAllowsFinalReplyAfterToolRounds(t *testing.T) {
	tool := &fakeTool{name: "schedule_query"}
	client := &fakeClient{answers: []string{
		`{"reply":"","tool_calls":[{"name":"schedule_query","arguments":{"query":"один"}}]}`,
		`{"reply":"","tool_calls":[{"name":"schedule_query","arguments":{"query":"два"}}]}`,
		`{"reply":"Проверка завершена.","tool_calls":[]}`,
	}}
	result, err := (Agent{Client: client, Tools: []Tool{tool}, MaxRounds: 2}).Run(context.Background(), testInput())
	if err != nil || result.Reply != "Проверка завершена." || tool.calls != 2 {
		t.Fatalf("result=%#v calls=%d err=%v", result, tool.calls, err)
	}
}

func TestAgentDefaultsToTenToolRounds(t *testing.T) {
	tool := &fakeTool{name: "schedule_query"}
	answers := make([]string, 11)
	for i := range answers[:10] {
		answers[i] = fmt.Sprintf(`{"reply":"","tool_calls":[{"name":"schedule_query","arguments":{"query":"%d"}}]}`, i)
	}
	answers[10] = `{"reply":"Готово.","tool_calls":[]}`
	result, err := (Agent{Client: &fakeClient{answers: answers}, Tools: []Tool{tool}}).Run(context.Background(), testInput())
	if err != nil || result.Reply != "Готово." || tool.calls != 10 {
		t.Fatalf("result=%#v calls=%d err=%v", result, tool.calls, err)
	}
}

func TestScheduleCreateBatchCreatesBirthdays(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "agent-batch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err = database.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	tool := ScheduleMutationTool{Schedule: schedule.Service{DB: database, GroupID: 1, TZ: time.UTC}, Operation: "create_batch"}
	raw := json.RawMessage(`{"events":[{"kind":"birthday","title":"День рождения: Аня","starts_at":"2005-05-03T00:00:00Z","ends_at":"2005-05-04T00:00:00Z","timezone":"UTC","all_day":true},{"kind":"birthday","title":"День рождения: Борис","starts_at":"2006-03-04T00:00:00Z","ends_at":"2006-03-05T00:00:00Z","timezone":"UTC","all_day":true}]}`)
	result, err := tool.Execute(ctx, raw)
	if err != nil || !strings.Contains(result.Content, "День рождения: Аня") || !strings.Contains(result.Content, "День рождения: Борис") {
		t.Fatalf("result=%s err=%v", result.Content, err)
	}
	var count int
	if err = database.QueryRow("SELECT count(*) FROM events WHERE kind='birthday' AND all_day=1").Scan(&count); err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestScheduleQueryMatchesRussianInflections(t *testing.T) {
	if !matchesQuery("Практикум по радиохимии", "радиохимия") {
		t.Fatal("search did not match an inflectional form")
	}
	if matchesQuery("Практикум по электрохимии", "радиохимия") {
		t.Fatal("search matched an unrelated subject")
	}
}

func TestScheduleQueryFindsSeriesDespiteWeekdayWords(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "agent-tools.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err = database.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	svc := schedule.Service{DB: database, GroupID: 1, TZ: time.UTC}
	start := time.Now().UTC().AddDate(0, 0, 2).Truncate(time.Minute)
	end := start.Add(95 * time.Minute)
	rule := "FREQ=WEEKLY;COUNT=10"
	for _, title := range []string{"Семинар по экономике", "Практикум по экономике"} {
		event := schedule.Event{GroupID: 1, Kind: "lesson", Category: "lesson", Title: title, StartsAt: start, EndsAt: &end, Timezone: "UTC", RRule: &rule}
		if _, _, err = svc.Create(ctx, event, "test", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	result, err := (ScheduleQueryTool{Schedule: svc}).Execute(ctx, json.RawMessage(`{"query":"семинар по экономике во вторник","limit":5}`))
	if err != nil || !strings.Contains(result.Content, "Семинар по экономике") || strings.Contains(result.Content, "Практикум по экономике") {
		t.Fatalf("result=%s err=%v", result.Content, err)
	}
}

func TestScheduleUpdatePatchPreservesRecurrence(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "agent-update.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err = database.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	svc := schedule.Service{DB: database, GroupID: 1, TZ: time.UTC}
	start := time.Now().UTC().AddDate(0, 0, 2).Truncate(time.Minute)
	end := start.Add(5 * time.Hour)
	rule := "FREQ=WEEKLY;COUNT=10"
	created, _, err := svc.Create(ctx, schedule.Event{GroupID: 1, Kind: "lesson", Category: "lesson", Title: "Семинар по экономике", StartsAt: start, EndsAt: &end, Timezone: "UTC", RRule: &rule}, "test", "", "")
	if err != nil {
		t.Fatal(err)
	}
	newEnd := start.Add(95 * time.Minute)
	tool := ScheduleMutationTool{Schedule: svc, Operation: "update"}
	if _, err = tool.Execute(ctx, json.RawMessage(`{"target_id":`+fmt.Sprint(created.ID)+`,"changes":{"ends_at":"`+newEnd.Format(time.RFC3339)+`"}}`)); err != nil {
		t.Fatal(err)
	}
	updated := svc.Get(ctx, created.ID)
	if updated.RRule == nil || *updated.RRule != rule || updated.EndsAt == nil || !updated.EndsAt.Equal(newEnd) {
		t.Fatalf("updated=%#v", updated)
	}
}

func TestBirthdayEventIsAnnualAndAllDay(t *testing.T) {
	start := time.Date(2000, 5, 3, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	event, err := (eventInput{Kind: "birthday", Category: "birthday", Title: "День рождения: Аня", StartsAt: start, EndsAt: &end, Timezone: "UTC"}).Event()
	if err != nil || !event.AllDay || event.RRule == nil || *event.RRule != "FREQ=YEARLY" || event.Category != "other" {
		t.Fatalf("event=%#v err=%v", event, err)
	}
}

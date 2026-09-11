package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"group411/internal/conversation"
	"group411/internal/db"
	"group411/internal/schedule"
)

type fakeClient struct {
	answers []string
	err     error
	calls   int
}

func (f *fakeClient) Complete(context.Context, string, string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	if len(f.answers) == 0 {
		return "", errors.New("missing response")
	}
	v := f.answers[0]
	f.answers = f.answers[1:]
	return v, nil
}

type fakeTool struct {
	name  string
	err   error
	calls int
}

func (t *fakeTool) Name() string            { return t.name }
func (t *fakeTool) Description() string     { return "test" }
func (t *fakeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *fakeTool) Execute(context.Context, json.RawMessage) (ToolResult, error) {
	t.calls++
	if t.err != nil {
		return ToolResult{}, t.err
	}
	return ToolResult{Content: "tool result"}, nil
}
func input() Conversation {
	return Conversation{Messages: []conversation.Message{{SenderType: conversation.SenderUser, Text: "привет"}}, Now: time.Now(), Timezone: "UTC"}
}
func TestAgentTextAndToolRounds(t *testing.T) {
	client := &fakeClient{answers: []string{`{"reply":"готово","tool_calls":[]}`}}
	got, err := Agent{Client: client}.Run(context.Background(), input())
	if err != nil || got.Reply != "готово" {
		t.Fatal(got, err)
	}
	tool := &fakeTool{name: "x"}
	client = &fakeClient{answers: []string{`{"reply":"","tool_calls":[{"name":"x","arguments":{}}]}`, `{"reply":"итог","tool_calls":[]}`}}
	got, err = Agent{Client: client, Tools: []Tool{tool}}.Run(context.Background(), input())
	if err != nil || got.Reply != "итог" || tool.calls != 1 {
		t.Fatal(got, err, tool.calls)
	}
}

func TestAgentUsesStructuredTransportWhenAvailable(t *testing.T) {
	client := &structuredFakeClient{answer: `{"reply":"готово","tool_calls":[]}`}
	result, err := Agent{Client: client}.Run(context.Background(), input())
	if err != nil || result.Reply != "готово" || client.jsonCalls != 1 || client.plainCalls != 0 {
		t.Fatalf("result=%#v err=%v json=%d plain=%d", result, err, client.jsonCalls, client.plainCalls)
	}
}

type structuredFakeClient struct {
	answer                string
	jsonCalls, plainCalls int
}

func (c *structuredFakeClient) Complete(context.Context, string, string) (string, error) {
	c.plainCalls++
	return c.answer, nil
}
func (c *structuredFakeClient) CompleteJSON(context.Context, string, string) (string, error) {
	c.jsonCalls++
	return c.answer, nil
}

func TestAgentBoundsToolResultPrompt(t *testing.T) {
	toolResult := strings.Repeat("событие ", 2000)
	client := &promptClient{answers: []string{`{"reply":"","tool_calls":[{"name":"x","arguments":{}}]}`, `{"reply":"готово","tool_calls":[]}`}}
	largeTool := largeResultTool{content: toolResult}
	result, err := Agent{Client: client, Tools: []Tool{largeTool}}.Run(context.Background(), input())
	if err != nil || result.Reply != "готово" || len(client.prompts) != 2 || len(client.prompts[1]) > 5800 {
		t.Fatalf("result=%#v err=%v prompts=%d len=%d", result, err, len(client.prompts), len(client.prompts[1]))
	}
}

type largeResultTool struct{ content string }

func (largeResultTool) Name() string            { return "x" }
func (largeResultTool) Description() string     { return "test" }
func (largeResultTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t largeResultTool) Execute(context.Context, json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: t.content}, nil
}

type promptClient struct {
	answers []string
	prompts []string
}

func (c *promptClient) Complete(_ context.Context, _ string, prompt string) (string, error) {
	c.prompts = append(c.prompts, prompt)
	answer := c.answers[0]
	c.answers = c.answers[1:]
	return answer, nil
}
func TestAgentRejectsUnsafeCalls(t *testing.T) {
	for _, raw := range []string{`{"reply":"","tool_calls":[{"name":"sql","arguments":{}}]}`, `{"reply":"","tool_calls":[{"name":"x","arguments":{}},{"name":"x","arguments":{}}]}`} {
		_, err := Agent{Client: &fakeClient{answers: []string{raw}}, Tools: []Tool{&fakeTool{name: "x"}}}.Run(context.Background(), input())
		if err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	_, err := Agent{Client: &fakeClient{err: errors.New("down")}}.Run(context.Background(), input())
	if err == nil {
		t.Fatal("AI error accepted")
	}
	_, err = Agent{Client: &fakeClient{answers: []string{`{"reply":"","tool_calls":[{"name":"x","arguments":{"bad":1}}]}`}}, Tools: []Tool{&fakeTool{name: "x", err: errors.New("bad args")}}}.Run(context.Background(), input())
	if err == nil {
		t.Fatal("tool error accepted")
	}
}

func TestAgentRetriesAfterInvalidToolArguments(t *testing.T) {
	tool := &retryTool{name: "event_create"}
	client := &fakeClient{answers: []string{
		`{"reply":"","tool_calls":[{"name":"event_create","arguments":{}}]}`,
		`{"reply":"","tool_calls":[{"name":"event_create","arguments":{}}]}`,
		`{"reply":"сохранено","tool_calls":[]}`,
	}}
	result, err := Agent{Client: client, Tools: []Tool{tool}}.Run(context.Background(), input())
	if err != nil || result.Reply != "сохранено" || tool.calls != 2 || client.calls != 3 {
		t.Fatalf("result=%#v err=%v tool_calls=%d agent_calls=%d", result, err, tool.calls, client.calls)
	}
}

func TestCancelToolUsesTargetIDAndLoadsSnapshot(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "cancel-tool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err = database.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	svc := schedule.Service{DB: database, GroupID: 1, TZ: time.UTC}
	start := time.Now().UTC().AddDate(0, 0, 1).Truncate(time.Minute)
	end := start.Add(time.Hour)
	created, err := svc.Apply(ctx, schedule.Proposal{Operation: "create", Event: schedule.Event{GroupID: 1, Kind: "lesson", Category: "lesson", Title: "Практикум", StartsAt: start, EndsAt: &end, Timezone: "UTC"}}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	tool := EventOperationTool{Schedule: svc, Operation: "cancel"}
	if _, err = tool.Execute(ctx, json.RawMessage(fmt.Sprintf(`{"target_id":%d}`, created.ID))); err != nil {
		t.Fatal(err)
	}
	if got := svc.Get(ctx, created.ID).Status; got != "cancelled" {
		t.Fatalf("status=%q", got)
	}
}

func TestAgentAcceptsPlainGatewayReplyAfterTool(t *testing.T) {
	tool := &fakeTool{name: "event_create"}
	client := &fakeClient{answers: []string{
		`{"reply":"","tool_calls":[{"name":"event_create","arguments":{}}]}`,
		"Практикум добавлен на нечётные недели.",
	}}
	result, err := Agent{Client: client, Tools: []Tool{tool}}.Run(context.Background(), input())
	if err != nil || result.Reply != "Практикум добавлен на нечётные недели." || tool.calls != 1 {
		t.Fatalf("result=%#v err=%v tool_calls=%d", result, err, tool.calls)
	}
}

func TestAgentExtractsReplyFromMixedGatewayOutput(t *testing.T) {
	tool := &fakeTool{name: "event_create"}
	client := &fakeClient{answers: []string{
		`{"reply":"","tool_calls":[{"name":"event_create","arguments":{}}]}`,
		"Создано: Практикум.\n\n{\"reply\":\"Создано: Практикум.\",\"tool_calls\":[]}",
	}}
	result, err := Agent{Client: client, Tools: []Tool{tool}}.Run(context.Background(), input())
	if err != nil || result.Reply != "Создано: Практикум." {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

type retryTool struct {
	name  string
	calls int
}

func (t *retryTool) Name() string            { return t.name }
func (t *retryTool) Description() string     { return "test" }
func (t *retryTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *retryTool) Execute(context.Context, json.RawMessage) (ToolResult, error) {
	t.calls++
	if t.calls == 1 {
		return ToolResult{}, errors.New("invalid event tool arguments: missing starts_at")
	}
	return ToolResult{Content: "created"}, nil
}

func TestAgentRejectsTrailingJSON(t *testing.T) {
	_, err := Agent{Client: &fakeClient{answers: []string{`{"reply":"ok","tool_calls":[]} trailing`}}}.Run(context.Background(), input())
	if err == nil {
		t.Fatal("trailing response accepted")
	}
}

func TestAgentAddsGroupContextOnlyForScheduleQuestions(t *testing.T) {
	client := &capturingClient{answer: `{"reply":"В 18:20.","tool_calls":[]}`}
	_, err := Agent{Client: client}.Run(context.Background(), Conversation{Messages: []conversation.Message{{SenderType: conversation.SenderUser, Text: "Когда заканчивается пятая пара?"}}, Now: time.Now(), Timezone: "UTC"})
	if err != nil || !strings.Contains(client.system, "5 16:35–18:20") {
		t.Fatalf("system=%q err=%v", client.system, err)
	}
	client = &capturingClient{answer: `{"reply":"Привет","tool_calls":[]}`}
	_, err = Agent{Client: client}.Run(context.Background(), input())
	if err != nil || strings.Contains(client.system, "Контекст конкретной учебной группы") {
		t.Fatalf("system=%q err=%v", client.system, err)
	}
}

func TestAdminPrivateToolRegistryIncludesWriteTools(t *testing.T) {
	read := []Tool{&fakeTool{name: "schedule_today"}, &fakeTool{name: "schedule_status"}, &fakeTool{name: "schedule_search"}}
	write := []Tool{&fakeTool{name: "event_create"}, &fakeTool{name: "event_update"}, &fakeTool{name: "event_cancel"}}
	a := Agent{Tools: read, AdminTools: write}
	for _, tc := range []struct {
		mode Mode
		want []string
	}{
		{ModeGroup, []string{"schedule_today", "schedule_status", "schedule_search"}},
		{ModeAdminPrivate, []string{"schedule_today", "schedule_status", "schedule_search", "event_create", "event_update", "event_cancel"}},
	} {
		got := a.ToolsFor(tc.mode)
		if len(got) != len(tc.want) {
			t.Fatalf("mode=%s tools=%d want=%d", tc.mode, len(got), len(tc.want))
		}
		for i, tool := range got {
			if tool.Name() != tc.want[i] {
				t.Fatalf("mode=%s tool[%d]=%s want=%s", tc.mode, i, tool.Name(), tc.want[i])
			}
		}
	}
}

func TestAdminPrivateAgentOwnsIntentResolution(t *testing.T) {
	client := &capturingClient{answer: `{"reply":"На месте.","tool_calls":[]}`}
	_, err := Agent{Client: client}.Run(context.Background(), Conversation{Messages: []conversation.Message{{SenderType: conversation.SenderUser, Text: "Ты здесь?"}}, Now: time.Now(), Timezone: "UTC", Mode: ModeAdminPrivate})
	if err != nil || !strings.Contains(client.system, "Сам анализируй смысл текущего сообщения") || !strings.Contains(client.system, "event tool") {
		t.Fatalf("system=%q err=%v", client.system, err)
	}
}

func TestAdminPrivateEventCreateToolIsAvailableAndAppliesProposal(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err = database.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	svc := schedule.Service{DB: database, GroupID: 1, TZ: time.UTC}
	a := Agent{AdminTools: []Tool{EventOperationTool{Schedule: svc, Operation: "create"}}}
	tools := a.ToolsFor(ModeAdminPrivate)
	if len(tools) != 1 || tools[0].Name() != "event_create" {
		t.Fatalf("private registry=%v", tools)
	}
	start := time.Now().UTC().AddDate(0, 0, 1).Truncate(time.Minute)
	end := start.Add(105 * time.Minute)
	proposal := schedule.Proposal{Operation: "create", Event: schedule.Event{Kind: "lesson", Category: "lesson", Title: "Физическая химия", StartsAt: start, EndsAt: &end, Timezone: "UTC"}}
	raw, err := json.Marshal(map[string]any{"proposal": proposal})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tools[0].Execute(ctx, raw); err != nil {
		t.Fatalf("event_create failed: %v", err)
	}
	if events, err := svc.Candidates(ctx); err != nil || len(events) != 1 || events[0].Title != "Физическая химия" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

type capturingClient struct{ system, answer string }

func (f *capturingClient) Complete(_ context.Context, system, _ string) (string, error) {
	f.system = system
	return f.answer, nil
}

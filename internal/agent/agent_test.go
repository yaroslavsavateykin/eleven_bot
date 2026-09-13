package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"group411/internal/conversation"
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
	result, err := (Agent{Client: client, Tools: []Tool{tool}}).Run(context.Background(), testInput())
	if err != nil || result.Reply == "" || tool.calls != 1 {
		t.Fatalf("result=%#v calls=%d err=%v", result, tool.calls, err)
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

func TestAgentReturnsSafeReplyWhenToolRoundsAreExhausted(t *testing.T) {
	tool := &fakeTool{name: "schedule_query"}
	client := &fakeClient{answers: []string{
		`{"reply":"","tool_calls":[{"name":"schedule_query","arguments":{"query":"один"}}]}`,
		`{"reply":"","tool_calls":[{"name":"schedule_query","arguments":{"query":"два"}}]}`,
	}}
	result, err := (Agent{Client: client, Tools: []Tool{tool}, MaxRounds: 2}).Run(context.Background(), testInput())
	if err != nil || result.Reply == "" || tool.calls != 2 {
		t.Fatalf("result=%#v calls=%d err=%v", result, tool.calls, err)
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

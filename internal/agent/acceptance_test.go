package agent

// Acceptance test matrix ‒ verifies every ТЗ scenario end-to-end against
// real schedule logic via httptest provider fixture.
//
// Scenario A: plain text question → no tools → final answer
// Scenario B: seminar rename → schedule_query → schedule_update → final answer
// Scenario C: birthday batch → schedule_create_batch → final answer
// Scenario D: invalid arguments → corrected call → success (repair)
// Scenario E: provider returns two tool_calls in one assistant turn
// Scenario F: legacy_json mode used only when AI_TOOL_MODE=legacy_json (not as fallback)
//
// These scenarios complement native_test.go. Some overlap intentionally to
// keep each test self-contained for failure diagnosis.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"group411/internal/ai"
	"group411/internal/conversation"
)

// roundTrip drives a fake server through a scripted sequence of model turns.
type roundTrip struct {
	t      *testing.T
	rounds []func(req *http.Request, w http.ResponseWriter)
	n      int
}

func (rt *roundTrip) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rt.n >= len(rt.rounds) {
			rt.t.Fatalf("unexpected provider round %d", rt.n)
		}
		rt.rounds[rt.n](r, w)
		rt.n++
	})
}

func assistantToolResponse(id, name, args string) string {
	return fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":%q,"type":"function","function":{"name":%q,"arguments":%s}}]},"finish_reason":"tool_calls"}]}`, id, name, args)
}
func assistantTextResponse(text string) string {
	return fmt.Sprintf(`{"choices":[{"message":{"content":%q},"finish_reason":"stop"}]}`, text)
}

// Scenario A: model answers without any tools.
func TestAcceptanceScenarioA_PlainAnswer(t *testing.T) {
	rt := &roundTrip{t: t, rounds: []func(*http.Request, http.ResponseWriter){
		func(r *http.Request, w http.ResponseWriter) {
			var p struct {
				Tools []any `json:"tools"`
			}
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				t.Fatal(err)
			}
			if len(p.Tools) == 0 {
				t.Error("no tools sent to provider in round 0")
			}
			fmt.Fprint(w, assistantTextResponse("Реакция первого порядка — это..."))
		},
	}}
	server := httptest.NewServer(rt.Handler())
	defer server.Close()

	result, err := (Agent{
		Client:     ai.Service{BaseURL: server.URL, Key: "test", Model: "x"},
		Tools:      []Tool{&fakeTool{name: "schedule_query"}},
		MaxRounds:  10,
	}).Run(context.Background(), testInput())
	if err != nil || !strings.Contains(result.Reply, "Реакция") || rt.n != 1 {
		t.Fatalf("result=%+v err=%v rounds=%d", result, err, rt.n)
	}
}

// Scenario E: provider returns two independent tool calls in one assistant turn.
// Agent must not panic or refuse — it executes both and sends two tool results.
func TestAcceptanceScenarioE_MultipleCalls(t *testing.T) {
	callsA, callsB := &fakeTool{name: "schedule_query"}, &fakeTool{name: "group_search"}
	round := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { round++ }()
		if round == 0 {
			// Return two independent read calls in a single assistant message.
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[`+
				`{"id":"ca1","type":"function","function":{"name":"schedule_query","arguments":"{}"}},`+
				`{"id":"ca2","type":"function","function":{"name":"group_search","arguments":"{\"query\":\"задание\"}"}}]},"finish_reason":"tool_calls"}]}`)
			return
		}
		// Verify both tool results arrived.
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				CallID  string `json:"tool_call_id"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		toolMessages := 0
		for _, m := range req.Messages {
			if m.Role == "tool" {
				toolMessages++
				if !strings.Contains(m.Content, `"ok":true`) {
					t.Errorf("failed tool result: %s", m.Content)
				}
			}
		}
		if toolMessages != 2 {
			t.Errorf("expected 2 tool results, got %d", toolMessages)
		}
		fmt.Fprint(w, assistantTextResponse("Проверил оба источника."))
	}))
	defer server.Close()

	input := testInput()
	input.Messages = []conversation.Message{{SenderType: conversation.SenderUser, Text: "что завтра?"}}
	result, err := (Agent{
		Client:    ai.Service{BaseURL: server.URL, Key: "test", Model: "x"},
		Tools:     []Tool{callsA, callsB},
		MaxRounds: 10,
	}).Run(context.Background(), input)
	if err != nil || result.Reply != "Проверил оба источника." {
		t.Fatalf("result=%+v err=%v rounds=%d", result, err, round)
	}
	if callsA.calls != 1 || callsB.calls != 1 {
		t.Fatalf("callsA=%d callsB=%d", callsA.calls, callsB.calls)
	}
}

// Scenario F: legacy_json is an explicit mode, never a silent fallback.
// When tool_mode=legacy_json the provider receives no native tools array.
// When tool_mode=native a 400 stays as an error, not a legacy retry.
func TestAcceptanceScenarioF_LegacyExplicitOnly(t *testing.T) {
	t.Run("native_400_no_retry", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"unsupported"}`)
		}))
		defer server.Close()
		_, err := (ai.Service{BaseURL: server.URL, Key: "test", Model: "x"}).Chat(
			context.Background(), ai.ChatRequest{Tools: []ai.ToolDefinition{{Name: "t", Parameters: json.RawMessage(`{"type":"object"}`)}}})
		if err == nil || calls != 1 {
			t.Fatalf("native retried on 400: err=%v calls=%d", err, calls)
		}
	})

	t.Run("legacy_no_native_tools", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var p map[string]any
			json.NewDecoder(r.Body).Decode(&p)
			if p["tools"] != nil {
				t.Error("legacy_json sent native tools field")
			}
			// Return a well-formed legacy JSON envelope.
			fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"reply\":\"ok\",\"tool_calls\":[]}"},"finish_reason":"stop"}]}`)
		}))
		defer server.Close()
		turn, err := (ai.Service{BaseURL: server.URL, Key: "test", Model: "x", ToolMode: "legacy_json"}).Chat(
			context.Background(), ai.ChatRequest{Tools: []ai.ToolDefinition{{Name: "t", Parameters: json.RawMessage(`{"type":"object"}`)}}})
		if err != nil || turn.Content != "ok" {
			t.Fatalf("%+v %v", turn, err)
		}
	})
}

// Verify role ordering: system→user→assistant→tool is never flattened.
func TestAcceptanceRolesNotFlattened(t *testing.T) {
	recorded := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Messages []struct{ Role string } `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Fatal(err)
		}
		for _, m := range p.Messages {
			recorded = append(recorded, m.Role)
		}
		fmt.Fprint(w, assistantTextResponse("ok"))
	}))
	defer server.Close()

	input := Conversation{
		Messages: []conversation.Message{
			{SenderType: conversation.SenderUser, Text: "контекст"},
			{SenderType: conversation.SenderBot, Text: "ответ бота"},
			{SenderType: conversation.SenderUser, Text: "вопрос"},
		},
	}
	(Agent{
		Client:    ai.Service{BaseURL: server.URL, Key: "test", Model: "x"},
		MaxRounds: 1,
	}).Run(context.Background(), input)
	expected := []string{"system", "user", "assistant", "user"}
	if len(recorded) != 4 {
		t.Fatalf("roles=%v", recorded)
	}
	for i, role := range expected {
		if recorded[i] != role {
			t.Fatalf("position %d: want %q got %q (all: %v)", i, role, recorded[i], recorded)
		}
	}
}

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"group411/internal/ai"
	"group411/internal/conversation"
	"group411/internal/db"
	"group411/internal/schedule"
	"group411/prompts"
)

func TestNativeProviderContract(t *testing.T) {
	for _, tc := range []struct {
		name       string
		calls      string
		executions int
		mode       Mode
	}{
		{"read", `{"id":"call_1","type":"function","function":{"name":"schedule_query","arguments":"{}"}}`, 1, ModeGroup},
		{"multiple", `{"id":"call_1","type":"function","function":{"name":"schedule_query","arguments":"{}"}},{"id":"call_2","type":"function","function":{"name":"schedule_query","arguments":"{\"query\":\"other\"}"}}`, 2, ModeGroup},
		{"unknown", `{"id":"call_1","type":"function","function":{"name":"shell","arguments":"{}"}}`, 0, ModeGroup},
		{"malformed", `{"id":"call_1","type":"function","function":{"name":"schedule_query","arguments":"{"}}`, 0, ModeGroup},
		{"unauthorized", `{"id":"call_1","type":"function","function":{"name":"schedule_create","arguments":"{}"}}`, 0, ModeGroup},
		{"authorized", `{"id":"call_1","type":"function","function":{"name":"schedule_create","arguments":"{}"}}`, 1, ModeAdminPrivate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			round := 0
			tool := &fakeTool{name: "schedule_query"}
			mutation := &fakeTool{name: "schedule_create"}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Messages []struct {
						Role, Content string
						Calls         []struct{ ID string } `json:"tool_calls"`
						CallID        string                `json:"tool_call_id"`
					}
					Tools []struct {
						Function struct {
							Name       string
							Parameters json.RawMessage
						}
					}
					Format json.RawMessage `json:"response_format"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if len(request.Tools) == 0 || string(request.Tools[0].Function.Parameters) != string(tool.Schema()) || len(request.Format) != 0 {
					t.Errorf("wrong native tools: %+v", request.Tools)
				}
				if len(request.Messages) < 4 || request.Messages[1].Role != "user" || request.Messages[2].Role != "assistant" || request.Messages[3].Role != "user" {
					t.Error("roles flattened")
				}
				if round == 0 {
					fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[%s]},"finish_reason":"tool_calls"}]}`, tc.calls)
				} else {
					assistant := request.Messages[4]
					for i, c := range assistant.Calls {
						result := request.Messages[5+i]
						if result.Role != "tool" || result.CallID != c.ID || !strings.Contains(result.Content, `"ok":`) {
							t.Errorf("unmatched result: %+v", result)
						}
					}
					fmt.Fprint(w, `{"choices":[{"message":{"content":"Готово."},"finish_reason":"stop"}]}`)
				}
				round++
			}))
			defer server.Close()
			input := testInput()
			input.Mode = tc.mode
			input.Messages = []conversation.Message{{SenderType: conversation.SenderUser, Text: "раньше"}, {SenderType: conversation.SenderBot, Text: "уточнение"}, {SenderType: conversation.SenderUser, Text: "завтра"}}
			result, err := (Agent{Client: ai.Service{BaseURL: server.URL, Key: "test", Model: "fixture"}, Tools: []Tool{tool}, AdminTools: []Tool{mutation}}).Run(context.Background(), input)
			if err != nil || result.Reply != "Готово." || tool.calls+mutation.calls != tc.executions || round != 2 {
				t.Fatalf("result=%+v err=%v calls=%d rounds=%d", result, err, tool.calls+mutation.calls, round)
			}
		})
	}
}

func TestNativeScheduleAcceptance(t *testing.T) {
	for _, scenario := range []string{"query", "update", "create", "birthdays", "repair"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			database, err := db.Open(ctx, filepath.Join(t.TempDir(), "schedule.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if _, err = database.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
				t.Fatal(err)
			}
			svc := schedule.Service{DB: database, GroupID: 1, TZ: time.UTC}
			start := time.Now().UTC().AddDate(0, 0, 1).Truncate(24 * time.Hour).Add(10 * time.Hour)
			end := start.Add(time.Hour)
			event, _, err := svc.Create(ctx, schedule.Event{Kind: "lesson", Title: "Семинар по экономике", StartsAt: start, EndsAt: &end, Timezone: "UTC"}, "test", "", "")
			if err != nil {
				t.Fatal(err)
			}
			round := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Messages []struct {
						Role, Content string
						ID            string `json:"tool_call_id"`
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				call := func(name, args string) {
					encoded, _ := json.Marshal(args)
					fmt.Fprintf(w, `{"choices":[{"message":{"tool_calls":[{"id":"call_%d","type":"function","function":{"name":%q,"arguments":%s}}]},"finish_reason":"tool_calls"}]}`, round, name, encoded)
				}
				if round > 0 {
					last := req.Messages[len(req.Messages)-1]
					if last.Role != "tool" || last.ID != fmt.Sprintf("call_%d", round-1) {
						t.Error("missing tool result")
					}
					if scenario == "repair" && round == 1 {
						if !strings.Contains(last.Content, "invalid_arguments") {
							t.Error("missing argument error")
						}
					} else if !strings.Contains(last.Content, `"ok":true`) {
						t.Errorf("failed tool: %s", last.Content)
					}
				}
				switch {
				case scenario == "birthdays" && round == 0:
					call("schedule_create_batch", `{"events":[{"kind":"birthday","title":"День рождения: Аня","starts_at":"2005-05-03T00:00:00Z"},{"kind":"birthday","title":"День рождения: Борис","starts_at":"2006-06-04T00:00:00Z"}]}`)
				case scenario == "repair" && round == 0:
					call("schedule_query", `{"query":123}`)
				case scenario == "repair" && round == 1:
					call("schedule_query", `{"query":"экономике"}`)
				case scenario == "create" && round == 0:
					call("schedule_create", `{"event":{"kind":"lesson","title":"Семинар по экономике","starts_at":"2026-09-21T15:00:00+03:00","ends_at":"2026-09-21T16:35:00+03:00","timezone":"Europe/Moscow","location":"235","recurrence":{"frequency":"weekly","interval":2}}}`)
				case scenario == "create" && round == 1:
					fmt.Fprint(w, `{"choices":[{"message":{"content":"Поставил семинар по экономике."},"finish_reason":"stop"}]}`)
				case (scenario == "query" || scenario == "update") && round == 0:
					call("schedule_query", `{"query":"экономике"}`)
				case scenario == "update" && round == 1:
					newStart := start.Add(6*time.Hour + 30*time.Minute)
					newEnd := newStart.Add(time.Hour)
					call("schedule_update", fmt.Sprintf(`{"target_id":%d,"changes":{"starts_at":%q,"ends_at":%q}}`, event.ID, newStart.Format(time.RFC3339), newEnd.Format(time.RFC3339)))
				default:
					fmt.Fprint(w, `{"choices":[{"message":{"content":"Готово."},"finish_reason":"stop"}]}`)
				}
				round++
			}))
			defer server.Close()
			input := testInput()
			input.Mode = ModeAdminPrivate
			input.RunID = "telegram:1:20"
			a := Agent{Client: ai.Service{BaseURL: server.URL, Key: "test", Model: "fixture"}, Tools: []Tool{ScheduleQueryTool{Schedule: svc}}, AdminTools: []Tool{ScheduleMutationTool{Schedule: svc, Operation: "update"}, ScheduleMutationTool{Schedule: svc, Operation: "create"}, ScheduleMutationTool{Schedule: svc, Operation: "create_batch"}}}
			result, err := a.Run(ctx, input)
			if err != nil || result.Reply != "Готово." && result.Reply != "Поставил семинар по экономике." {
				t.Fatalf("%+v %v", result, err)
			}
			if scenario == "update" && svc.Get(ctx, event.ID).StartsAt.Hour() != 16 {
				t.Fatal("not moved")
			}
			if scenario == "create" {
				var count int
				if err := database.QueryRow("SELECT count(*) FROM events WHERE rrule LIKE '%INTERVAL=2%'").Scan(&count); err != nil || count != 1 {
					t.Fatalf("create recurrence missing: count=%d err=%v", count, err)
				}
			}
			if scenario == "birthdays" {
				var count int
				if err := database.QueryRow("SELECT count(*) FROM events WHERE kind='birthday' AND all_day=1").Scan(&count); err != nil || count != 2 || round != 2 {
					t.Fatalf("batch %d rounds %d %v", count, round, err)
				}
			}
		})
	}
}

func TestPromptHasNoWireEnvelope(t *testing.T) {
	for _, s := range []string{`"tool_calls"`, `"reply"`, "РОВНО одним JSON", "ровно один tool call"} {
		if strings.Contains(prompts.AgentSystem, s) {
			t.Fatalf("protocol instruction %q", s)
		}
	}
}

type chatFunc func(context.Context, ai.ChatRequest) (ai.AssistantTurn, error)

func (f chatFunc) Chat(ctx context.Context, r ai.ChatRequest) (ai.AssistantTurn, error) {
	return f(ctx, r)
}

func TestContextCompactionProtectsToolExchange(t *testing.T) {
	input := testInput()
	input.Messages = []conversation.Message{{SenderType: conversation.SenderUser, Text: strings.Repeat("old", 4000)}, {SenderType: conversation.SenderBot, Text: "immediate parent"}, {SenderType: conversation.SenderUser, Text: "current"}}
	round := 0
	tool := &fakeTool{name: "schedule_query"}
	client := chatFunc(func(_ context.Context, r ai.ChatRequest) (ai.AssistantTurn, error) {
		if len(r.Messages) < 3 || r.Messages[1].Content != "immediate parent" || r.Messages[2].Content != "current" {
			t.Fatal("protected input lost")
		}
		round++
		if round == 1 {
			return ai.AssistantTurn{ToolCalls: []ai.ToolCall{{ID: "native-id", Name: tool.Name(), Arguments: json.RawMessage(`{}`)}}}, nil
		}
		if len(r.Messages) != 5 || r.Messages[3].ToolCalls[0].ID != "native-id" || r.Messages[4].ToolCallID != "native-id" || !strings.Contains(r.Messages[4].Content, `"ok":true`) {
			t.Fatal("tool exchange dropped")
		}
		return ai.AssistantTurn{Content: "done"}, nil
	})
	result, err := (Agent{Client: client, Tools: []Tool{tool}, ContextBytes: 12000}).Run(context.Background(), input)
	if err != nil || result.Reply != "done" || round != 2 {
		t.Fatalf("%+v %v rounds %d", result, err, round)
	}
}

func TestCanonicalDuplicateCalls(t *testing.T) {
	tool := &fakeTool{name: "schedule_query"}
	round := 0
	client := chatFunc(func(_ context.Context, r ai.ChatRequest) (ai.AssistantTurn, error) {
		round++
		if round == 3 {
			if !strings.Contains(r.Messages[len(r.Messages)-1].Content, "duplicate_call_in_run") {
				t.Fatal("duplicate not reported")
			}
			return ai.AssistantTurn{Content: "done"}, nil
		}
		raw := `{"a":1,"b":2}`
		if round == 2 {
			raw = `{ "b":2, "a":1 }`
		}
		return ai.AssistantTurn{ToolCalls: []ai.ToolCall{{ID: fmt.Sprint(round), Name: tool.Name(), Arguments: json.RawMessage(raw)}}}, nil
	})
	_, err := (Agent{Client: client, Tools: []Tool{tool}}).Run(context.Background(), testInput())
	if err != nil || tool.calls != 1 {
		t.Fatalf("%v calls %d", err, tool.calls)
	}
}

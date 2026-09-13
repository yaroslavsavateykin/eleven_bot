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
)

// The provider fixture checks orchestration, not the linguistic accuracy of a
// live model. Dates missing from human context must be clarified, not invented.
func TestScreenshotCompoundScheduleRequest(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "compound.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	svc := schedule.Service{DB: d, GroupID: 1, TZ: time.UTC}
	start := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	end := start.Add(205 * time.Minute)
	event, _, err := svc.Create(ctx, schedule.Event{Kind: "lesson", Title: "Практикум по химической технологии для военников", StartsAt: start, EndsAt: &end, Timezone: "UTC"}, "test", "", "")
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
			w.WriteHeader(400)
			return
		}
		if round > 0 {
			last := req.Messages[len(req.Messages)-1]
			if last.Role != "tool" || !strings.Contains(last.Content, `"ok":true`) {
				t.Errorf("tool result: %+v", last)
			}
		}
		call := func(id, name, args string) map[string]any {
			return map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": args}}
		}
		var calls []map[string]any
		switch round {
		case 0:
			calls = []map[string]any{call("find", "schedule_query", `{"query":"Практикум по химической технологии для военников","from":"2026-09-26T00:00:00Z","to":"2026-09-27T00:00:00Z"}`)}
		case 1:
			calls = []map[string]any{
				call("weekly", "schedule_create", `{"event":{"kind":"lesson","title":"Практикум по химической технологии для военников","starts_at":"2026-10-03T09:00:00Z","ends_at":"2026-10-03T12:25:00Z","timezone":"UTC","recurrence":{"frequency":"weekly","weekdays":["SA"]}}}`),
				call("replace", "schedule_update", fmt.Sprintf(`{"target_id":%d,"changes":{"title":"Контрольная работа по химической технологии для военников"}}`, event.ID)),
			}
		default:
			fmt.Fprint(w, `{"choices":[{"message":{"content":"Добавил еженедельный практикум по субботам и заменил занятие 26 сентября контрольной."},"finish_reason":"stop"}]}`)
			round++
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": nil, "tool_calls": calls}, "finish_reason": "tool_calls"}}})
		round++
	}))
	defer server.Close()
	a := Agent{Client: ai.Service{BaseURL: server.URL, Key: "test", Model: "fixture"}, Tools: []Tool{ScheduleQueryTool{Schedule: svc}}, AdminTools: []Tool{ScheduleMutationTool{Schedule: svc, Operation: "create"}, ScheduleMutationTool{Schedule: svc, Operation: "update"}}}
	input := Conversation{RunID: "telegram:1:compound", Mode: ModeAdminPrivate, Now: time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC), Timezone: "UTC", Messages: []conversation.Message{{SenderType: conversation.SenderUser, Text: "26.09 вместо практикума контрольная"}, {SenderType: conversation.SenderBot, Text: "Нашёл практикум по химической технологии для военников."}, {SenderType: conversation.SenderUser, Text: "Тогда ко всему прочему для военников в субботу 1-2 парой добавь еженедельно практикум по химической технологии и в тот день который я указал вместо практикума контрольная"}}}
	result, err := a.Run(ctx, input)
	if err != nil || round != 3 || !strings.Contains(result.Reply, "контрольной") {
		t.Fatalf("%+v %v rounds=%d", result, err, round)
	}
	if !strings.Contains(svc.Get(ctx, event.ID).Title, "Контрольная") {
		t.Fatal("target not updated")
	}
	var recurring int
	if err := d.QueryRow("SELECT count(*) FROM events WHERE rrule LIKE '%FREQ=WEEKLY%'").Scan(&recurring); err != nil || recurring != 1 {
		t.Fatalf("recurring=%d %v", recurring, err)
	}
}

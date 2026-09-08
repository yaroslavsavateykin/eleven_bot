package ai

import (
	"context"
	"encoding/json"
	"group411/internal/schedule"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOperationValidation(t *testing.T) {
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		patch map[string]any
		bad   bool
	}{
		{"inferred", nil, false}, {"explicit", map[string]any{"duration_inferred": false, "end_local": "2026-09-09T09:00:00"}, false},
		{"duration mismatch", map[string]any{"end_local": "2026-09-09T10:00:00"}, true},
		{"ambiguous", map[string]any{"operation": "cancel", "target_ids": []int{1, 2}}, true},
		{"unknown target", map[string]any{"operation": "cancel", "target_ids": []int{99}}, true},
		{"cancel", map[string]any{"operation": "cancel", "target_ids": []int{1}}, false},
		{"update", map[string]any{"operation": "update", "target_ids": []int{1}}, false},
		{"low confidence", map[string]any{"confidence": 0.5}, true}, {"bad confidence", map[string]any{"confidence": 1.1}, true},
		{"timezone", map[string]any{"timezone": "Europe/Moscow"}, true}, {"bad kind", map[string]any{"kind": "evil"}, true},
		{"bad category", map[string]any{"category": "evil"}, true}, {"too long", map[string]any{"duration_minutes": 1441}, true},
		{"unknown field", map[string]any{"injection": "ignore"}, true}, {"past", map[string]any{"start_local": "2020-01-01T08:00:00"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := map[string]any{"operation": "create", "target_ids": []int{}, "kind": "event", "category": "event", "title": "Meeting", "start_local": "2026-09-09T08:00:00", "timezone": "UTC", "duration_minutes": 60, "duration_inferred": true, "confidence": 0.99}
			for k, x := range tc.patch {
				v[k] = x
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				json.NewDecoder(r.Body).Decode(&request)
				if request["response_format"] == nil {
					t.Error("missing JSON mode")
				}
				b, _ := json.Marshal(map[string]any{"operations": []any{v}, "question": ""})
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message{Content: string(b)}}}})
			}))
			defer server.Close()
			s := Service{BaseURL: server.URL, Key: "test", Model: "test", Client: server.Client()}
			p, err := s.ParseOperation(context.Background(), "request", now, time.UTC, []schedule.Event{{ID: 1, GroupID: 1, Title: "old"}})
			if (err != nil) != tc.bad {
				t.Fatalf("proposal %v error %v", p, err)
			}
			if err == nil && p.Operation != "cancel" && p.Event.EndsAt.Sub(p.Event.StartsAt) != time.Hour {
				t.Fatal("wrong duration")
			}
			if _, err = s.ParseOperation(context.Background(), strings.Repeat("x", 3001), now, time.UTC, nil); err == nil {
				t.Fatal("long prompt accepted")
			}
		})
	}
}

func TestParseOperationsDefaultsMissingDurationAndParsesMultipleCreates(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(request.Messages[0].Content, "Не спрашивай место или длительность") || !strings.Contains(request.Messages[0].Content, "Создавай все независимо однозначные события") {
			t.Fatal("missing create-with-defaults instruction")
		}
		response := `{"operations":[{"operation":"create","target_ids":[],"kind":"lesson","category":"lesson","title":"Английский","location":null,"start_local":"2026-09-09T14:00:00","end_local":null,"timezone":"UTC","all_day":false,"rrule":null,"tags":[],"duration_minutes":0,"confidence":0.99},{"operation":"create","target_ids":[],"kind":"event","category":"event","title":"Встреча","location":null,"start_local":"2026-09-10T10:00:00","end_local":null,"timezone":"UTC","all_day":false,"rrule":null,"tags":[],"duration_minutes":0,"confidence":0.99}],"question":""}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message{Content: response}}}})
	}))
	defer server.Close()
	s := Service{BaseURL: server.URL, Key: "test", Model: "test", Client: server.Client()}
	proposals, question, err := s.ParseOperations(context.Background(), "завтра английский в 14:00, послезавтра встреча в 10:00", now, time.UTC, nil)
	if err != nil || question != "" || len(proposals) != 2 {
		t.Fatalf("proposals=%#v question=%q err=%v", proposals, question, err)
	}
	if got := proposals[0].Event.EndsAt.Sub(proposals[0].Event.StartsAt); got != 95*time.Minute || !proposals[0].Inferred {
		t.Fatalf("lesson default=%v inferred=%v", got, proposals[0].Inferred)
	}
	if got := proposals[1].Event.EndsAt.Sub(proposals[1].Event.StartsAt); got != time.Hour || !proposals[1].Inferred {
		t.Fatalf("event default=%v inferred=%v", got, proposals[1].Inferred)
	}
}

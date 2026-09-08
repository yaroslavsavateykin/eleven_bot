package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseCategory(t *testing.T) {
	for _, value := range []string{"exam", "", "invalid"} {
		t.Run(value, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Messages []message `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if len(request.Messages) == 0 || !strings.Contains(request.Messages[0].Content, "category") {
					t.Error("missing category prompt")
				}
				content, _ := json.Marshal(map[string]any{"operation": "create", "kind": "lesson", "category": value, "title": "КР по химии", "start_local": "2026-09-09T08:00:00", "timezone": "UTC", "confidence": 0.99})
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message{Content: string(content)}}}})
			}))
			defer server.Close()
			s := Service{BaseURL: server.URL, Key: "test", Model: "test", Client: server.Client()}
			e, _, err := s.ParseEvent(context.Background(), "test", "test", time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC), time.UTC)
			if value == "invalid" {
				if err == nil {
					t.Fatal("invalid category accepted")
				}
				return
			}
			want := value
			if want == "" {
				want = "test"
			}
			if err != nil || e.Category != want || e.Kind != "lesson" {
				t.Fatalf("parse category: %s %v", e.Category, err)
			}
		})
	}
}

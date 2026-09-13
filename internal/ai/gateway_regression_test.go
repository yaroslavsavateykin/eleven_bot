package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentRouteAndNonStreamingRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		if p["model"] != "cx/stable" || p["stream"] != false {
			t.Errorf("wrong route/stream: %v %v", p["model"], p["stream"])
		}
		requests++
		fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	s := Service{BaseURL: server.URL, Key: "test", Model: "rotating-combo", AgentModel: "cx/stable"}
	for i := 0; i < 2; i++ {
		if _, err := s.Chat(context.Background(), ChatRequest{RunID: "same-run", Round: i}); err != nil {
			t.Fatal(err)
		}
	}
	if requests != 2 {
		t.Fatal(requests)
	}
}
func TestProtocolReasons(t *testing.T) {
	for _, tc := range []struct{ body, reason string }{
		{`{"choices":[{"message":{},"finish_reason":"stop"}]}`, "empty_assistant_turn"},
		{`{"error":{"message":"private provider detail"}}`, "upstream_error_envelope"},
		{`{"choices":[{"message":{"tool_calls":[{"id":"c1","type":"function","function":{"name":"lookup","arguments":"{"}}]},"finish_reason":"length"}]}`, "completion_truncated"},
	} {
		_, err := decodeAssistant([]byte(tc.body))
		var e *Error
		if !errors.As(err, &e) || e.Reason != tc.reason {
			t.Fatalf("expected %s, got %v", tc.reason, err)
		}
	}
}

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatRetriesAndText(t *testing.T) {
	for _, status := range []int{200, 429, 500, 400} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				if attempts == 1 && status != 200 {
					w.WriteHeader(status)
					return
				}
				fmt.Fprint(w, `{"choices":[{"message":{"content":"Ответ"},"finish_reason":"stop"}]}`)
			}))
			defer server.Close()
			turn, err := (Service{BaseURL: server.URL, Key: "test", Model: "fixture"}).Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "вопрос"}}})
			if status == 400 {
				if err == nil || attempts != 1 {
					t.Fatal("4xx retried")
				}
				return
			}
			expected := 2
			if status == 200 {
				expected = 1
			}
			if err != nil || turn.Content != "Ответ" || attempts != expected {
				t.Fatalf("%+v %v %d", turn, err, attempts)
			}
		})
	}
}

func TestTranscribeUsesDedicatedSTTConfiguration(t *testing.T) {
	var gotModel, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/transcriptions" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		gotModel = r.FormValue("model")
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"text":"распознано"}`)
	}))
	defer server.Close()

	text, err := (Service{BaseURL: "http://chat.invalid/v1", Key: "chat-key", STTBaseURL: server.URL, STTKey: "stt-key", STTModel: "gpt-4o-mini-transcribe"}).Transcribe(context.Background(), []byte("audio"), "voice.ogg", "audio/ogg")
	if err != nil || text != "распознано" || gotModel != "gpt-4o-mini-transcribe" || gotAuth != "Bearer stt-key" {
		t.Fatalf("text=%q model=%q auth=%q err=%v", text, gotModel, gotAuth, err)
	}
}

func TestDecodeNativeSSE(t *testing.T) {
	raw := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"schedule_query","arguments":"{\"query\":"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"экономика\"}"}}]},"finish_reason":"tool_calls"}]}

data: [DONE]
`
	turn, err := decodeAssistant([]byte(raw))
	if err != nil || len(turn.ToolCalls) != 1 || turn.ToolCalls[0].ID != "call_a" || string(turn.ToolCalls[0].Arguments) != `{"query":"экономика"}` {
		t.Fatalf("%+v %v", turn, err)
	}
}
func TestEmptyAssistantRejected(t *testing.T) {
	for _, raw := range []string{`{"choices":[{"message":{"content":null}}]}`, `{"choices":[]}`, `{"choices":[{"message":{"content":"   "}}]}`} {
		if _, err := decodeAssistant([]byte(raw)); err == nil {
			t.Fatal("accepted", raw)
		}
	}
}

func TestTruncatedTextStillReturned(t *testing.T) {
	turn, err := decodeAssistant([]byte(`{"choices":[{"message":{"content":"partial answer"},"finish_reason":"length"}]}`))
	if err != nil || turn.Content != "partial answer" {
		t.Fatalf("%+v %v", turn, err)
	}
}

func TestDecodeJSONWithTrailingDone(t *testing.T) {
	// 9router appends an SSE "[DONE]" terminator directly after a non-streaming
	// JSON body, with no separating newline.
	raw := `{"choices":[{"message":{"content":"Ответ","reasoning_content":"thinking"},"finish_reason":"stop"}]}data: [DONE]
`
	turn, err := decodeAssistant([]byte(raw))
	if err != nil || turn.Content != "Ответ" {
		t.Fatalf("%+v %v", turn, err)
	}
}

func TestDecodeJSONWithTrailingDoneAndToolCall(t *testing.T) {
	raw := `{"choices":[{"message":{"content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"schedule_query","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}data: [DONE]
`
	turn, err := decodeAssistant([]byte(raw))
	if err != nil || len(turn.ToolCalls) != 1 || turn.ToolCalls[0].ID != "call_1" {
		t.Fatalf("%+v %v", turn, err)
	}
}

func TestExplicitLegacyProtocol(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		if p["tools"] != nil || p["response_format"] != nil {
			t.Error("legacy sent native tools")
		}
		requests++
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"reply\":\"\",\"tool_calls\":[{\"name\":\"schedule_query\",\"arguments\":{}}]}"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	turn, err := (Service{BaseURL: server.URL, Key: "test", Model: "fixture", ToolMode: "legacy_json"}).Chat(context.Background(), ChatRequest{Round: 2})
	if err != nil || len(turn.ToolCalls) != 1 || turn.ToolCalls[0].ID != "legacy_2_0" || requests != 1 {
		t.Fatalf("%+v %v", turn, err)
	}
}

func TestNativeNeverFallsBack(t *testing.T) {
	for _, status := range []int{400, 500, 200} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var p map[string]any
				json.NewDecoder(r.Body).Decode(&p)
				if p["tools"] == nil {
					t.Error("native fallback")
				}
				requests++
				w.WriteHeader(status)
				if status == 200 {
					fmt.Fprint(w, `{"choices":[{"message":{}}]}`)
				}
			}))
			defer server.Close()
			_, err := (Service{BaseURL: server.URL, Key: "test", Model: "fixture"}).Chat(context.Background(), ChatRequest{Tools: []ToolDefinition{{Name: "read", Parameters: json.RawMessage(`{"type":"object"}`)}}})
			expected := 1
			if status == 500 {
				expected = 3
			}
			if err == nil || requests != expected {
				t.Fatalf("%v %d", err, requests)
			}
		})
	}
}

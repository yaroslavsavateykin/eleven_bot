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

func TestParseOperationsUsesDialogueAndRussianQuestion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if !strings.Contains(request.Messages[1].Content, "Исходный запрос") || !strings.Contains(request.Messages[1].Content, "Вопрос бота") || !strings.Contains(request.Messages[1].Content, "Уточнение") {
			t.Error("full dialogue missing")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message{Content: `{"operations":[],"question":"Which event?"}`}}}})
	}))
	defer server.Close()
	s := Service{BaseURL: server.URL, Key: "test", Model: "test", Client: server.Client()}
	_, question, err := s.ParseOperations(context.Background(), "Исходный запрос пользователя: перенеси пару\nВопрос бота: какую?\nУточнение пользователя: вторую", time.Now(), time.UTC, nil)
	if err != nil || question != "Уточните дату, время или нужное событие." {
		t.Fatalf("question %q, err %v", question, err)
	}
}

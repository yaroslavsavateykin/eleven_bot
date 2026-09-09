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

func TestTranscribeAndDescribeImageUseDedicatedModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/audio/transcriptions":
			if err := r.ParseMultipartForm(1 << 20); err != nil || r.Form.Get("model") != "stt" {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"text":"/event завтра в 12"}`))
		case "/chat/completions":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["model"] != "vision" {
				t.Fatalf("vision model: %#v", body["model"])
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"конспект по химии"}}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	s := Service{BaseURL: server.URL, Key: "test", VisionModel: "vision", STTModel: "stt", Client: server.Client()}
	text, err := s.Transcribe(context.Background(), []byte("audio"), "voice.ogg", "audio/ogg")
	if err != nil || text != "/event завтра в 12" {
		t.Fatalf("transcribe: %q %v", text, err)
	}
	note, err := s.DescribeImage(context.Background(), []byte("image"), "image/jpeg")
	if err != nil || note != "конспект по химии" {
		t.Fatalf("describe: %q %v", note, err)
	}
}

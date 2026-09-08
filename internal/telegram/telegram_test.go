package telegram

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

	"github.com/go-telegram/bot"
	"group411/internal/ai"
	"group411/internal/db"
	"group411/internal/schedule"
)

func TestAuthorizationAndCommands(t *testing.T) {
	s := Service{ChatID: -100, AdminID: 123}
	for _, tc := range []struct {
		chat, user    int64
		private, want bool
	}{{-100, 4, false, true}, {-200, 123, false, false}, {123, 123, true, true}, {456, 456, true, false}, {123, 456, true, false}, {-100, 123, true, false}} {
		if s.allowed(tc.chat, tc.user, tc.private) != tc.want {
			t.Fatal(tc)
		}
	}
	s.AdminID = 0
	if s.allowed(123, 123, true) {
		t.Fatal("unset admin accepted")
	}
	for _, tc := range []struct{ text, cmd, arg string }{{"/event@bot перенеси #2", "/event", "перенеси #2"}, {"/event", "/event", ""}, {"", "", ""}} {
		cmd, arg := command(tc.text)
		if cmd != tc.cmd || arg != tc.arg {
			t.Fatal(tc, cmd, arg)
		}
	}
}

func TestEventSummaryIsCompact(t *testing.T) {
	tz := time.FixedZone("MSK", 3*3600)
	s := Service{Schedule: schedule.Service{TZ: tz}}
	start := time.Now().In(tz).AddDate(0, 0, 1).Truncate(time.Minute)
	end := start.Add(95 * time.Minute)
	e := schedule.Event{Title: "Английский", StartsAt: start, EndsAt: &end, Timezone: tz.String(), Warnings: []schedule.Conflict{{Event: schedule.Event{Title: "Физика"}, StartsAt: start}}}
	got := s.eventSummary(e, schedule.Proposal{Operation: "create"})
	want := "Добавил: Английский — завтра, " + start.Format("15:04") + "–" + end.Format("15:04") + ".\nПересекается с: Физика, " + start.Format("02.01 15:04")
	if got != want {
		t.Fatalf("summary=%q want=%q", got, want)
	}
	if got := s.eventSummary(e, schedule.Proposal{Operation: "update"}); !strings.HasPrefix(got, "Обновил: ") {
		t.Fatalf("update summary=%q", got)
	}
	if got := s.eventSummary(e, schedule.Proposal{Operation: "cancel"}); got != "Отменил: Английский." {
		t.Fatalf("cancel summary=%q", got)
	}
}

func TestEventSuccessSendsOneStandaloneCompactMessage(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().AddDate(0, 0, 1).Truncate(time.Minute)
	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := fmt.Sprintf(`{"operations":[{"operation":"create","target_ids":[],"kind":"lesson","category":"lesson","title":"Английский","location":null,"start_local":"%s","end_local":null,"timezone":"UTC","all_day":false,"rrule":null,"tags":[],"duration_minutes":0,"confidence":0.99}],"question":""}`, start.Format("2006-01-02T15:04:05"))
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": response}}}})
	}))
	defer aiServer.Close()
	var sent []map[string]string
	telegramServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			if err := r.ParseMultipartForm(1024); err != nil {
				t.Error(err)
			}
			sent = append(sent, map[string]string{"text": r.Form.Get("text"), "reply_parameters": r.Form.Get("reply_parameters")})
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":77,"date":0,"chat":{"id":-1,"type":"group"}}}`))
	}))
	defer telegramServer.Close()
	b, err := bot.New("test", bot.WithServerURL(telegramServer.URL))
	if err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d, GroupID: 1, Schedule: schedule.Service{DB: d, GroupID: 1, TZ: time.UTC}, AI: ai.Service{BaseURL: aiServer.URL, Key: "test", Model: "test", Client: aiServer.Client()}}
	s.processEvent(ctx, b, -1, 10, 10, 1, "завтра пара по английскому в 14:00", "завтра пара по английскому в 14:00", "")
	if len(sent) != 1 {
		t.Fatalf("send count=%d payloads=%#v", len(sent), sent)
	}
	if got, want := sent[0]["text"], "Добавил: Английский — завтра, "+start.Format("15:04")+"–"+start.Add(95*time.Minute).Format("15:04")+"."; got != want {
		t.Fatalf("text=%q want=%q", got, want)
	}
	if sent[0]["reply_parameters"] != "" {
		t.Fatalf("successful event unexpectedly replies to command: %#v", sent[0])
	}
}

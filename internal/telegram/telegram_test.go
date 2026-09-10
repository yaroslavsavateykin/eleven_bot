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
	"github.com/go-telegram/bot/models"
	"group411/internal/agent"
	"group411/internal/ai"
	"group411/internal/conversation"
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
	if cmd, _ := (Service{BotUsername: "eleven_bot"}).command("/event@other_bot завтра"); cmd != "" {
		t.Fatal("foreign command accepted")
	}
	if cmd, _ := (Service{BotUsername: "eleven_bot"}).command("/event@other_bot@bad завтра"); cmd != "" {
		t.Fatal("malformed command accepted")
	}
}

func TestUnknownGroupCommandProducesReply(t *testing.T) {
	if cmd, _ := (Service{BotUsername: "configured_bot"}).command("/chatid"); cmd != "/chatid" {
		t.Fatalf("command=%q", cmd)
	}
}

func TestRoutingTriggers(t *testing.T) {
	s := Service{BotUsername: "configured_bot"}
	if !s.mentioned("@configured_bot когда пара?") || !s.mentioned("привет, @CONFIGURED_bot!") {
		t.Fatal("mention was not recognized")
	}
	if s.mentioned("@another_bot вопрос") || s.mentioned("обычное сообщение") {
		t.Fatal("ordinary message routed as mention")
	}
}

func TestDirectReplyToConfiguredBotUsesTelegramIdentity(t *testing.T) {
	ctx := context.Background()
	s := Service{BotUserID: 42, BotUsername: "configured_bot"}
	message := &models.Message{Chat: models.Chat{ID: -1}, ReplyToMessage: &models.Message{ID: 10, From: &models.User{ID: 42, IsBot: true, Username: "configured_bot"}}}
	if !s.directReplyToBot(ctx, message) {
		t.Fatal("reply to configured bot was not activated")
	}
	message.ReplyToMessage.From = &models.User{ID: 99, IsBot: true, Username: "other_bot"}
	if s.directReplyToBot(ctx, message) {
		t.Fatal("reply to another bot was activated")
	}
}

func TestPointerHandlerSeesTelegramIdentitySetAfterRegistration(t *testing.T) {
	s := &Service{}
	handler := s.handle
	s.BotUserID = 42 // Start sets this after registering the default handler.
	update := &models.Update{Message: &models.Message{From: &models.User{ID: 1}, Chat: models.Chat{ID: -1}, Text: "обычное сообщение"}}
	// The handler must be a pointer method value, not a stale Service copy.
	if handler == nil || s.BotUserID != 42 || update.Message == nil {
		t.Fatal("handler setup lost configured Telegram identity")
	}
}

func TestThinkingResponseIsEditedAndPersisted(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "thinking.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	var calls []string
	telegramServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":77,"date":0,"chat":{"id":-1,"type":"group"}}}`))
	}))
	defer telegramServer.Close()
	b, err := bot.New("test", bot.WithServerURL(telegramServer.URL))
	if err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d, GroupID: 1, Conversation: conversation.Service{DB: d}}
	ctx = s.withThinking(ctx, b, -1, 10)
	if _, err := s.send(ctx, b, -1, "Готово."); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || !strings.HasSuffix(calls[1], "/sendMessage") || !strings.HasSuffix(calls[2], "/editMessageText") {
		t.Fatalf("telegram calls=%v", calls)
	}
	var text string
	if err := d.QueryRow(`SELECT text FROM messages WHERE telegram_chat_id=-1 AND telegram_message_id=77`).Scan(&text); err != nil || text != "Готово." {
		t.Fatalf("persisted text=%q err=%v", text, err)
	}
}

func TestThinkingResponseCanReplyInPrivateChat(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "private-thinking.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	telegramServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":77,"date":0,"chat":{"id":1,"type":"private"}}}`))
	}))
	defer telegramServer.Close()
	b, err := bot.New("test", bot.WithServerURL(telegramServer.URL))
	if err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d, GroupID: 1, Conversation: conversation.Service{DB: d}}
	ctx = s.withThinking(ctx, b, 1, 10)
	if _, err := s.send(ctx, b, 1, "Готово."); err != nil {
		t.Fatal(err)
	}
	var text string
	if err := d.QueryRow(`SELECT text FROM messages WHERE telegram_chat_id=1 AND telegram_message_id=77`).Scan(&text); err != nil || text != "Готово." {
		t.Fatalf("persisted text=%q err=%v", text, err)
	}
}

func TestPrivateNaturalLanguageEventRouting(t *testing.T) {
	for _, text := range []string{
		"Запиши завтра в расписание 5 парой физическую химию",
		"Добавь завтра на первой паре квантовую",
		"Удали пару по математике в пятницу",
		"Перенеси органику на четвёртую пару",
		"Каждый вторник второй парой поставь семинар по физхимии",
	} {
		if !isEventRequest(text) {
			t.Fatalf("event request was routed to general agent: %q", text)
		}
	}
}

func TestAdminPrivatePasteUsesScheduleImportWithoutGeneralAgent(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "admin-import.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().AddDate(0, 0, 1).Truncate(time.Minute)
	parseCalls := 0
	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parseCalls++
		response := fmt.Sprintf(`{"operations":[{"operation":"create","target_ids":[],"kind":"lesson","category":"lesson","title":"ВМС","location":null,"start_local":"%s","end_local":null,"timezone":"UTC","all_day":false,"rrule":null,"week_parity":"","tags":[],"duration_minutes":95,"confidence":0.99}],"skipped":[{"source":"Инструментальные методы","reason":"нет точного слота"}]}`, start.Format("2006-01-02T15:04:05"))
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": response}}}})
	}))
	defer aiServer.Close()
	var sent []string
	telegramServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			_ = r.ParseMultipartForm(1024)
			sent = append(sent, r.Form.Get("text"))
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":77,"date":0,"chat":{"id":1,"type":"private"}}}`))
	}))
	defer telegramServer.Close()
	b, err := bot.New("test", bot.WithServerURL(telegramServer.URL))
	if err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d, GroupID: 1, Schedule: schedule.Service{DB: d, GroupID: 1, TZ: time.UTC}, AI: ai.Service{BaseURL: aiServer.URL, Key: "test", Model: "test", Client: aiServer.Client()}}
	s.processPrivateEvent(ctx, b, &models.Message{ID: 44, Chat: models.Chat{ID: 1}, Text: "Среда: ВМС\n" + strings.Repeat("материалы ", 600)}, conversation.Message{}, 1)
	if parseCalls != 1 || len(sent) != 1 || !strings.Contains(sent[0], "Добавил") || !strings.Contains(sent[0], "Пропустил") {
		t.Fatalf("parse=%d sent=%q", parseCalls, sent)
	}
	events, err := s.Schedule.Candidates(ctx)
	if err != nil || len(events) != 1 {
		t.Fatal("parsed event was not applied")
	}
}

func TestAdminPrivateRecurringImportUsesFiniteHorizonAndCompactSummary(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "admin-recurrence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Minute)
	daysUntilFriday := (int(time.Friday) - int(now.Weekday()) + 7) % 7
	if daysUntilFriday == 0 {
		daysUntilFriday = 7
	}
	start := time.Date(now.Year(), now.Month(), now.Day()+daysUntilFriday, 9, 0, 0, 0, time.UTC)
	ref := start.AddDate(0, 0, -int((start.Weekday()+6)%7))
	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := fmt.Sprintf(`{"operations":[{"operation":"create","target_ids":[],"kind":"lesson","category":"lesson","title":"География","location":null,"start_local":"%s","end_local":"%s","timezone":"UTC","all_day":false,"rrule":null,"recurrence":{"frequency":"weekly","interval":2,"weekday":["FR"],"count":0,"until_local":""},"week_parity":"even","source_index":1,"source_text":"по чётным неделям в пятницу первой парой география","tags":[],"duration_minutes":95,"confidence":0.99}],"skipped":[]}`, start.Format("2006-01-02T15:04:05"), start.Add(95*time.Minute).Format("2006-01-02T15:04:05"))
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": response}}}})
	}))
	defer aiServer.Close()
	var sent []string
	telegramServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			_ = r.ParseMultipartForm(1024)
			sent = append(sent, r.Form.Get("text"))
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":77,"date":0,"chat":{"id":1,"type":"private"}}}`))
	}))
	defer telegramServer.Close()
	b, err := bot.New("test", bot.WithServerURL(telegramServer.URL))
	if err != nil {
		t.Fatal(err)
	}
	scheduleService := schedule.Service{DB: d, GroupID: 1, TZ: time.UTC, WeekParity: schedule.WeekParityConfig{ReferenceWeekStart: ref, ReferenceParity: "even"}}
	s := Service{DB: d, GroupID: 1, Schedule: scheduleService, AI: ai.Service{BaseURL: aiServer.URL, Key: "test", Model: "test", Client: aiServer.Client(), WeekParity: scheduleService.WeekParity}}
	s.processPrivateEvent(ctx, b, &models.Message{ID: 45, Chat: models.Chat{ID: 1}, Text: "по чётным неделям в пятницу первой парой география"}, conversation.Message{}, 1)
	events, err := scheduleService.Candidates(ctx)
	if err != nil || len(events) != 1 || events[0].RRule == nil || !strings.Contains(*events[0].RRule, "UNTIL=") {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	if len(sent) != 1 || !strings.Contains(sent[0], "по чётным неделям") || !strings.Contains(sent[0], "на ближайшие 16 недель") || strings.Contains(sent[0], "до ") || strings.Contains(sent[0], "Пропустил") {
		t.Fatalf("summary=%q", sent)
	}
}

func TestLatestGitHubTagRejectsInvalidRepository(t *testing.T) {
	if _, err := latestGitHubTag(context.Background(), "not-a-repository"); err == nil {
		t.Fatal("invalid repository accepted")
	}
}

func TestFilterEventCandidatesKeepsOnlyRequestedSubject(t *testing.T) {
	events := []schedule.Event{{ID: 1, Title: "КР по органике"}, {ID: 2, Title: "КР по математике"}, {ID: 3, Title: "Английский"}, {ID: 4, Title: "Семинар по математике"}}
	got := filterEventCandidates(events, "удали все пары по математике")
	if len(got) != 2 || got[0].ID != 2 || got[1].ID != 4 {
		t.Fatalf("filtered=%#v", got)
	}
}

func TestEventReplyRootRoutesToEventPipeline(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "continuation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`INSERT INTO users(id,telegram_user_id,created_at,updated_at) VALUES(1,1,'','')`); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().AddDate(0, 0, 1).Truncate(time.Minute)
	end := start.Add(time.Hour)
	if _, err = d.Exec(`INSERT INTO events(group_id,kind,category,title,starts_at,ends_at,timezone,all_day,status,source_type,dedupe_key,created_at,updated_at) VALUES(1,'event','event','Консультация',?,?, 'UTC',0,'active','test','x','','')`, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	aiCalls := 0
	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aiCalls++
		response := `{"operations":[{"operation":"cancel","target_ids":[1],"kind":"","category":"","title":"","start_local":"","timezone":"","all_day":false,"duration_minutes":0,"confidence":0.99}],"question":""}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": response}}}})
	}))
	defer aiServer.Close()
	telegramServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":77,"date":0,"chat":{"id":-1,"type":"group"}}}`))
	}))
	defer telegramServer.Close()
	b, err := bot.New("test", bot.WithServerURL(telegramServer.URL))
	if err != nil {
		t.Fatal(err)
	}
	conversations := conversation.Service{DB: d, MaxDepth: 16, MaxChars: 3000}
	root, _, err := conversations.Ingest(ctx, conversation.Incoming{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 10, UserID: 1, Kind: "text", Text: "/event перенеси консультацию", SentAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	botReply, _, err := conversations.StoreBot(ctx, conversation.BotMessage{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 11, Kind: "text", Text: "1. Консультация", ReplyToTelegramMessageID: &root.TelegramMessageID, SentAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	current, _, err := conversations.Ingest(ctx, conversation.Incoming{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 12, UserID: 1, Kind: "text", Text: "#1", ReplyToTelegramMessageID: &botReply.TelegramMessageID, SentAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d, GroupID: 1, Schedule: schedule.Service{DB: d, GroupID: 1, TZ: time.UTC}, AI: ai.Service{BaseURL: aiServer.URL, Key: "test", Model: "test", Client: aiServer.Client()}, Conversation: conversations}
	s.reply(ctx, b, &models.Message{ID: 12, Chat: models.Chat{ID: -1}, ReplyToMessage: &models.Message{ID: 11}}, current, 1)
	if aiCalls != 1 {
		t.Fatalf("event parser calls=%d", aiCalls)
	}
	if status := s.Schedule.Get(ctx, 1).Status; status != "cancelled" {
		t.Fatalf("status=%s", status)
	}
}

type replyAgentClient struct{ calls int }

func (c *replyAgentClient) Complete(_ context.Context, _ string, prompt string) (string, error) {
	c.calls++
	if !strings.Contains(prompt, "assistant: roast") || !strings.Contains(prompt, "user: сам такой") {
		return "", fmt.Errorf("reply context omitted bot or user message: %q", prompt)
	}
	return `{"reply":"Исчерпывающе.","tool_calls":[]}`, nil
}

func TestDirectReplyToBotFallsBackWhenParentIsMissing(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "missing-parent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','',''); INSERT INTO users(id,telegram_user_id,created_at,updated_at) VALUES(1,1,'','')`); err != nil {
		t.Fatal(err)
	}
	telegramServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":77,"date":0,"chat":{"id":-1,"type":"group"}}}`))
	}))
	defer telegramServer.Close()
	b, err := bot.New("test", bot.WithServerURL(telegramServer.URL))
	if err != nil {
		t.Fatal(err)
	}
	conversations := conversation.Service{DB: d, MaxDepth: 16, MaxChars: 3000}
	parentTelegramID := 11
	current, _, err := conversations.Ingest(ctx, conversation.Incoming{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 12, UserID: 1, Kind: "text", Text: "сам такой", ReplyToTelegramMessageID: &parentTelegramID, SentAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	client := &replyAgentClient{}
	s := Service{DB: d, GroupID: 1, BotUserID: 99, Schedule: schedule.Service{DB: d, GroupID: 1, TZ: time.UTC}, Conversation: conversations, Agent: agent.Agent{Client: client}}
	m := &models.Message{ID: 12, Chat: models.Chat{ID: -1}, Text: "сам такой", ReplyToMessage: &models.Message{ID: 11, Text: "roast", From: &models.User{ID: 99}}}
	if !s.directReplyToBot(ctx, m) {
		t.Fatal("Telegram reply to configured bot was not recognized")
	}
	s.reply(ctx, b, m, current, 1)
	if client.calls != 1 {
		t.Fatalf("agent calls=%d", client.calls)
	}
	var persisted int
	if err := d.QueryRow(`SELECT count(*) FROM messages WHERE telegram_chat_id=-1 AND telegram_message_id=77 AND sender_type='bot'`).Scan(&persisted); err != nil || persisted != 1 {
		t.Fatalf("bot reply was not persisted: count=%d err=%v", persisted, err)
	}
}

func TestExplicitEventWriteOverridesEventRoot(t *testing.T) {
	if !isEventWriteRequest("Забей. Добавь завтра пятой парой физическую химию.") {
		t.Fatal("explicit event write was not detected")
	}
	if isEventWriteRequest("вторую") || isEventWriteRequest("на 16:30") {
		t.Fatal("clarification was misclassified as a new event request")
	}
}

func TestPrivateCommandsAndAllMentionBatches(t *testing.T) {
	s := Service{BotUsername: "configured_bot"}
	for _, tc := range []struct {
		text string
		want bool
	}{{"/sync", true}, {"/event завтра", false}, {"/ask вопрос", false}} {
		cmd, _ := s.command(tc.text)
		if privateCommandAllowed(cmd) != tc.want {
			t.Fatalf("command %q parsed as %q", tc.text, cmd)
		}
	}
	mentions := []string{"a", "b", "c", "d", "e", "f"}
	var batches [][]string
	for len(mentions) > 0 {
		end := 5
		if end > len(mentions) {
			end = len(mentions)
		}
		batches = append(batches, mentions[:end])
		mentions = mentions[end:]
	}
	if len(batches) != 2 || len(batches[0]) != 5 || len(batches[1]) != 1 {
		t.Fatalf("mention batches=%v", batches)
	}
}

func TestMediaDetails(t *testing.T) {
	voice := &models.Voice{FileID: "voice-id", MimeType: "audio/ogg"}
	if kind, id, mime := mediaDetails(&models.Message{Voice: voice}); kind != "voice" || id != "voice-id" || mime != "audio/ogg" {
		t.Fatalf("voice metadata: %q %q %q", kind, id, mime)
	}
	if kind, id, mime := mediaDetails(&models.Message{Photo: []models.PhotoSize{{FileID: "small"}, {FileID: "large"}}}); kind != "photo" || id != "large" || mime != "image/jpeg" {
		t.Fatalf("photo metadata: %q %q %q", kind, id, mime)
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

func TestAllDayDeadlineSummaryHasNoInventedTime(t *testing.T) {
	tz := time.UTC
	start := time.Date(2026, 9, 25, 0, 0, 0, 0, tz)
	end := start.AddDate(0, 0, 1)
	s := Service{Schedule: schedule.Service{TZ: tz}}
	got := s.eventSummary(schedule.Event{Title: "Сдать отчёт", Category: "deadline", AllDay: true, StartsAt: start, EndsAt: &end, Timezone: tz.String()}, schedule.Proposal{Operation: "create"})
	if !strings.Contains(got, "дедлайн: Сдать отчёт") || strings.Contains(got, "00:00") {
		t.Fatalf("deadline summary=%q", got)
	}
}

func TestEventSummaryReportsMergedDuplicate(t *testing.T) {
	s := Service{Schedule: schedule.Service{TZ: time.UTC}}
	got := s.eventSummary(schedule.Event{Title: "Практикум", StartsAt: time.Now().UTC(), MergedDuplicateID: 42}, schedule.Proposal{Operation: "update"})
	if !strings.Contains(got, "Объединил с дубликатом #42") {
		t.Fatalf("summary=%q", got)
	}
}

func TestRecurringSummaryAndDigestHideRRule(t *testing.T) {
	tz := time.UTC
	rule := "FREQ=WEEKLY;INTERVAL=2;BYDAY=TH;COUNT=6"
	start := time.Date(2026, 9, 10, 9, 0, 0, 0, tz)
	end := start.Add(95 * time.Minute)
	e := schedule.Event{Title: "Квантовая", StartsAt: start, EndsAt: &end, Timezone: tz.String(), RRule: &rule}
	s := Service{Schedule: schedule.Service{TZ: tz}}
	if got := s.eventSummary(e, schedule.Proposal{Operation: "create"}); !strings.Contains(got, "каждый четверг, раз в две недели, 6 занятий, 09:00–10:35") || strings.Contains(got, "FREQ=") {
		t.Fatalf("event summary=%q", got)
	}
	digest := s.announcementDigest([]announcementChange{{Kind: "event_create", Proposal: schedule.Proposal{Event: e}}})
	if !strings.Contains(digest, "каждый четверг, раз в две недели, 6 занятий, 09:00–10:35") || strings.Contains(digest, "FREQ=") {
		t.Fatalf("digest=%q", digest)
	}
}

func TestReceiptClaimOwnershipAndLeaseRecovery(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "receipt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := Service{DB: d}
	tokenA, claimed, err := s.claimReceipt(ctx, -1, 10)
	if err != nil || !claimed || tokenA == "" {
		t.Fatalf("first claim: token=%q claimed=%v err=%v", tokenA, claimed, err)
	}
	if _, claimed, err = s.claimReceipt(ctx, -1, 10); err != nil || claimed {
		t.Fatalf("concurrent claim accepted: claimed=%v err=%v", claimed, err)
	}
	if _, err = d.Exec(`UPDATE telegram_receipts SET lease_until='2000-01-01T00:00:00Z' WHERE chat_id=-1 AND message_id=10`); err != nil {
		t.Fatal(err)
	}
	tokenB, claimed, err := s.claimReceipt(ctx, -1, 10)
	if err != nil || !claimed || tokenB == tokenA {
		t.Fatalf("reclaim: token=%q claimed=%v err=%v", tokenB, claimed, err)
	}
	if err = s.completeReceipt(ctx, -1, 10, tokenA); err == nil {
		t.Fatal("stale owner finalized receipt")
	}
	if err = s.completeReceipt(ctx, -1, 10, tokenB); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err = s.claimReceipt(ctx, -1, 10); err != nil || claimed {
		t.Fatalf("done receipt reclaimed: claimed=%v err=%v", claimed, err)
	}
}

func TestEventSuccessRepliesWithOneCompactMessage(t *testing.T) {
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
	s.processEvent(ctx, b, -1, 10, 10, 1, "завтра пара по английскому в 14:00", "завтра пара по английскому в 14:00", "", false)
	if len(sent) != 1 {
		t.Fatalf("send count=%d payloads=%#v", len(sent), sent)
	}
	if got, want := sent[0]["text"], "Добавил: Английский — завтра, "+start.Format("15:04")+"–"+start.Add(95*time.Minute).Format("15:04")+"."; got != want {
		t.Fatalf("text=%q want=%q", got, want)
	}
	if sent[0]["reply_parameters"] != `{"message_id":10}` {
		t.Fatalf("successful event did not reply to command: %#v", sent[0])
	}
}

package telegram

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"group411/internal/admin"
	"group411/internal/agent"
	"group411/internal/ai"
	"group411/internal/conversation"
	"group411/internal/schedule"
	"group411/prompts"
)

type Service struct {
	DB                            *sql.DB
	Token                         string
	Schedule                      schedule.Service
	ChatID, GroupID               int64
	AdminID                       int64
	Discovery                     bool
	Bootstrap, BaseURL, GroupName string
	GitHubRepository              string
	BotUsername                   string
	BotUserID                     int64
	AI                            ai.Service
	Conversation                  conversation.Service
	Agent                         agent.Agent
}

type thinkingContextKey struct{}

type thinkingResponse struct {
	chatID    int64
	messageID int
	used      bool
}

var groupSyncMu sync.Mutex

func (s Service) conversations() conversation.Service {
	if s.Conversation.DB != nil {
		return s.Conversation
	}
	return conversation.Service{DB: s.DB, MaxDepth: 16, MaxChars: 3000}
}

func Start(ctx context.Context, token string, s Service) error {
	if token == "" {
		slog.Info("telegram disabled: TELEGRAM_BOT_TOKEN is empty")
		return nil
	}
	if s.ChatID == 0 && !s.Discovery {
		slog.Warn("telegram disabled: TELEGRAM_GROUP_CHAT_ID is empty")
		return nil
	}
	// The default handler is a method value. Bind it to the same Service
	// instance that receives GetMe metadata below; binding s.handle here would
	// capture an earlier copy with BotUserID still zero.
	service := &s
	b, err := bot.New(token, bot.WithDefaultHandler(service.handle))
	if err != nil {
		return fmt.Errorf("create telegram bot: %w", err)
	}
	me, err := b.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("get telegram bot identity: %w", err)
	}
	service.BotUsername = strings.TrimPrefix(strings.ToLower(me.Username), "@")
	service.BotUserID = me.ID
	for _, scope := range []models.BotCommandScope{&models.BotCommandScopeDefault{}, &models.BotCommandScopeAllPrivateChats{}, &models.BotCommandScopeAllGroupChats{}} {
		if _, err = b.DeleteMyCommands(ctx, &bot.DeleteMyCommandsParams{Scope: scope}); err != nil {
			return fmt.Errorf("clear telegram commands: %w", err)
		}
	}
	commands := []models.BotCommand{
		{Command: "event", Description: "Создать, изменить или отменить событие"},
		{Command: "today", Description: "Расписание на сегодня"},
		{Command: "week", Description: "Расписание на неделю"},
		{Command: "all", Description: "Позвать известных участников"},
		{Command: "roast", Description: "Подколоть участника"},
		{Command: "ask", Description: "Спросить о расписании"},
		{Command: "help", Description: "Справка"},
	}
	adminCommands := append(append([]models.BotCommand{}, commands...), models.BotCommand{Command: "sync", Description: "Опубликовать изменения в группе"})
	for _, id := range []int64{s.ChatID, s.AdminID} {
		if id == 0 {
			continue
		}
		selected := commands
		if id == s.AdminID {
			selected = adminCommands
		}
		if _, err = b.SetMyCommands(ctx, &bot.SetMyCommandsParams{Scope: &models.BotCommandScopeChat{ChatID: id}, Commands: selected}); err != nil {
			return fmt.Errorf("set telegram commands: %w", err)
		}
	}
	go b.Start(ctx)
	go service.dailyImageContext(ctx, b)
	go service.watchGitHubTags(ctx, b)
	slog.Info("telegram long polling started", "chat_id", service.ChatID, "bot_user_id", service.BotUserID)
	return nil
}

func (s *Service) handle(ctx context.Context, b *bot.Bot, u *models.Update) {
	m := u.Message
	if m == nil || m.From == nil {
		return
	}
	if s.Discovery && m.Chat.Type != models.ChatTypePrivate && s.AdminID > 0 && m.From.ID == s.AdminID && m.Text == "/chatid" {
		s.send(ctx, b, m.Chat.ID, fmt.Sprintf("Chat ID: %d\nДобавьте его в TELEGRAM_GROUP_CHAT_ID, выключите TELEGRAM_DISCOVERY_MODE и перезапустите сервис.", m.Chat.ID))
		return
	}
	if !s.allowed(m.Chat.ID, m.From.ID, m.Chat.Type == models.ChatTypePrivate) {
		slog.Warn("telegram message rejected", "chat_id", m.Chat.ID, "user_id", m.From.ID, "chat_type", m.Chat.Type, "configured_admin_id", s.AdminID, "configured_group_chat_id", s.ChatID)
		return
	}
	userID, err := admin.EnsureMember(s.DB, s.GroupID, m.From.ID, m.From.Username, m.From.FirstName, m.From.LastName, s.AdminID)
	if err != nil {
		slog.Error("telegram member upsert", "error", err)
		return
	}
	slog.Info("telegram message accepted", "message_id", m.ID, "user_id", m.From.ID)
	text := m.Text
	kind, fileID, mimeType := mediaDetails(m)
	if m.Voice != nil {
		voice, err := s.downloadTelegramFile(ctx, b, m.Voice.FileID)
		if err != nil {
			slog.Error("download voice", "error", err, "chat_id", m.Chat.ID, "message_id", m.ID)
			return
		}
		text, err = s.AI.Transcribe(ctx, voice, "voice.ogg", m.Voice.MimeType)
		if err != nil {
			slog.Error("transcribe voice", "error", err, "chat_id", m.Chat.ID, "message_id", m.ID)
			s.sendReply(ctx, b, m.Chat.ID, m.ID, "Не удалось распознать голосовое сообщение. Отправьте текстом.")
			return
		}
		kind = "voice"
	}
	m.Text = text
	if text != "" && m.Voice == nil {
		kind = "text"
	}
	claimToken, claimed, err := s.claimReceipt(ctx, m.Chat.ID, m.ID)
	if err != nil {
		slog.Error("telegram receipt claim", "error", err)
		return
	}
	if !claimed {
		return
	}
	stored, _, err := s.conversations().Ingest(ctx, conversation.Incoming{GroupID: s.GroupID, TelegramChatID: m.Chat.ID, TelegramMessageID: m.ID, UserID: userID, Kind: kind, Text: text, MediaGroupID: m.MediaGroupID, MediaFileID: fileID, MediaMIMEType: mimeType, ReplyToTelegramMessageID: replyID(m), SentAt: time.Unix(int64(m.Date), 0).UTC()})
	if err != nil {
		slog.Error("message ingestion failed", "error", err)
		if releaseErr := s.releaseReceipt(context.Background(), m.Chat.ID, m.ID, claimToken, err); releaseErr != nil {
			slog.Error("telegram receipt release", "error", releaseErr)
		}
		return
	}
	completed := false
	stopLease := s.renewReceipt(ctx, m.Chat.ID, m.ID, claimToken)
	defer func() {
		stopLease()
		if completed {
			if err := s.completeReceipt(context.Background(), m.Chat.ID, m.ID, claimToken); err != nil {
				slog.Error("telegram receipt finalize", "error", err)
			}
		} else if err := s.releaseReceipt(context.Background(), m.Chat.ID, m.ID, claimToken, nil); err != nil {
			slog.Error("telegram receipt release", "error", err)
		}
	}()
	private := m.Chat.Type == models.ChatTypePrivate
	if private {
		// Private admin messages are activated by policy, so they get the same
		// visible acknowledgement and eventual in-place result as group requests.
		ctx = s.withThinking(ctx, b, m.Chat.ID, m.ID)
		if strings.HasPrefix(text, "/") {
			cmd, arg := s.command(text)
			s.privateCommand(ctx, b, m, stored, userID, cmd, arg)
			completed = true
			return
		}
		s.askPrivate(ctx, b, m.Chat.ID, text, stored)
		completed = true
		return
	}
	if !strings.HasPrefix(text, "/") {
		if s.mentioned(text) {
			slog.Info("telegram routing", "message_id", m.ID, "chat_id", m.Chat.ID, "trigger", "mention", "resolved_mode", "conversation")
			ctx = s.withThinking(ctx, b, m.Chat.ID, m.ID)
			s.mention(ctx, b, m, stored)
			completed = true
			return
		}
		if s.directReplyToBot(ctx, m) {
			ctx = s.withThinking(ctx, b, m.Chat.ID, m.ID)
			s.reply(ctx, b, m, stored, userID)
		} else if m.ReplyToMessage != nil {
			slog.Info("telegram message ignored", "message_id", m.ID, "chat_id", m.Chat.ID, "reason", "reply_target_is_not_configured_bot", "reply_to_message_id", m.ReplyToMessage.ID, "reply_from_id", telegramReplyFromID(m), "reply_from_username", telegramReplyFromUsername(m), "reply_from_is_bot", telegramReplyFromIsBot(m), "configured_bot_user_id", s.BotUserID, "configured_bot_username", s.BotUsername)
		}
		completed = true
		return
	}
	cmd, arg := s.command(text)
	if cmd == "" {
		completed = true
		return
	}
	slog.Info("telegram routing", "message_id", m.ID, "chat_id", m.Chat.ID, "trigger", "command", "resolved_mode", strings.TrimPrefix(cmd, "/"))
	ctx = s.withThinking(ctx, b, m.Chat.ID, m.ID)
	switch cmd {
	case "/help", "/start":
		s.sendReply(ctx, b, m.Chat.ID, m.ID, "/event описание, /today, /week, /all, /roast @username, /ask вопрос\nСобытия: создать, перенести, заменить, отменить. Корректные запросы сохраняются сразу; пересечения времени сохраняются с предупреждением.\nРасписание: "+s.BaseURL)
	case "/today":
		s.todayReply(ctx, b, m.Chat.ID, m.ID)
	case "/week":
		s.sendReply(ctx, b, m.Chat.ID, m.ID, "Расписание на неделю: "+s.BaseURL)
	case "/roast":
		s.roastReply(ctx, b, m.Chat.ID, m.ID, arg, m.ReplyToMessage)
	case "/ask":
		s.askReply(ctx, b, m.Chat.ID, m.ID, arg, userID)
	case "/all":
		s.all(ctx, b, m.Chat.ID, arg)
	case "/event":
		if arg == "" && m.ReplyToMessage != nil {
			arg = m.ReplyToMessage.Text
		}
		if arg == "" {
			s.sendReply(ctx, b, m.Chat.ID, m.ID, "Опишите событие после /event.")
			break
		}
		stored.Text = arg
		s.runAgentReply(ctx, b, m.Chat.ID, m.ID, stored, agent.ModeGroupWrite)
	default:
		s.sendReply(ctx, b, m.Chat.ID, m.ID, "Неизвестная команда. Напишите /help.")
	}
	completed = true
}

func (s Service) renewReceipt(ctx context.Context, chatID int64, messageID int, token string) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := time.Now().UTC()
				lease := now.Add(2 * time.Minute).Format(time.RFC3339Nano)
				result, err := s.DB.ExecContext(context.Background(), "UPDATE telegram_receipts SET updated_at=?,lease_until=? WHERE chat_id=? AND message_id=? AND status='processing' AND claim_token=?", now.Format(time.RFC3339Nano), lease, chatID, messageID, token)
				if err != nil {
					slog.Error("telegram receipt lease renewal", "error", err, "chat_id", chatID, "message_id", messageID)
					continue
				}
				if n, _ := result.RowsAffected(); n != 1 {
					slog.Warn("telegram receipt lease no longer owned", "chat_id", chatID, "message_id", messageID)
					return
				}
			}
		}
	}()
	return func() { close(done) }
}

func (s Service) claimReceipt(ctx context.Context, chatID int64, messageID int) (string, bool, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", false, fmt.Errorf("receipt claim token: %w", err)
	}
	token := hex.EncodeToString(buf)
	nowTime := time.Now().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	lease := nowTime.Add(2 * time.Minute).Format(time.RFC3339Nano)
	result, err := s.DB.ExecContext(ctx, "INSERT OR IGNORE INTO telegram_receipts(chat_id,message_id,created_at,status,updated_at,claim_token,lease_until) VALUES(?,?,?,'processing',?,?,?)", chatID, messageID, now, now, token, lease)
	if err != nil {
		return "", false, err
	}
	if n, err := result.RowsAffected(); err != nil {
		return "", false, err
	} else if n == 1 {
		return token, true, nil
	}
	// A crashed worker leaves an expired lease. Compare-and-swap makes replay exclusive.
	result, err = s.DB.ExecContext(ctx, "UPDATE telegram_receipts SET status='processing',updated_at=?,claim_token=?,lease_until=? WHERE chat_id=? AND message_id=? AND (status='pending' OR (status='processing' AND lease_until < ?))", now, token, lease, chatID, messageID, now)
	if err != nil {
		return "", false, err
	}
	n, err := result.RowsAffected()
	return token, n == 1, err
}

func (s Service) completeReceipt(ctx context.Context, chatID int64, messageID int, token string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.DB.ExecContext(ctx, "UPDATE telegram_receipts SET status='done',claim_token=NULL,lease_until=NULL,processed_at=?,updated_at=? WHERE chat_id=? AND message_id=? AND status='processing' AND claim_token=?", now, now, chatID, messageID, token)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("receipt claim is no longer owned")
	}
	return nil
}

func (s Service) releaseReceipt(ctx context.Context, chatID int64, messageID int, token string, cause error) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var lastError any
	if cause != nil {
		lastError = cause.Error()
	}
	result, err := s.DB.ExecContext(ctx, "UPDATE telegram_receipts SET status='pending',claim_token=NULL,lease_until=NULL,attempts=attempts+1,last_error=?,updated_at=? WHERE chat_id=? AND message_id=? AND status='processing' AND claim_token=?", lastError, now, chatID, messageID, token)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("receipt claim is no longer owned")
	}
	return nil
}

func (s Service) mentioned(text string) bool {
	username := strings.TrimPrefix(strings.ToLower(s.BotUsername), "@")
	if username == "" {
		return false
	}
	for _, field := range strings.Fields(strings.ToLower(text)) {
		if strings.Trim(field, ".,!?:;()[]{}") == "@"+username {
			return true
		}
	}
	return false
}

func (s Service) mention(ctx context.Context, b *bot.Bot, m *models.Message, current conversation.Message) {
	s.runAgentReply(ctx, b, m.Chat.ID, m.ID, current, agent.ModeGroup)
}

func replyID(m *models.Message) *int {
	if m.ReplyToMessage == nil {
		return nil
	}
	id := m.ReplyToMessage.ID
	return &id
}

// directReplyToBot uses Telegram's authoritative reply metadata. The durable
// graph enriches context, but must never decide whether a reply is actionable.
func (s Service) directReplyToBot(ctx context.Context, m *models.Message) bool {
	if m.ReplyToMessage == nil {
		return false
	}
	from := m.ReplyToMessage.From
	if from != nil && s.BotUserID != 0 && from.ID == s.BotUserID {
		return true
	}
	if from != nil && s.BotUsername != "" && strings.EqualFold(strings.TrimPrefix(from.Username, "@"), strings.TrimPrefix(s.BotUsername, "@")) {
		return true
	}
	if s.DB == nil && s.Conversation.DB == nil {
		return false
	}
	parent, err := s.conversations().ByTelegramID(ctx, m.Chat.ID, m.ReplyToMessage.ID)
	return err == nil && parent.SenderType == conversation.SenderBot
}

func telegramReplyFromID(m *models.Message) int64 {
	if m.ReplyToMessage == nil || m.ReplyToMessage.From == nil {
		return 0
	}
	return m.ReplyToMessage.From.ID
}

func telegramReplyFromUsername(m *models.Message) string {
	if m.ReplyToMessage == nil || m.ReplyToMessage.From == nil {
		return ""
	}
	return m.ReplyToMessage.From.Username
}

func telegramReplyFromIsBot(m *models.Message) bool {
	return m.ReplyToMessage != nil && m.ReplyToMessage.From != nil && m.ReplyToMessage.From.IsBot
}

func mediaDetails(m *models.Message) (kind, fileID, mimeType string) {
	if m.Voice != nil {
		return "voice", m.Voice.FileID, m.Voice.MimeType
	}
	if len(m.Photo) > 0 {
		photo := m.Photo[len(m.Photo)-1]
		return "photo", photo.FileID, "image/jpeg"
	}
	return "other", "", ""
}

func (s Service) downloadTelegramFile(ctx context.Context, b *bot.Bot, fileID string) ([]byte, error) {
	file, err := b.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil || file.FilePath == "" || s.Token == "" {
		return nil, fmt.Errorf("get Telegram file: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.telegram.org/file/bot"+s.Token+"/"+file.FilePath, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Telegram file returned HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 10<<20+1))
}

func (s Service) dailyImageContext(ctx context.Context, b *bot.Bot) {
	for {
		now := time.Now().In(s.Schedule.TZ)
		next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 10, 0, 0, s.Schedule.TZ)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.enrichImages(ctx, b, time.Now().In(s.Schedule.TZ).AddDate(0, 0, -1))
		}
	}
}

func (s Service) watchGitHubTags(ctx context.Context, b *bot.Bot) {
	if s.AdminID == 0 || s.GitHubRepository == "" {
		return
	}
	check := func() {
		tag, err := latestGitHubTag(ctx, s.GitHubRepository)
		if err != nil || tag == "" {
			if err != nil {
				slog.Warn("github tag check", "error", err)
			}
			return
		}
		var known string
		err = s.DB.QueryRowContext(ctx, "SELECT tag FROM release_notification_state WHERE repository=?", s.GitHubRepository).Scan(&known)
		if err == sql.ErrNoRows {
			_, _ = s.DB.ExecContext(ctx, "INSERT INTO release_notification_state(repository,tag,updated_at) VALUES(?,?,?)", s.GitHubRepository, tag, time.Now().UTC().Format(time.RFC3339Nano))
			return
		}
		if err != nil || known == tag {
			return
		}
		if _, err = s.send(ctx, b, s.AdminID, "В GitHub появился новый тег "+tag+". Можно обновляться: https://github.com/"+s.GitHubRepository+"/releases/tag/"+tag); err != nil {
			return
		}
		_, _ = s.DB.ExecContext(ctx, "UPDATE release_notification_state SET tag=?,updated_at=? WHERE repository=?", tag, time.Now().UTC().Format(time.RFC3339Nano), s.GitHubRepository)
	}
	check()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

func latestGitHubTag(ctx context.Context, repository string) (string, error) {
	if strings.Count(repository, "/") != 1 {
		return "", fmt.Errorf("invalid GitHub repository")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repository+"/tags?per_page=1", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub tags returned HTTP %d", resp.StatusCode)
	}
	var tags []struct {
		Name string `json:"name"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tags); err != nil {
		return "", err
	}
	if len(tags) == 0 || strings.TrimSpace(tags[0].Name) == "" {
		return "", nil
	}
	return tags[0].Name, nil
}

func (s Service) enrichImages(ctx context.Context, b *bot.Bot, day time.Time) {
	if s.AI.VisionModel == "" {
		return
	}
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, s.Schedule.TZ).UTC().Format(time.RFC3339Nano)
	end := time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, s.Schedule.TZ).UTC().Format(time.RFC3339Nano)
	rows, err := s.DB.QueryContext(ctx, "SELECT id,user_id,media_file_id,COALESCE(media_mime_type,'image/jpeg') FROM messages WHERE group_id=? AND sender_type='user' AND kind='photo' AND sent_at>=? AND sent_at<? AND media_file_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM image_context_entries e WHERE e.message_id=messages.id) ORDER BY sent_at", s.GroupID, start, end)
	if err != nil {
		slog.Error("daily image context query", "error", err)
		return
	}
	defer rows.Close()
	type image struct {
		messageID, userID int64
		fileID, mime      string
	}
	byUser := map[int64][]image{}
	for rows.Next() {
		var x image
		if err := rows.Scan(&x.messageID, &x.userID, &x.fileID, &x.mime); err != nil {
			return
		}
		byUser[x.userID] = append(byUser[x.userID], x)
	}
	for userID, images := range byUser {
		if len(images) > 3 {
			continue
		}
		var notes []string
		for _, image := range images {
			data, err := s.downloadTelegramFile(ctx, b, image.fileID)
			if err != nil {
				slog.Error("daily image download", "error", err, "message_id", image.messageID)
				continue
			}
			note, err := s.AI.DescribeImage(ctx, data, image.mime)
			if err == nil && note != "" {
				notes = append(notes, note)
			}
		}
		if len(notes) == 0 {
			continue
		}
		note := limit(strings.Join(notes, " "), 900)
		_, err := s.DB.ExecContext(ctx, "INSERT INTO user_contexts(group_id,user_id,summary,tags_json,updated_at) VALUES(?,?,?,'[]',?) ON CONFLICT(group_id,user_id) DO UPDATE SET summary=substr(CASE WHEN summary='' THEN excluded.summary ELSE summary || ' ' || excluded.summary END,-3000),updated_at=excluded.updated_at", s.GroupID, userID, note, time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			slog.Error("daily image context save", "error", err, "user_id", userID)
			continue
		}
		for _, image := range images {
			if _, err := s.DB.ExecContext(ctx, "INSERT OR IGNORE INTO image_context_entries(message_id,created_at) VALUES(?,?)", image.messageID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				slog.Error("daily image context mark", "error", err, "message_id", image.messageID)
			}
		}
	}
}
func command(text string) (string, string)             { return commandFor("", text) }
func (s Service) command(text string) (string, string) { return commandFor(s.BotUsername, text) }
func commandFor(username, text string) (string, string) {
	f := strings.Fields(text)
	if len(f) == 0 {
		return "", ""
	}
	parts := strings.Split(f[0], "@")
	if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && (parts[1] == "" || (username != "" && !strings.EqualFold(parts[1], username)))) {
		return "", ""
	}
	return parts[0], strings.TrimSpace(strings.TrimPrefix(text, f[0]))
}
func (s Service) send(ctx context.Context, b *bot.Bot, chatID int64, text string) (*models.Message, error) {
	if s.finishThinking(ctx, b, chatID, text, "") {
		return nil, nil
	}
	m, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text})
	if err != nil {
		slog.Error("telegram send", "error", err)
	} else if m != nil {
		if _, _, saveErr := s.conversations().StoreBot(ctx, conversation.BotMessage{GroupID: s.GroupID, TelegramChatID: chatID, TelegramMessageID: m.ID, Kind: "text", Text: text, SentAt: time.Unix(int64(m.Date), 0).UTC()}); saveErr != nil {
			slog.Error("save bot message after successful Telegram send", "error", saveErr, "chat_id", chatID, "message_id", m.ID)
		}
	}
	return m, err
}
func (s Service) sendReply(ctx context.Context, b *bot.Bot, chatID int64, replyTo int, text string) (*models.Message, error) {
	if s.finishThinking(ctx, b, chatID, text, "") {
		return nil, nil
	}
	m, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text, ReplyParameters: &models.ReplyParameters{MessageID: replyTo}})
	if err != nil {
		slog.Error("telegram send", "error", err)
	} else if m != nil {
		if _, _, saveErr := s.conversations().StoreBot(ctx, conversation.BotMessage{GroupID: s.GroupID, TelegramChatID: chatID, TelegramMessageID: m.ID, Kind: "text", Text: text, ReplyToTelegramMessageID: &replyTo, SentAt: time.Unix(int64(m.Date), 0).UTC()}); saveErr != nil {
			slog.Error("save bot message after successful Telegram send", "error", saveErr, "chat_id", chatID, "message_id", m.ID, "reply_to_message_id", replyTo)
		}
	}
	return m, err
}

func (s Service) sendHTML(ctx context.Context, b *bot.Bot, chatID int64, text string) (*models.Message, error) {
	if s.finishThinking(ctx, b, chatID, text, models.ParseModeHTML) {
		return nil, nil
	}
	m, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text, ParseMode: models.ParseModeHTML})
	if err != nil {
		slog.Error("telegram send", "error", err)
		return nil, err
	}
	if m != nil {
		if _, _, saveErr := s.conversations().StoreBot(ctx, conversation.BotMessage{GroupID: s.GroupID, TelegramChatID: chatID, TelegramMessageID: m.ID, Kind: "text", Text: text, SentAt: time.Unix(int64(m.Date), 0).UTC()}); saveErr != nil {
			slog.Error("save bot message after successful Telegram send", "error", saveErr, "chat_id", chatID, "message_id", m.ID)
		}
	}
	return m, nil
}

func (s Service) withThinking(ctx context.Context, b *bot.Bot, chatID int64, replyTo int) context.Context {
	m, err := s.sendReply(ctx, b, chatID, replyTo, "Думаю…")
	if err != nil || m == nil {
		return ctx
	}
	return context.WithValue(ctx, thinkingContextKey{}, &thinkingResponse{chatID: chatID, messageID: m.ID})
}

// finishThinking edits the provisional reply once. If editing fails, callers
// retain their normal send path so the user still receives the final result.
func (s Service) finishThinking(ctx context.Context, b *bot.Bot, chatID int64, text string, parseMode models.ParseMode) bool {
	pending, ok := ctx.Value(thinkingContextKey{}).(*thinkingResponse)
	if !ok || pending == nil || pending.used || pending.chatID != chatID {
		return false
	}
	pending.used = true
	_, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: chatID, MessageID: pending.messageID, Text: text, ParseMode: parseMode})
	if err != nil {
		slog.Error("telegram edit thinking response", "error", err, "chat_id", chatID, "message_id", pending.messageID)
		return false
	}
	if err := s.conversations().UpdateBotText(ctx, chatID, pending.messageID, text); err != nil {
		slog.Error("save edited bot message", "error", err, "chat_id", chatID, "message_id", pending.messageID)
	}
	return true
}

// updateThinking keeps a single visible progress message while the agent works.
func (s Service) updateThinking(ctx context.Context, b *bot.Bot, chatID int64, text string) {
	pending, ok := ctx.Value(thinkingContextKey{}).(*thinkingResponse)
	if !ok || pending == nil || pending.used || pending.chatID != chatID {
		return
	}
	if _, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: chatID, MessageID: pending.messageID, Text: text}); err != nil {
		slog.Debug("telegram edit agent progress", "error", err, "chat_id", chatID, "message_id", pending.messageID)
		return
	}
	if err := s.conversations().UpdateBotText(ctx, chatID, pending.messageID, text); err != nil {
		slog.Error("save agent progress", "error", err, "chat_id", chatID, "message_id", pending.messageID)
	}
}
func (s Service) today(ctx context.Context, b *bot.Bot, chatID int64) {
	s.todayReply(ctx, b, chatID, 0)
}

func (s Service) todayReply(ctx context.Context, b *bot.Bot, chatID int64, replyTo int) {
	send := func(text string) {
		if replyTo != 0 {
			s.sendReply(ctx, b, chatID, replyTo, text)
			return
		}
		s.send(ctx, b, chatID, text)
	}
	st, err := s.Schedule.CurrentStatus(ctx, time.Now())
	if err != nil {
		send("Не удалось прочитать расписание.")
		return
	}
	if len(st.Today) == 0 {
		send("Сегодня событий нет. " + s.BaseURL)
		return
	}
	var lines []string
	for _, e := range st.Today {
		line := fmt.Sprintf("#%d", e.ID)
		if e.AllDay {
			if e.Category == "deadline" {
				line += " Дедлайн: "
			} else {
				line += " Весь день "
			}
		} else {
			line += " " + e.StartsAt.In(s.Schedule.TZ).Format("15:04")
		}
		if e.EndsAt != nil && !e.AllDay {
			line += "–" + e.EndsAt.In(s.Schedule.TZ).Format("15:04")
		}
		lines = append(lines, line+" "+e.Title+" ("+e.Category+")")
	}
	send(strings.Join(lines, "\n") + "\n" + s.BaseURL)
}
func (s Service) all(ctx context.Context, b *bot.Bot, chatID int64, text string) {
	rows, err := s.DB.QueryContext(ctx, "SELECT u.telegram_user_id,COALESCE(u.first_name,'участник') FROM users u JOIN group_members m ON m.user_id=u.id WHERE m.group_id=? AND m.active=1", s.GroupID)
	if err != nil {
		return
	}
	defer rows.Close()
	var mentions []string
	for rows.Next() {
		var id int64
		var name string
		_ = rows.Scan(&id, &name)
		mentions = append(mentions, fmt.Sprintf(`<a href="tg://user?id=%d">%s</a>`, id, htmlEscape(name)))
	}
	prefix := htmlEscape(limit(text, 1000))
	if prefix != "" {
		prefix += "\n"
	}
	for len(mentions) > 0 {
		part := prefix
		for tagged := 0; len(mentions) > 0 && tagged < 5 && len(part)+len(mentions[0])+1 < 3800; tagged++ {
			part += mentions[0] + " "
			mentions = mentions[1:]
		}
		message, sendErr := s.sendHTML(ctx, b, chatID, part)
		if sendErr != nil {
			return
		}
		_ = message
	}
}
func (s Service) ask(ctx context.Context, b *bot.Bot, chatID int64, question string, userID int64) {
	s.askReply(ctx, b, chatID, 0, question, userID)
}

func (s Service) askReply(ctx context.Context, b *bot.Bot, chatID int64, replyTo int, question string, userID int64) {
	send := func(text string) {
		if replyTo != 0 {
			s.sendReply(ctx, b, chatID, replyTo, text)
			return
		}
		s.send(ctx, b, chatID, text)
	}
	if question == "" {
		send("Напишите вопрос после /ask.")
		return
	}
	_ = userID
	if s.Agent.Client == nil {
		send("AI не настроен.")
		return
	}
	result, err := s.Agent.Run(ctx, agent.Conversation{Messages: []conversation.Message{{SenderType: conversation.SenderUser, Text: question}}, Now: time.Now(), Timezone: s.Schedule.TZ.String()})
	if err != nil {
		slog.Error("ask agent", "error", err)
		send("Не удалось получить ответ от AI. Попробуйте позже.")
		return
	}
	send(limit(result.Reply, 1800))
}
func (s Service) roast(ctx context.Context, b *bot.Bot, chatID int64, arg string, reply *models.Message) {
	s.roastReply(ctx, b, chatID, 0, arg, reply)
}

func (s Service) roastReply(ctx context.Context, b *bot.Bot, chatID int64, replyTo int, arg string, reply *models.Message) {
	send := func(text string) {
		if replyTo != 0 {
			s.sendReply(ctx, b, chatID, replyTo, text)
			return
		}
		s.send(ctx, b, chatID, text)
	}
	fields := strings.Fields(arg)
	username := ""
	if len(fields) > 0 {
		username = strings.TrimPrefix(fields[0], "@")
	}
	var id int64
	var name, summary string
	var enabled int
	if reply != nil && reply.From != nil {
		err := s.DB.QueryRowContext(ctx, "SELECT u.id,COALESCE(u.first_name,''),COALESCE(uc.summary,''),m.roast_enabled FROM users u JOIN group_members m ON m.user_id=u.id LEFT JOIN user_contexts uc ON uc.group_id=m.group_id AND uc.user_id=u.id WHERE m.group_id=? AND u.telegram_user_id=?", s.GroupID, reply.From.ID).Scan(&id, &name, &summary, &enabled)
		if err != nil {
			id = 0
		}
	} else if username != "" {
		err := s.DB.QueryRowContext(ctx, "SELECT u.id,COALESCE(u.first_name,''),COALESCE(uc.summary,''),m.roast_enabled FROM users u JOIN group_members m ON m.user_id=u.id LEFT JOIN user_contexts uc ON uc.group_id=m.group_id AND uc.user_id=u.id WHERE m.group_id=? AND lower(u.username)=lower(?)", s.GroupID, username).Scan(&id, &name, &summary, &enabled)
		if err != nil {
			id = 0
		}
	}
	if id == 0 {
		send("Укажите известного участника: /roast @username или reply на его сообщение.")
		return
	}
	if enabled == 0 {
		send("Этот участник отключил подколки.")
		return
	}
	answer, err := s.AI.Complete(ctx, prompts.RoastSystem, fmt.Sprintf("Участник: %s. Context для персонализации: %s", name, summary))
	if err != nil {
		slog.Error("roast AI", "error", err)
		send("Не удалось придумать подколку. Попробуйте позже.")
		return
	}
	send(limit(answer, 200))
}
func limit(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
func (s Service) allowed(chatID, userID int64, private bool) bool {
	if private {
		return s.ChatID != 0 && s.AdminID > 0 && userID == s.AdminID && chatID == userID
	}
	return s.ChatID != 0 && chatID == s.ChatID
}

func privateCommandAllowed(command string) bool { return command == "/sync" }

func (s Service) askPrivate(ctx context.Context, b *bot.Bot, chatID int64, question string, current conversation.Message) {
	s.runAgentReply(ctx, b, chatID, 0, current, agent.ModeAdminPrivate)
}

func (s Service) privateCommand(ctx context.Context, b *bot.Bot, m *models.Message, current conversation.Message, userID int64, command, arg string) {
	switch command {
	case "/sync":
		s.sync(ctx, b, m.Chat.ID, strings.EqualFold(strings.TrimSpace(arg), "preview"))
	case "/today":
		s.today(ctx, b, m.Chat.ID)
	case "/week":
		s.send(ctx, b, m.Chat.ID, "Расписание на неделю: "+s.BaseURL)
	case "/ask":
		s.askPrivate(ctx, b, m.Chat.ID, arg, current)
	case "/event":
		if arg == "" {
			s.send(ctx, b, m.Chat.ID, "Опишите событие после /event.")
			return
		}
		current.Text = arg
		s.runAgentReply(ctx, b, m.Chat.ID, m.ID, current, agent.ModeAdminPrivate)
	case "/help", "/start":
		s.send(ctx, b, m.Chat.ID, "Можно писать обычным текстом: добавить, перенести или отменить событие, а также спросить о расписании.\n/sync preview — показать накопленные изменения\n/sync — опубликовать их группе")
	default:
		s.send(ctx, b, m.Chat.ID, "Неизвестная команда. Напишите /help.")
	}
}

type announcementChange struct {
	ID       int64
	Kind     string
	Proposal schedule.Proposal
}

func (s Service) pendingAnnouncements(ctx context.Context) ([]announcementChange, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT c.id,c.kind,c.payload_json FROM group_announcement_changes a JOIN change_log c ON c.id=a.change_id WHERE a.group_id=? ORDER BY c.id", s.GroupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var changes []announcementChange
	for rows.Next() {
		var change announcementChange
		var raw string
		if err := rows.Scan(&change.ID, &change.Kind, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &change.Proposal); err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}
	return changes, rows.Err()
}

func (s Service) announcementDigest(changes []announcementChange) string {
	lines := []string{"Обновления расписания:"}
	for _, change := range changes {
		e := change.Proposal.Event
		verb := map[string]string{"event_create": "Добавлено", "event_update": "Обновлено", "event_cancel": "Отменено"}[change.Kind]
		when := e.StartsAt.In(s.Schedule.TZ).Format("02.01")
		if recurrence := recurrenceSummary(e, s.Schedule.TZ); recurrence != "" {
			when = recurrence
		}
		if !e.AllDay && change.Kind != "event_cancel" {
			when += ", " + e.StartsAt.In(s.Schedule.TZ).Format("15:04")
			if e.EndsAt != nil {
				when += "–" + e.EndsAt.In(s.Schedule.TZ).Format("15:04")
			}
		}
		if e.Category == "deadline" {
			verb = "Дедлайн"
		}
		lines = append(lines, "• "+verb+": "+e.Title+" — "+when+".")
	}
	return strings.Join(lines, "\n")
}

func (s Service) sync(ctx context.Context, b *bot.Bot, adminChatID int64, preview bool) {
	groupSyncMu.Lock()
	defer groupSyncMu.Unlock()
	changes, err := s.pendingAnnouncements(ctx)
	if err != nil {
		s.send(ctx, b, adminChatID, "Не удалось загрузить изменения.")
		return
	}
	if len(changes) == 0 {
		s.send(ctx, b, adminChatID, "Новых изменений для публикации нет.")
		return
	}
	digest := s.announcementDigest(changes)
	if preview {
		s.send(ctx, b, adminChatID, "Будет опубликовано:\n\n"+digest)
		return
	}
	for _, part := range telegramParts(digest) {
		if _, err = s.send(ctx, b, s.ChatID, part); err != nil {
			s.send(ctx, b, adminChatID, "Не удалось опубликовать изменения; они остались в очереди.")
			return
		}
	}
	ids := make([]any, len(changes))
	marks := make([]string, len(changes))
	for i, change := range changes {
		ids[i], marks[i] = change.ID, "?"
	}
	if _, err = s.DB.ExecContext(ctx, "DELETE FROM group_announcement_changes WHERE group_id=? AND change_id IN ("+strings.Join(marks, ",")+")", append([]any{s.GroupID}, ids...)...); err != nil {
		s.send(ctx, b, adminChatID, "Изменения опубликованы, но не удалось подтвердить синхронизацию. Повторите /sync.")
		return
	}
	s.send(ctx, b, adminChatID, fmt.Sprintf("Опубликовал %d изменений.", len(changes)))
}

func telegramParts(text string) []string {
	const max = 3800
	var parts []string
	for len([]rune(text)) > max {
		r := []rune(text)
		cut := max
		for cut > 0 && r[cut] != '\n' {
			cut--
		}
		if cut == 0 {
			cut = max
		}
		parts, text = append(parts, string(r[:cut])), strings.TrimSpace(string(r[cut:]))
	}
	return append(parts, text)
}

func (s Service) reply(ctx context.Context, b *bot.Bot, m *models.Message, current conversation.Message, userID int64) {
	_ = userID
	s.runAgentReply(ctx, b, m.Chat.ID, m.ID, current, agent.ModeGroup)
}

func (s Service) runAgentReply(ctx context.Context, b *bot.Bot, chatID int64, replyTo int, current conversation.Message, mode agent.Mode) {
	if s.Agent.Client == nil {
		return
	}
	messages := []conversation.Message{{SenderType: conversation.SenderUser, Text: current.Text}}
	if current.ID != 0 {
		if chain, err := s.conversations().BuildReplyChain(ctx, current.ID); err == nil && len(chain) > 0 {
			messages = chain
		}
	}
	botAgent := s.Agent
	botAgent.Progress = func(text string) { s.updateThinking(ctx, b, chatID, text) }
	result, err := botAgent.Run(ctx, agent.Conversation{Messages: messages, Now: time.Now(), Timezone: s.Schedule.TZ.String(), Mode: mode})
	if err != nil {
		slog.Error("agent", "error", err)
		if replyTo != 0 {
			s.sendReply(ctx, b, chatID, replyTo, "Не удалось обработать запрос. Попробуйте отправить его ещё раз.")
		} else {
			s.send(ctx, b, chatID, "Не удалось обработать запрос. Попробуйте отправить его ещё раз.")
		}
		return
	}
	if replyTo != 0 {
		s.sendReply(ctx, b, chatID, replyTo, limit(result.Reply, 1800))
		return
	}
	s.send(ctx, b, chatID, limit(result.Reply, 1800))
}

func (s Service) eventSummary(e schedule.Event, p schedule.Proposal) string {
	summary := "Добавил"
	if p.Operation == "cancel" {
		return "Отменил: " + e.Title + "."
	}
	if p.Operation == "update" {
		summary = "Обновил"
	}
	when := e.StartsAt.In(s.Schedule.TZ).Format("02.01")
	if recurrence := recurrenceSummary(e, s.Schedule.TZ); recurrence != "" {
		when = recurrence
	}
	if !e.AllDay {
		when += ", " + e.StartsAt.In(s.Schedule.TZ).Format("15:04")
	}
	today := time.Now().In(s.Schedule.TZ).Format("2006-01-02")
	if e.RRule == nil && e.StartsAt.In(s.Schedule.TZ).Format("2006-01-02") == today {
		when = "сегодня"
		if !e.AllDay {
			when += ", " + e.StartsAt.In(s.Schedule.TZ).Format("15:04")
		}
	} else if e.RRule == nil && e.StartsAt.In(s.Schedule.TZ).Format("2006-01-02") == time.Now().In(s.Schedule.TZ).AddDate(0, 0, 1).Format("2006-01-02") {
		when = "завтра"
		if !e.AllDay {
			when += ", " + e.StartsAt.In(s.Schedule.TZ).Format("15:04")
		}
	}
	if e.EndsAt != nil && !e.AllDay {
		when += "–" + e.EndsAt.In(s.Schedule.TZ).Format("15:04")
	}
	if e.RecurrenceHorizon == "semester" {
		when += ", до конца семестра"
	} else if e.RecurrenceHorizon == "default" {
		when += ", на ближайшие 16 недель"
	}
	if e.Category == "deadline" {
		summary += " дедлайн: " + e.Title + " — " + when + "."
	} else {
		summary += ": " + e.Title + " — " + when + "."
	}
	for _, warning := range e.Warnings {
		summary += fmt.Sprintf("\nПересекается с: %s, %s", limit(warning.Event.Title, 80), warning.StartsAt.In(s.Schedule.TZ).Format("02.01 15:04"))
	}
	if e.MergedDuplicateID != 0 {
		summary += fmt.Sprintf("\nОбъединил с дубликатом #%d.", e.MergedDuplicateID)
	}
	return summary
}

func recurrenceSummary(e schedule.Event, loc *time.Location) string {
	if e.RRule == nil {
		return ""
	}
	parts := map[string]string{}
	for _, part := range strings.Split(*e.RRule, ";") {
		key, value, ok := strings.Cut(part, "=")
		if ok {
			parts[key] = value
		}
	}
	days := map[string]string{"MO": "каждый понедельник", "TU": "каждый вторник", "WE": "каждую среду", "TH": "каждый четверг", "FR": "каждую пятницу", "SA": "каждую субботу", "SU": "каждое воскресенье"}
	freq, interval, day := parts["FREQ"], parts["INTERVAL"], days[parts["BYDAY"]]
	var summary string
	switch freq {
	case "WEEKLY":
		if interval == "2" {
			summary = "раз в две недели"
		} else {
			summary = "каждую неделю"
		}
		if day != "" {
			summary = day
			if interval == "2" {
				summary += ", раз в две недели"
			}
		}
	case "DAILY":
		summary = "ежедневно"
	case "MONTHLY":
		summary = "ежемесячно"
	case "YEARLY":
		summary = "ежегодно"
	default:
		return ""
	}
	if count := parts["COUNT"]; count != "" {
		word := "занятий"
		if count == "1" {
			word = "занятие"
		}
		summary += ", " + count + " " + word
	} else if e.RecurrenceHorizon == "explicit_until" {
		until := recurrenceUntil(parts["UNTIL"], loc)
		if until != "" {
			summary += ", до " + until
		}
	}
	return summary
}

func recurrenceUntil(value string, loc *time.Location) string {
	for _, layout := range []string{"20060102T150405Z", "20060102"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.In(loc).Format("02.01")
		}
	}
	return ""
}
func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;").Replace(s)
}

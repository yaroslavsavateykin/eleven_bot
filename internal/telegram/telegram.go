package telegram

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
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
	MTProto                       interface {
		Members(context.Context, int64) ([]TelegramMember, error)
	}
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
		{Command: "today", Description: "Расписание на сегодня"},
		{Command: "week", Description: "Расписание на неделю"},
		{Command: "all", Description: "Позвать всех участников"},
		{Command: "roast", Description: "Подколоть участника"},
		{Command: "context", Description: "Показать мой контекст"},
		{Command: "help", Description: "Справка"},
	}
	adminCommands := append(append([]models.BotCommand{}, commands...),
		models.BotCommand{Command: "sync", Description: "Опубликовать изменения в группе"},
		models.BotCommand{Command: "calendar", Description: "Всё расписание"},
		models.BotCommand{Command: "people", Description: "Сводка о людях"},
	)
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
	go service.watchAdminScheduleNotifications(ctx, b)
	slog.Info("telegram long polling started", "chat_id", service.ChatID, "bot_user_id", service.BotUserID)
	return nil
}

// Hermes mutations are already approved in Hermes Hub. Notify the Eleven
// administrator privately after the canonical DB transaction commits, without
// turning that notification into a public group announcement.
func (s Service) watchAdminScheduleNotifications(ctx context.Context, b *bot.Bot) {
	if s.AdminID == 0 {
		return
	}
	flush := func() {
		rows, err := s.DB.QueryContext(ctx, `SELECT n.change_id,c.kind,c.payload_json FROM admin_schedule_notifications n JOIN change_log c ON c.id=n.change_id WHERE n.group_id=? ORDER BY n.change_id LIMIT 20`, s.GroupID)
		if err != nil {
			slog.Error("load Hermes schedule notifications", "error", err)
			return
		}
		defer rows.Close()
		var ids []any
		var changes []announcementChange
		for rows.Next() {
			var change announcementChange
			var raw string
			if err = rows.Scan(&change.ID, &change.Kind, &raw); err != nil {
				return
			}
			if err = json.Unmarshal([]byte(raw), &change.Proposal); err != nil {
				slog.Error("decode Hermes schedule notification", "error", err)
				return
			}
			ids = append(ids, change.ID)
			changes = append(changes, change)
		}
		if err = rows.Err(); err != nil || len(changes) == 0 {
			return
		}
		if _, err = s.sendMarkdown(ctx, b, s.AdminID, "*Hermes изменил расписание после approval:*\n\n"+s.announcementDigest(changes)); err != nil {
			return
		}
		marks := make([]string, len(ids))
		for i := range ids {
			marks[i] = "?"
		}
		if _, err = s.DB.ExecContext(ctx, "DELETE FROM admin_schedule_notifications WHERE group_id=? AND change_id IN ("+strings.Join(marks, ",")+")", append([]any{s.GroupID}, ids...)...); err != nil {
			slog.Error("ack Hermes schedule notification", "error", err)
		}
	}
	flush()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			flush()
		}
	}
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
	// Keep join/leave service messages in local context storage even though /all
	// obtains its current roster from MTProto.
	if m.Chat.Type != models.ChatTypePrivate {
		for _, member := range m.NewChatMembers {
			if _, err := admin.EnsureMember(s.DB, s.GroupID, member.ID, member.Username, member.FirstName, member.LastName, s.AdminID); err != nil {
				slog.Error("telegram joined member upsert", "error", err, "user_id", member.ID)
			}
		}
		if m.LeftChatMember != nil {
			if err := admin.MarkMemberInactive(s.DB, s.GroupID, m.LeftChatMember.ID); err != nil {
				slog.Error("telegram departed member update", "error", err, "user_id", m.LeftChatMember.ID)
			}
		}
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
	if m.Chat.Type != models.ChatTypePrivate && strings.TrimSpace(text) != "" {
		s.refreshUserContext(ctx, userID)
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
		s.runAgentReply(ctx, b, m.Chat.ID, 0, stored, agent.ModeAdminPrivate)
		completed = true
		return
	}
	cmd, arg := s.command(text)
	if cmd == "/roast" {
		ctx = s.withThinking(ctx, b, m.Chat.ID, m.ID)
		s.roastReply(ctx, b, m.Chat.ID, m.ID, arg, m.ReplyToMessage)
		completed = true
		return
	}
	if cmd == "/context" {
		ctx = s.withThinking(ctx, b, m.Chat.ID, m.ID)
		s.contextReply(ctx, b, m.Chat.ID, m.ID, userID)
		completed = true
		return
	}
	if cmd == "/today" {
		ctx = s.withThinking(ctx, b, m.Chat.ID, m.ID)
		s.todayReply(ctx, b, m.Chat.ID, m.ID)
		completed = true
		return
	}
	if cmd == "/week" {
		ctx = s.withThinking(ctx, b, m.Chat.ID, m.ID)
		s.sendReply(ctx, b, m.Chat.ID, m.ID, "Расписание на неделю: "+s.BaseURL)
		completed = true
		return
	}
	if cmd == "/all" {
		ctx = s.withThinking(ctx, b, m.Chat.ID, m.ID)
		s.all(ctx, b, m.Chat.ID, arg)
		completed = true
		return
	}
	if cmd == "/help" || cmd == "/start" {
		ctx = s.withThinking(ctx, b, m.Chat.ID, m.ID)
		s.sendMarkdownReply(ctx, b, m.Chat.ID, m.ID, "Напишите обычным сообщением, что нужно сделать с расписанием или найти в истории группы. Можно тегнуть бота или ответить на его сообщение.\n\n`/roast` @username или reply — подколоть участника\n`/context` — показать ваш сохранённый контекст")
		completed = true
		return
	}
	trigger := ""
	if s.mentioned(text) {
		trigger = "mention"
	} else if s.directReplyToBot(ctx, m) {
		trigger = "reply"
	} else {
		// Keep every group message for participant context, but do not interrupt
		// the conversation unless the bot was explicitly addressed.
		completed = true
		return
	}
	slog.Info("telegram routing", "message_id", m.ID, "chat_id", m.Chat.ID, "trigger", trigger, "resolved_mode", "group_write")
	ctx = s.withThinking(ctx, b, m.Chat.ID, m.ID)
	s.runAgentReply(ctx, b, m.Chat.ID, m.ID, stored, agent.ModeGroupWrite)
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
	run := func() {
		if !s.claimProfileRefresh(ctx) {
			return
		}
		s.enrichImages(ctx, b)
		s.enrichMessages(ctx)
	}
	run()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

// claimProfileRefresh persists the daily limit so restarts cannot cause extra
// model calls or profile rewrites within the same 24-hour window.
func (s Service) claimProfileRefresh(ctx context.Context) bool {
	now := time.Now().UTC()
	result, err := s.DB.ExecContext(ctx, `INSERT INTO profile_refresh_state(group_id,refreshed_at) VALUES(?,?)
		ON CONFLICT(group_id) DO UPDATE SET refreshed_at=excluded.refreshed_at
		WHERE profile_refresh_state.refreshed_at<?`, s.GroupID, now.Format(time.RFC3339Nano), now.Add(-24*time.Hour).Format(time.RFC3339Nano))
	if err != nil {
		slog.Error("daily profile refresh claim", "error", err)
		return false
	}
	n, err := result.RowsAffected()
	return err == nil && n == 1
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

func (s Service) enrichImages(ctx context.Context, b *bot.Bot) {
	if s.AI.VisionModel == "" {
		return
	}
	since := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339Nano)
	rows, err := s.DB.QueryContext(ctx, "SELECT id,user_id,media_file_id,COALESCE(media_mime_type,'image/jpeg') FROM messages WHERE group_id=? AND sender_type='user' AND kind='photo' AND sent_at>=? AND media_file_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM image_context_entries e WHERE e.message_id=messages.id) ORDER BY sent_at", s.GroupID, since)
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
		note := limit(strings.Join(notes, " "), 300)
		_, err := s.DB.ExecContext(ctx, "INSERT INTO user_contexts(group_id,user_id,summary,tags_json,updated_at) VALUES(?,?,?,'[]',?) ON CONFLICT(group_id,user_id) DO UPDATE SET summary=excluded.summary,updated_at=excluded.updated_at", s.GroupID, userID, note, time.Now().UTC().Format(time.RFC3339Nano))
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

// enrichMessages rebuilds a factual profile from each member's full retained
// history. It runs once a day (plus on startup), never for every new message.
func (s Service) enrichMessages(ctx context.Context) {
	if s.AI.Key == "" || s.AI.Model == "" {
		return
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT m.id,m.user_id FROM messages m JOIN group_members gm ON gm.user_id=m.user_id AND gm.group_id=m.group_id WHERE m.group_id=? AND m.sender_type='user' AND COALESCE(m.text,'')<>'' AND gm.active=1 ORDER BY m.user_id,m.sent_at,m.id", s.GroupID)
	if err != nil {
		slog.Error("daily text context query", "error", err)
		return
	}
	defer rows.Close()
	type msg struct {
		id     int64
		userID int64
	}
	byUser := map[int64][]msg{}
	for rows.Next() {
		var m msg
		if err := rows.Scan(&m.id, &m.userID); err != nil {
			return
		}
		byUser[m.userID] = append(byUser[m.userID], m)
	}
	for userID, msgs := range byUser {
		if !s.refreshUserContext(ctx, userID) {
			continue
		}
		for _, m := range msgs {
			if _, err := s.DB.ExecContext(ctx, "INSERT OR IGNORE INTO text_context_entries(message_id,created_at) VALUES(?,?)", m.id, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				slog.Error("daily text context mark", "error", err, "message_id", m.id)
			}
		}
	}
}

// refreshUserContext builds a factual profile from every retained group
// message by one participant. It runs after each new message and daily as a
// safety net for historical messages.
func (s Service) refreshUserContext(ctx context.Context, userID int64) bool {
	if s.AI.Key == "" || s.AI.Model == "" {
		return false
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT COALESCE(text,'') FROM messages WHERE group_id=? AND user_id=? AND sender_type='user' AND COALESCE(text,'')<>'' AND text NOT LIKE '/%' ORDER BY sent_at DESC,id DESC", s.GroupID, userID)
	if err != nil {
		slog.Error("text context query", "error", err, "user_id", userID)
		return false
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return false
		}
		b.WriteString(text)
		b.WriteByte('\n')
		if b.Len() > 4000 {
			break
		}
	}
	if err := rows.Err(); err != nil || b.Len() == 0 {
		return false
	}
	note, err := s.AI.Complete(ctx, prompts.MessageContextSystem, b.String())
	if err != nil {
		slog.Error("text context generation", "error", err, "user_id", userID)
		return false
	}
	note = limit(strings.TrimSpace(note), 300)
	if _, err := s.DB.ExecContext(ctx, "UPDATE user_contexts SET summary=?,updated_at=? WHERE group_id=? AND user_id=?", note, time.Now().UTC().Format(time.RFC3339Nano), s.GroupID, userID); err != nil {
		slog.Error("text context save", "error", err, "user_id", userID)
		return false
	}
	return true
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
	return s.deliver(ctx, b, chatID, 0, text, "", false)
}
func (s Service) sendReply(ctx context.Context, b *bot.Bot, chatID int64, replyTo int, text string) (*models.Message, error) {
	return s.deliver(ctx, b, chatID, replyTo, text, "", false)
}
func (s Service) sendHTML(ctx context.Context, b *bot.Bot, chatID int64, text string) (*models.Message, error) {
	return s.deliver(ctx, b, chatID, 0, text, models.ParseModeHTML, false)
}
func (s Service) sendMarkdown(ctx context.Context, b *bot.Bot, chatID int64, text string) (*models.Message, error) {
	return s.deliver(ctx, b, chatID, 0, text, models.ParseModeMarkdown, true)
}
func (s Service) sendMarkdownReply(ctx context.Context, b *bot.Bot, chatID int64, replyTo int, text string) (*models.Message, error) {
	return s.deliver(ctx, b, chatID, replyTo, text, models.ParseModeMarkdown, true)
}

// deliver sends a message with the given parse mode, editing the pending
// provisional placeholder when present. When fallback is set and the formatted
// send fails, it retries as plain text so the user always receives the result.
func (s Service) deliver(ctx context.Context, b *bot.Bot, chatID int64, replyTo int, text string, parseMode models.ParseMode, fallback bool) (*models.Message, error) {
	if s.finishThinking(ctx, b, chatID, text, parseMode) {
		return nil, nil
	}
	m, err := s.post(ctx, b, chatID, replyTo, text, parseMode)
	if err != nil && fallback && parseMode != "" {
		m, err = s.post(ctx, b, chatID, replyTo, text, "")
	}
	return m, err
}

func (s Service) post(ctx context.Context, b *bot.Bot, chatID int64, replyTo int, text string, parseMode models.ParseMode) (*models.Message, error) {
	params := &bot.SendMessageParams{ChatID: chatID, Text: text, ParseMode: parseMode}
	if replyTo != 0 {
		params.ReplyParameters = &models.ReplyParameters{MessageID: replyTo}
	}
	m, err := b.SendMessage(ctx, params)
	if err != nil {
		slog.Error("telegram send", "error", err)
		return nil, err
	}
	if m != nil {
		msg := conversation.BotMessage{GroupID: s.GroupID, TelegramChatID: chatID, TelegramMessageID: m.ID, Kind: "text", Text: text, SentAt: time.Unix(int64(m.Date), 0).UTC()}
		if replyTo != 0 {
			msg.ReplyToTelegramMessageID = &replyTo
		}
		if _, _, saveErr := s.conversations().StoreBot(ctx, msg); saveErr != nil {
			slog.Error("save bot message after successful Telegram send", "error", saveErr, "chat_id", chatID, "message_id", m.ID)
		}
	}
	return m, nil
}

func (s Service) postEntities(ctx context.Context, b *bot.Bot, chatID int64, replyTo int, message MentionMessage) (*models.Message, error) {
	params := &bot.SendMessageParams{ChatID: chatID, Text: message.Text, Entities: message.Entities}
	if replyTo != 0 {
		params.ReplyParameters = &models.ReplyParameters{MessageID: replyTo}
	}
	m, err := b.SendMessage(ctx, params)
	if err != nil {
		slog.Error("telegram send mentions", "error", err)
		return nil, err
	}
	if m != nil {
		msg := conversation.BotMessage{GroupID: s.GroupID, TelegramChatID: chatID, TelegramMessageID: m.ID, Kind: "text", Text: message.Text, SentAt: time.Unix(int64(m.Date), 0).UTC()}
		if replyTo != 0 {
			msg.ReplyToTelegramMessageID = &replyTo
		}
		if _, _, err := s.conversations().StoreBot(ctx, msg); err != nil {
			slog.Error("save bot mention message", "error", err, "chat_id", chatID, "message_id", m.ID)
		}
	}
	return m, nil
}

// escapeMD escapes MarkdownV2 special characters so dynamic text renders literally.
func escapeMD(s string) string {
	if s == "" {
		return s
	}
	return strings.NewReplacer(
		`\`, `\\`,
		`_`, `\_`,
		`*`, `\*`,
		`[`, `\[`,
		`]`, `\]`,
		`(`, `\(`,
		`)`, `\)`,
		`~`, `\~`,
		"`", "\\`",
		`>`, `\>`,
		`#`, `\#`,
		`+`, `\+`,
		`-`, `\-`,
		`=`, `\=`,
		`|`, `\|`,
		`{`, `\{`,
		`}`, `\}`,
		`.`, `\.`,
		`!`, `\!`,
	).Replace(s)
}

func (s Service) withThinking(ctx context.Context, b *bot.Bot, chatID int64, replyTo int) context.Context {
	m, err := s.sendReply(ctx, b, chatID, replyTo, conversation.ProvisionalText)
	if err != nil || m == nil {
		return ctx
	}
	return context.WithValue(ctx, thinkingContextKey{}, &thinkingResponse{chatID: chatID, messageID: m.ID})
}

// finishThinking edits the provisional reply once. If editing fails, callers
// retain their normal send path so the user still receives the final result.
func (s Service) finishThinking(ctx context.Context, b *bot.Bot, chatID int64, text string, parseMode models.ParseMode) bool {
	return s.finishThinkingEntities(ctx, b, chatID, text, parseMode, nil)
}

func (s Service) finishThinkingEntities(ctx context.Context, b *bot.Bot, chatID int64, text string, parseMode models.ParseMode, entities []models.MessageEntity) bool {
	pending, ok := ctx.Value(thinkingContextKey{}).(*thinkingResponse)
	if !ok || pending == nil || pending.used || pending.chatID != chatID {
		return false
	}
	pending.used = true
	_, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: chatID, MessageID: pending.messageID, Text: text, ParseMode: parseMode, Entities: entities})
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
			s.sendMarkdownReply(ctx, b, chatID, replyTo, text)
			return
		}
		s.sendMarkdown(ctx, b, chatID, text)
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
		line := fmt.Sprintf("`#%d`", e.ID)
		if e.AllDay {
			if e.Category == "deadline" {
				line += " Дедлайн:"
			} else {
				line += " Весь день"
			}
		} else {
			line += " " + e.StartsAt.In(s.Schedule.TZ).Format("15:04")
		}
		if e.EndsAt != nil && !e.AllDay {
			line += "–" + e.EndsAt.In(s.Schedule.TZ).Format("15:04")
		}
		lines = append(lines, line+" *"+escapeMD(e.Title)+"* ("+escapeMD(e.Category)+")")
	}
	send(strings.Join(lines, "\n") + "\n" + s.BaseURL)
}
func (s Service) all(ctx context.Context, b *bot.Bot, chatID int64, text string) {
	sendError := func(text string) {
		if !s.finishThinking(ctx, b, chatID, text, "") {
			s.send(ctx, b, chatID, text)
		}
	}
	if s.MTProto == nil {
		sendError("/all недоступен: не настроен Telegram MTProto API.")
		return
	}
	members, err := s.MTProto.Members(ctx, chatID)
	if err != nil {
		slog.Error("Telegram participant roster", "error", err, "chat_id", chatID)
		if strings.Contains(err.Error(), "CHAT_ADMIN_REQUIRED") {
			sendError("Для /all мне нужны права администратора группы.")
			return
		}
		sendError("Не удалось получить участников группы для /all. Попробуйте позже.")
		return
	}
	batches := buildMentionBatches(text, members, s.BotUserID)
	if len(batches) == 0 {
		sendError("Не нашёл участников, которых можно позвать.")
		return
	}
	for i, batch := range batches {
		if i == 0 && s.finishThinkingEntities(ctx, b, chatID, batch.Text, "", batch.Entities) {
			continue
		}
		if _, err := s.postEntities(ctx, b, chatID, 0, batch); err != nil {
			return
		}
	}
}
func (s Service) roast(ctx context.Context, b *bot.Bot, chatID int64, arg string, reply *models.Message) {
	s.roastReply(ctx, b, chatID, 0, arg, reply)
}

func (s Service) roastReply(ctx context.Context, b *bot.Bot, chatID int64, replyTo int, arg string, reply *models.Message) {
	send := func(text string) {
		if replyTo != 0 {
			s.sendMarkdownReply(ctx, b, chatID, replyTo, text)
			return
		}
		s.sendMarkdown(ctx, b, chatID, text)
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

func (s Service) contextReply(ctx context.Context, b *bot.Bot, chatID int64, replyTo int, userID int64) {
	s.refreshUserContext(ctx, userID)
	summary := s.senderSummary(ctx, &userID)
	if summary == "" {
		s.sendReply(ctx, b, chatID, replyTo, "Пока не накопилось достаточно содержательных сообщений для контекста.")
		return
	}
	s.sendReply(ctx, b, chatID, replyTo, summary)
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

func (s Service) privateCommand(ctx context.Context, b *bot.Bot, m *models.Message, current conversation.Message, userID int64, command, arg string) {
	switch command {
	case "/sync":
		s.sync(ctx, b, m.Chat.ID, strings.EqualFold(strings.TrimSpace(arg), "preview"))
	case "/calendar":
		s.calendarDump(ctx, b, m.Chat.ID)
	case "/people":
		s.peopleSummaries(ctx, b, m.Chat.ID)
	case "/today":
		s.today(ctx, b, m.Chat.ID)
	case "/week":
		s.send(ctx, b, m.Chat.ID, "Расписание на неделю: "+s.BaseURL)
	case "/context":
		s.contextReply(ctx, b, m.Chat.ID, m.ID, userID)
	case "/help", "/start":
		s.sendMarkdown(ctx, b, m.Chat.ID, "*Команды:*\n`/sync preview` — показать накопленные изменения\n`/sync` — опубликовать их группе\n\nМожно писать обычным текстом: добавить, перенести или отменить событие, а также спросить о расписании. `/ask` и `/event` больше не нужны.")
	default:
		s.runAgentReply(ctx, b, m.Chat.ID, m.ID, current, agent.ModeAdminPrivate)
	}
}

// calendarDump lists every active event series (title, weekday, time, recurrence).
func (s Service) calendarDump(ctx context.Context, b *bot.Bot, chatID int64) {
	events, err := s.Schedule.Candidates(ctx)
	if err != nil {
		s.send(ctx, b, chatID, "Не удалось загрузить расписание.")
		return
	}
	if len(events) == 0 {
		s.send(ctx, b, chatID, "Расписание пустое.")
		return
	}
	loc := s.Schedule.TZ
	if loc == nil {
		loc = time.UTC
	}
	lines := make([]string, 0, len(events))
	for _, e := range events {
		when := e.StartsAt.In(loc).Format("02.01")
		if !e.AllDay {
			when += " " + e.StartsAt.In(loc).Format("15:04")
			if e.EndsAt != nil {
				when += "–" + e.EndsAt.In(loc).Format("15:04")
			}
		}
		line := fmt.Sprintf("`#%d` *%s* — %s %s", e.ID, escapeMD(e.Title), weekdayRu(e.StartsAt.In(loc).Weekday()), when)
		if rec := recurrenceSummary(e, loc); rec != "" {
			line += " (" + escapeMD(rec) + ")"
		}
		if e.Location != nil && strings.TrimSpace(*e.Location) != "" {
			line += ", ауд. " + escapeMD(*e.Location)
		}
		lines = append(lines, line)
	}
	for _, part := range telegramParts("*Расписание:*\n" + strings.Join(lines, "\n")) {
		s.sendMarkdown(ctx, b, chatID, part)
	}
}

// peopleSummaries lists what the bot has learned about each member.
func (s Service) peopleSummaries(ctx context.Context, b *bot.Bot, chatID int64) {
	rows, err := s.DB.QueryContext(ctx, `SELECT COALESCE(u.first_name,''),COALESCE(u.username,''),m.role,COALESCE(uc.summary,'') FROM group_members m JOIN users u ON u.id=m.user_id LEFT JOIN user_contexts uc ON uc.group_id=m.group_id AND uc.user_id=u.id WHERE m.group_id=? AND m.active=1 ORDER BY m.role DESC,u.first_name`, s.GroupID)
	if err != nil {
		s.send(ctx, b, chatID, "Не удалось загрузить сводку о людях.")
		return
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var name, username, role, summary string
		if err := rows.Scan(&name, &username, &role, &summary); err != nil {
			s.send(ctx, b, chatID, "Не удалось загрузить сводку о людях.")
			return
		}
		line := "*" + escapeMD(name) + "*"
		if username != "" {
			line += " (@" + escapeMD(username) + ")"
		}
		if role == "admin" {
			line += " — админ"
		}
		if strings.TrimSpace(summary) != "" {
			line += ": " + escapeMD(summary)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		s.send(ctx, b, chatID, "Не удалось загрузить сводку о людях.")
		return
	}
	if len(lines) == 0 {
		s.send(ctx, b, chatID, "О людях пока ничего не накоплено.")
		return
	}
	for _, part := range telegramParts("*Сводка о людях:*\n" + strings.Join(lines, "\n")) {
		s.sendMarkdown(ctx, b, chatID, part)
	}
}

func weekdayRu(d time.Weekday) string {
	switch d {
	case time.Monday:
		return "пн"
	case time.Tuesday:
		return "вт"
	case time.Wednesday:
		return "ср"
	case time.Thursday:
		return "чт"
	case time.Friday:
		return "пт"
	case time.Saturday:
		return "сб"
	default:
		return "вс"
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
	lines := []string{"*Обновления расписания:*"}
	for _, change := range changes {
		e := change.Proposal.Event
		verb := map[string]string{"event_create": "Добавлено", "event_update": "Обновлено", "event_cancel": "Отменено", "event_exclude_occurrence": "Исключено занятие на дату"}[change.Kind]
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
		lines = append(lines, "• *"+verb+":* "+escapeMD(e.Title)+" — "+when+".")
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
		s.sendMarkdown(ctx, b, adminChatID, "Будет опубликовано:\n\n"+digest)
		return
	}
	for _, part := range telegramParts(digest) {
		if _, err = s.sendMarkdown(ctx, b, s.ChatID, part); err != nil {
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
	if mode == agent.ModeAdminPrivate {
		if recent, err := s.conversations().Recent(ctx, chatID, 10); err != nil {
			slog.Error("private conversation context failed", "error", err)
		} else if len(recent) > 0 {
			messages = recent
		}
	} else if current.ID != 0 {
		if chain, err := s.conversations().BuildReplyChain(ctx, current.ID); err == nil && len(chain) > 0 {
			messages = chain
		}
	}
	// Preserve the live current text (including long pasted batches) and the
	// immediate reply parent even when the graph's preliminary history is bounded.
	if current.ReplyToMessageID != nil {
		if parent, err := s.conversations().ByID(ctx, *current.ReplyToMessageID); err == nil {
			found := false
			for i := range messages {
				if messages[i].ID == parent.ID {
					messages[i] = parent
					found = true
				}
			}
			if !found {
				messages = append([]conversation.Message{parent}, messages...)
			}
		}
	}
	if len(messages) > 0 && (messages[len(messages)-1].ID == current.ID || current.ID == 0) {
		messages[len(messages)-1] = current
	} else {
		messages = append(messages, current)
	}
	if current.ID != 0 {
		kept := messages[:0]
		for _, m := range messages {
			if m.ID <= current.ID {
				kept = append(kept, m)
			}
		}
		messages = kept
		sort.SliceStable(messages, func(i, j int) bool { return messages[i].ID < messages[j].ID })
	}
	messages = s.withAuthorNames(ctx, messages)
	botAgent := s.Agent
	botAgent.Progress = func(text string) { s.updateThinking(ctx, b, chatID, text) }
	result, err := botAgent.Run(ctx, agent.Conversation{RunID: fmt.Sprintf("telegram:%d:%d", chatID, current.TelegramMessageID), Messages: messages, Now: time.Now(), Timezone: s.Schedule.TZ.String(), Mode: mode, SenderSummary: s.senderSummary(ctx, current.UserID)})
	if err != nil {
		slog.Error("agent", "error", err)
		text := agentErrorReply(err)
		if replyTo != 0 {
			s.sendReply(ctx, b, chatID, replyTo, text)
		} else {
			s.send(ctx, b, chatID, text)
		}
		return
	}
	if replyTo != 0 {
		s.sendReply(ctx, b, chatID, replyTo, limit(result.Reply, 1800))
		return
	}
	s.send(ctx, b, chatID, limit(result.Reply, 1800))
}

// senderSummary returns the compact per-user context for the current sender.
func (s Service) senderSummary(ctx context.Context, userID *int64) string {
	if userID == nil {
		return ""
	}
	var summary string
	if err := s.DB.QueryRowContext(ctx, "SELECT COALESCE(summary,'') FROM user_contexts WHERE group_id=? AND user_id=?", s.GroupID, *userID).Scan(&summary); err != nil {
		return ""
	}
	return strings.TrimSpace(summary)
}

// withAuthorNames prefixes user messages with the speaker's first name so the
// agent can tell who is replying to whom without inventing identities.
func (s Service) withAuthorNames(ctx context.Context, messages []conversation.Message) []conversation.Message {
	ids := map[int64]struct{}{}
	for _, m := range messages {
		if m.UserID != nil {
			ids[*m.UserID] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return messages
	}
	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT id, COALESCE(NULLIF(first_name,''), NULLIF(username,''), 'участник') FROM users WHERE id IN ("+strings.Join(placeholders, ",")+")", args...)
	if err != nil {
		return messages
	}
	defer rows.Close()
	names := map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if rows.Scan(&id, &name) == nil {
			names[id] = name
		}
	}
	out := make([]conversation.Message, len(messages))
	for i, m := range messages {
		out[i] = m
		if m.SenderType == conversation.SenderUser && m.UserID != nil {
			if name := names[*m.UserID]; name != "" {
				out[i].Text = name + ": " + m.Text
			}
		}
	}
	return out
}

func agentErrorReply(err error) string {
	var provider *ai.Error
	if errors.As(err, &provider) {
		switch provider.Kind {
		case "provider_configuration":
			return "AI-сервис отклонил запрос. Администратору нужно проверить модель, доступ и режим инструментов в настройках."
		case "provider_protocol":
			return "AI-сервис вернул неполный или некорректный ответ. Запрос не удалось завершить; если изменения уже были сохранены, проверьте расписание перед повтором."
		case "provider_transport":
			return "AI-сервис временно недоступен. Попробуйте позже; ранее сохранённые изменения остаются в расписании."
		}
	}
	return "Не удалось завершить запрос. Проверьте расписание перед повтором: часть изменений могла сохраниться."
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

// Package conversation owns durable Telegram message ingestion and reply context.
package conversation

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type Service struct {
	DB       *sql.DB
	MaxDepth int
	MaxChars int
}

func (s Service) Ingest(ctx context.Context, in Incoming) (Message, bool, error) {
	if in.Kind == "" {
		in.Kind = "other"
	}
	if in.SentAt.IsZero() {
		in.SentAt = time.Now().UTC()
	}
	return s.insert(ctx, in.GroupID, in.TelegramChatID, in.TelegramMessageID, SenderUser, &in.UserID, in.Kind, in.Text, in.MediaGroupID, in.MediaFileID, in.MediaMIMEType, in.ReplyToTelegramMessageID, in.SentAt)
}

func (s Service) StoreBot(ctx context.Context, in BotMessage) (Message, bool, error) {
	if in.SentAt.IsZero() {
		in.SentAt = time.Now().UTC()
	}
	return s.insert(ctx, in.GroupID, in.TelegramChatID, in.TelegramMessageID, SenderBot, nil, in.Kind, in.Text, "", "", "", in.ReplyToTelegramMessageID, in.SentAt)
}

func (s Service) insert(ctx context.Context, groupID, chatID int64, telegramID int, sender SenderType, userID *int64, kind, text, media, fileID, mimeType string, replyTelegram *int, sentAt time.Time) (Message, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, false, err
	}
	defer tx.Rollback()
	var existing int64
	var existingSender SenderType
	err = tx.QueryRowContext(ctx, "SELECT id,sender_type FROM messages WHERE telegram_chat_id=? AND telegram_message_id=?", chatID, telegramID).Scan(&existing, &existingSender)
	if err == nil {
		if existingSender != sender {
			// Telegram IDs are unique within a chat. A conflicting local record is
			// stale data (for example, from an earlier bot run), never a reason to
			// discard the live update and break its reply chain.
			var parent any
			if replyTelegram != nil {
				var parentID int64
				if err = tx.QueryRowContext(ctx, "SELECT id FROM messages WHERE telegram_chat_id=? AND telegram_message_id=?", chatID, *replyTelegram).Scan(&parentID); err == nil {
					parent = parentID
				}
				if err != nil && err != sql.ErrNoRows {
					return Message{}, false, err
				}
			}
			if _, err = tx.ExecContext(ctx, `UPDATE messages SET group_id=?,user_id=?,sender_type=?,sent_at=?,kind=?,text=?,media_group_id=?,media_file_id=?,media_mime_type=?,reply_to_telegram_message_id=?,reply_to_message_id=?,metadata_json='{}',created_at=? WHERE id=?`, groupID, userID, sender, sentAt.UTC().Format(time.RFC3339Nano), kind, text, nullable(media), nullable(fileID), nullable(mimeType), replyTelegram, parent, time.Now().UTC().Format(time.RFC3339Nano), existing); err != nil {
				return Message{}, false, err
			}
			if err = tx.Commit(); err != nil {
				return Message{}, false, err
			}
			m, err := s.ByID(ctx, existing)
			return m, true, err
		}
		if err = tx.Commit(); err != nil {
			return Message{}, false, err
		}
		m, err := s.ByID(ctx, existing)
		return m, false, err
	}
	if err != sql.ErrNoRows {
		return Message{}, false, err
	}
	var parent any
	if replyTelegram != nil {
		var parentID int64
		if err = tx.QueryRowContext(ctx, "SELECT id FROM messages WHERE telegram_chat_id=? AND telegram_message_id=?", chatID, *replyTelegram).Scan(&parentID); err == nil {
			parent = parentID
		}
		if err != nil && err != sql.ErrNoRows {
			return Message{}, false, err
		}
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO messages(group_id,telegram_chat_id,telegram_message_id,user_id,sender_type,sent_at,kind,text,media_group_id,media_file_id,media_mime_type,reply_to_telegram_message_id,reply_to_message_id,metadata_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, groupID, chatID, telegramID, userID, sender, sentAt.UTC().Format(time.RFC3339Nano), kind, text, nullable(media), nullable(fileID), nullable(mimeType), replyTelegram, parent, "{}", time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return Message{}, false, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Message{}, false, err
	}
	// Telegram can deliver a child before its quoted parent. Repair its local edge once the parent arrives.
	if _, err = tx.ExecContext(ctx, "UPDATE messages SET reply_to_message_id=? WHERE telegram_chat_id=? AND reply_to_telegram_message_id=? AND reply_to_message_id IS NULL", id, chatID, telegramID); err != nil {
		return Message{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return Message{}, false, err
	}
	m, err := s.ByID(ctx, id)
	return m, true, err
}

// FindConversationRoot walks farther than the AI context budget to locate the initiating command.
// It reports false for a missing root, a cycle, or a chain beyond maxEdges.
func (s Service) FindConversationRoot(ctx context.Context, messageID int64, command string, maxEdges int) (Message, bool, error) {
	if maxEdges <= 0 {
		maxEdges = 128
	}
	seen := make(map[int64]struct{}, maxEdges)
	for id, edges := messageID, 0; id != 0 && edges <= maxEdges; edges++ {
		if _, ok := seen[id]; ok {
			return Message{}, false, nil
		}
		seen[id] = struct{}{}
		m, err := s.ByID(ctx, id)
		if err == sql.ErrNoRows {
			return Message{}, false, nil
		}
		if err != nil {
			return Message{}, false, fmt.Errorf("load conversation root: %w", err)
		}
		if m.SenderType == SenderUser && isCommand(m.Text, command) {
			return m, true, nil
		}
		if m.ReplyToMessageID == nil {
			return Message{}, false, nil
		}
		id = *m.ReplyToMessageID
	}
	return Message{}, false, nil
}

func isCommand(text, command string) bool {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false
	}
	parts := strings.Split(fields[0], "@")
	return len(parts) <= 2 && parts[0] == command && (len(parts) == 1 || parts[1] != "")
}

func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func (s Service) ByID(ctx context.Context, id int64) (Message, error) {
	return scanMessage(s.DB.QueryRowContext(ctx, `SELECT id,group_id,telegram_chat_id,telegram_message_id,sender_type,user_id,kind,COALESCE(text,''),media_group_id,media_file_id,media_mime_type,reply_to_telegram_message_id,reply_to_message_id,sent_at,created_at FROM messages WHERE id=?`, id))
}

func (s Service) ByTelegramID(ctx context.Context, chatID int64, telegramID int) (Message, error) {
	return scanMessage(s.DB.QueryRowContext(ctx, `SELECT id,group_id,telegram_chat_id,telegram_message_id,sender_type,user_id,kind,COALESCE(text,''),media_group_id,media_file_id,media_mime_type,reply_to_telegram_message_id,reply_to_message_id,sent_at,created_at FROM messages WHERE telegram_chat_id=? AND telegram_message_id=?`, chatID, telegramID))
}

func (s Service) UpdateBotText(ctx context.Context, chatID int64, telegramID int, text string) error {
	result, err := s.DB.ExecContext(ctx, "UPDATE messages SET text=? WHERE telegram_chat_id=? AND telegram_message_id=? AND sender_type=?", text, chatID, telegramID, SenderBot)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return fmt.Errorf("bot message not found")
	}
	return nil
}

func (s Service) BuildReplyChain(ctx context.Context, messageID int64) ([]Message, error) {
	depth, chars := s.MaxDepth, s.MaxChars
	if depth <= 0 {
		depth = 16
	}
	if chars <= 0 {
		// The OpenAI-compatible text transport accepts at most 6 KB prompts.
		chars = 3000
	}
	seen := map[int64]bool{}
	reversed := make([]Message, 0, depth)
	probeDepth := depth * 4
	if probeDepth < depth {
		probeDepth = depth
	}
	for id := messageID; id != 0 && len(reversed) < probeDepth; {
		if seen[id] {
			break
		}
		seen[id] = true
		m, err := s.ByID(ctx, id)
		if err == sql.ErrNoRows {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("load reply chain: %w", err)
		}
		reversed = append(reversed, m)
		if m.ReplyToMessageID == nil {
			break
		}
		id = *m.ReplyToMessageID
	}
	chain := make([]Message, len(reversed))
	for i := range reversed {
		chain[len(reversed)-1-i] = reversed[i]
	}
	if len(chain) > depth {
		if strings.HasPrefix(strings.TrimSpace(chain[0].Text), "/event") {
			chain = append([]Message{chain[0]}, chain[len(chain)-depth+1:]...)
		} else {
			chain = chain[len(chain)-depth:]
		}
	}
	used := 0
	for _, m := range chain {
		used += len(m.Text)
	}
	if used <= chars {
		return chain, nil
	}
	keep := make([]Message, 0, len(chain))
	start := 0
	if strings.HasPrefix(strings.TrimSpace(chain[0].Text), "/event") {
		root := chain[0]
		root.Text = truncateBytes(root.Text, chars/2)
		keep = append(keep, root)
		used = len(root.Text)
		start = 1
	} else {
		used = 0
	}
	tail := make([]Message, 0, len(chain))
	for i := len(chain) - 1; i >= start; i-- {
		item := chain[i]
		if len(tail) == 0 && used+len(item.Text) > chars {
			item.Text = truncateBytes(item.Text, max(0, chars-used))
		}
		if len(tail) > 0 && used+len(item.Text) > chars {
			break
		}
		tail = append(tail, item)
		used += len(item.Text)
	}
	for i := len(tail) - 1; i >= 0; i-- {
		keep = append(keep, tail[i])
	}
	// A reply only makes sense alongside the bot message it quotes. Under an
	// unusually small budget retain that immediate pair, truncating their text
	// rather than dropping the parent relation from the model context.
	if len(keep) < 2 && len(chain) >= 2 {
		parent, current := chain[len(chain)-2], chain[len(chain)-1]
		parent.Text = truncateBytes(parent.Text, chars/2)
		current.Text = truncateBytes(current.Text, max(0, chars-len(parent.Text)))
		return []Message{parent, current}, nil
	}
	return keep, nil
}

func truncateBytes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	for limit > 0 && (text[limit]&0xc0) == 0x80 {
		limit--
	}
	return text[:limit]
}

type rowScanner interface{ Scan(...any) error }

func scanMessage(row rowScanner) (Message, error) {
	var m Message
	var sender string
	var user sql.NullInt64
	var media sql.NullString
	var fileID, mimeType sql.NullString
	var replyTelegram sql.NullInt64
	var reply sql.NullInt64
	var sent, created string
	err := row.Scan(&m.ID, &m.GroupID, &m.TelegramChatID, &m.TelegramMessageID, &sender, &user, &m.Kind, &m.Text, &media, &fileID, &mimeType, &replyTelegram, &reply, &sent, &created)
	if err != nil {
		return m, err
	}
	m.SenderType = SenderType(sender)
	if user.Valid {
		v := user.Int64
		m.UserID = &v
	}
	if media.Valid {
		v := media.String
		m.MediaGroupID = &v
	}
	if fileID.Valid {
		v := fileID.String
		m.MediaFileID = &v
	}
	if mimeType.Valid {
		v := mimeType.String
		m.MediaMIMEType = &v
	}
	if replyTelegram.Valid {
		v := int(replyTelegram.Int64)
		m.ReplyToTelegramMessageID = &v
	}
	if reply.Valid {
		v := reply.Int64
		m.ReplyToMessageID = &v
	}
	m.SentAt, _ = time.Parse(time.RFC3339Nano, sent)
	m.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return m, nil
}

func (s Service) Prune(ctx context.Context, before time.Time) error {
	// Preserve graph integrity for surviving children while keeping Telegram parent IDs for future diagnostics.
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "UPDATE messages SET reply_to_message_id=NULL WHERE reply_to_message_id IN (SELECT id FROM messages WHERE created_at < ?)", before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM messages WHERE created_at < ?", before.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

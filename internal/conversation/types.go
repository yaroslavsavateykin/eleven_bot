package conversation

import "time"

type SenderType string

const (
	SenderUser SenderType = "user"
	SenderBot  SenderType = "bot"
)

type Message struct {
	ID                       int64
	GroupID                  int64
	TelegramChatID           int64
	TelegramMessageID        int
	SenderType               SenderType
	UserID                   *int64
	Kind                     string
	Text                     string
	MediaGroupID             *string
	MediaFileID              *string
	MediaMIMEType            *string
	ReplyToTelegramMessageID *int
	ReplyToMessageID         *int64
	SentAt                   time.Time
	CreatedAt                time.Time
}

type Incoming struct {
	GroupID                                              int64
	TelegramChatID                                       int64
	TelegramMessageID                                    int
	UserID                                               int64
	Kind, Text, MediaGroupID, MediaFileID, MediaMIMEType string
	ReplyToTelegramMessageID                             *int
	SentAt                                               time.Time
}

type BotMessage struct {
	GroupID                  int64
	TelegramChatID           int64
	TelegramMessageID        int
	Kind, Text               string
	ReplyToTelegramMessageID *int
	SentAt                   time.Time
}

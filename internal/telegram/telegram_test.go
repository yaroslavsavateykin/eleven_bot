package telegram

import (
	"context"
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestAuthorizationAndCommands(t *testing.T) {
	s := Service{ChatID: -100, AdminID: 123, BotUsername: "eleven_bot"}
	if !s.allowed(-100, 4, false) || !s.allowed(123, 123, true) || s.allowed(456, 456, true) {
		t.Fatal("authorization policy")
	}
	if command, arg := s.command("/event@eleven_bot завтра"); command != "/event" || arg != "завтра" {
		t.Fatalf("command=%q arg=%q", command, arg)
	}
}
func TestRoutingTriggers(t *testing.T) {
	s := Service{BotUsername: "configured_bot", BotUserID: 42}
	if !s.mentioned("@configured_bot что завтра?") || s.mentioned("обычный текст") {
		t.Fatal("mention routing")
	}
	m := &models.Message{Chat: models.Chat{ID: -1}, ReplyToMessage: &models.Message{ID: 10, From: &models.User{ID: 42, IsBot: true}}}
	if !s.directReplyToBot(context.Background(), m) {
		t.Fatal("reply to configured bot")
	}
}

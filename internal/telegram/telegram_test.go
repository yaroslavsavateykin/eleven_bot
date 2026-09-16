package telegram

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/go-telegram/bot/models"
	"group411/internal/admin"
	"group411/internal/db"
)

func TestAuthorizationAndCommands(t *testing.T) {
	s := Service{ChatID: -100, AdminID: 123, BotUsername: "eleven_bot"}
	if !s.allowed(-100, 4, false) || !s.allowed(123, 123, true) || s.allowed(456, 456, true) {
		t.Fatal("authorization policy")
	}
	if command, arg := s.command("/context@eleven_bot"); command != "/context" || arg != "" {
		t.Fatalf("command=%q arg=%q", command, arg)
	}
}

func TestMemberRosterIncludesSilentJoinedMembers(t *testing.T) {
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec("INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','now','now')"); err != nil {
		t.Fatal(err)
	}
	id, err := admin.EnsureMember(d, 1, 77, "silent", "Тихий", "Участник", 0)
	if err != nil {
		t.Fatal(err)
	}
	var active int
	if err = d.QueryRow("SELECT active FROM group_members WHERE group_id=1 AND user_id=?", id).Scan(&active); err != nil || active != 1 {
		t.Fatalf("joined member: active=%d err=%v", active, err)
	}
	var summary string
	if err = d.QueryRow("SELECT summary FROM user_contexts WHERE group_id=1 AND user_id=?", id).Scan(&summary); err != nil || summary != "" {
		t.Fatalf("silent member profile: summary=%q err=%v", summary, err)
	}
	if err = admin.MarkMemberInactive(d, 1, 77); err != nil {
		t.Fatal(err)
	}
	if err = d.QueryRow("SELECT active FROM group_members WHERE group_id=1 AND user_id=?", id).Scan(&active); err != nil || active != 0 {
		t.Fatalf("departed member: active=%d err=%v", active, err)
	}
}

func TestProfileRefreshIsLimitedToOncePerDay(t *testing.T) {
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec("INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','now','now')"); err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d, GroupID: 1}
	if !s.claimProfileRefresh(context.Background()) {
		t.Fatal("first refresh claim was not granted")
	}
	if s.claimProfileRefresh(context.Background()) {
		t.Fatal("second refresh claim was granted within 24 hours")
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

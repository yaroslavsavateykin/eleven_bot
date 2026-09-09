package conversation

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"group411/internal/db"
)

func TestReplyChainPersistsBothSendersAndHandlesDuplicates(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "graph.db"))
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
	s := Service{DB: d, MaxDepth: 16, MaxChars: 1000}
	now := time.Now().UTC()
	u1, created, err := s.Ingest(ctx, Incoming{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 1, UserID: 1, Kind: "text", Text: "/event перенеси органику", SentAt: now})
	if err != nil || !created {
		t.Fatal(err, created)
	}
	b1, _, err := s.StoreBot(ctx, BotMessage{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 2, Kind: "text", Text: "Какую?", ReplyToTelegramMessageID: intPtr(1), SentAt: now})
	if err != nil {
		t.Fatal(err)
	}
	u2, _, err := s.Ingest(ctx, Incoming{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 3, UserID: 1, Kind: "text", Text: "Вторую", ReplyToTelegramMessageID: intPtr(2), SentAt: now})
	if err != nil {
		t.Fatal(err)
	}
	b2, _, err := s.StoreBot(ctx, BotMessage{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 4, Kind: "text", Text: "Когда?", ReplyToTelegramMessageID: intPtr(3), SentAt: now})
	if err != nil {
		t.Fatal(err)
	}
	u3, _, err := s.Ingest(ctx, Incoming{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 5, UserID: 1, Kind: "text", Text: "Завтра в 16:30", ReplyToTelegramMessageID: intPtr(4), SentAt: now})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, created, err := s.Ingest(ctx, Incoming{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 5, UserID: 1, Kind: "text", Text: "duplicate", SentAt: now})
	if err != nil || created || duplicate.ID != u3.ID {
		t.Fatal(err, created, duplicate)
	}
	chain, err := s.BuildReplyChain(ctx, u3.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 5 || chain[0].ID != u1.ID || chain[1].ID != b1.ID || chain[2].ID != u2.ID || chain[3].ID != b2.ID || chain[4].ID != u3.ID {
		t.Fatalf("unexpected chain: %#v", chain)
	}
}

func TestReplyChainHasDepthAndCycleProtection(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d, MaxDepth: 2}
	now := time.Now()
	first, _, err := s.StoreBot(ctx, BotMessage{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 1, Kind: "text", Text: "one", SentAt: now})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.StoreBot(ctx, BotMessage{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 2, Kind: "text", Text: "two", ReplyToTelegramMessageID: intPtr(1), SentAt: now})
	if err != nil {
		t.Fatal(err)
	}
	third, _, err := s.StoreBot(ctx, BotMessage{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 3, Kind: "text", Text: "three", ReplyToTelegramMessageID: intPtr(2), SentAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`UPDATE messages SET reply_to_message_id=? WHERE id=?`, third.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	chain, err := s.BuildReplyChain(ctx, third.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 2 {
		t.Fatalf("depth protection failed: %#v", chain)
	}
	_ = second
}

func TestLateParentRepairsReplyEdge(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "late.db"))
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
	s := Service{DB: d}
	child, _, err := s.Ingest(ctx, Incoming{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 2, UserID: 1, Kind: "text", Text: "child", ReplyToTelegramMessageID: intPtr(1)})
	if err != nil {
		t.Fatal(err)
	}
	parent, _, err := s.Ingest(ctx, Incoming{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 1, UserID: 1, Kind: "text", Text: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	chain, err := s.BuildReplyChain(ctx, child.ID)
	if err != nil || len(chain) != 2 || chain[0].ID != parent.ID {
		t.Fatalf("late parent not repaired: %#v %v", chain, err)
	}
}

func TestFindConversationRootBeyondContextAndRejectsCycles(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "root.db"))
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
	s := Service{DB: d, MaxDepth: 4, MaxChars: 100}
	root, _, err := s.Ingest(ctx, Incoming{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 1, UserID: 1, Kind: "text", Text: "/event перенеси #42"})
	if err != nil {
		t.Fatal(err)
	}
	previous := root
	for i := 2; i <= 25; i++ {
		previous, _, err = s.Ingest(ctx, Incoming{GroupID: 1, TelegramChatID: -1, TelegramMessageID: i, UserID: 1, Kind: "text", Text: "reply", ReplyToTelegramMessageID: intPtr(i - 1)})
		if err != nil {
			t.Fatal(err)
		}
	}
	chain, err := s.BuildReplyChain(ctx, previous.ID)
	if err != nil || len(chain) > 4 {
		t.Fatalf("bounded context: %d %v", len(chain), err)
	}
	found, ok, err := s.FindConversationRoot(ctx, previous.ID, "/event", 128)
	if err != nil || !ok || found.ID != root.ID || found.Text != "/event перенеси #42" {
		t.Fatalf("root=%#v ok=%v err=%v", found, ok, err)
	}
	if _, err = d.Exec(`UPDATE messages SET reply_to_message_id=? WHERE id=?`, previous.ID, root.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`UPDATE messages SET text='not an event' WHERE id=?`, root.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.FindConversationRoot(ctx, previous.ID, "/event", 128); err != nil || ok {
		t.Fatalf("cycle root accepted: ok=%v err=%v", ok, err)
	}
}

func TestFindConversationRootRequiresExactCommand(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "command.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','',''); INSERT INTO users(id,telegram_user_id,created_at,updated_at) VALUES(1,1,'','')`); err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d}
	m, _, err := s.Ingest(ctx, Incoming{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 1, UserID: 1, Kind: "text", Text: "/eventual should not mutate"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.FindConversationRoot(ctx, m.ID, "/event", 128); err != nil || ok {
		t.Fatalf("non-command root accepted: ok=%v err=%v", ok, err)
	}
}

func TestReplyChainHonorsCharacterBudget(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "budget.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d, MaxDepth: 16, MaxChars: 4}
	_, _, err = s.StoreBot(ctx, BotMessage{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 1, Kind: "text", Text: "1234"})
	if err != nil {
		t.Fatal(err)
	}
	two, _, err := s.StoreBot(ctx, BotMessage{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 2, Kind: "text", Text: "56789", ReplyToTelegramMessageID: intPtr(1)})
	if err != nil {
		t.Fatal(err)
	}
	chain, err := s.BuildReplyChain(ctx, two.ID)
	if err != nil || len(chain) != 2 || chain[0].TelegramMessageID != 1 || chain[1].ID != two.ID {
		t.Fatalf("budget failed: %#v %v", chain, err)
	}
}

func TestUpdateBotText(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "update.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d}
	if _, _, err = s.StoreBot(ctx, BotMessage{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 7, Kind: "text", Text: "Думаю…"}); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdateBotText(ctx, -1, 7, "Готово."); err != nil {
		t.Fatal(err)
	}
	m, err := s.ByTelegramID(ctx, -1, 7)
	if err != nil || m.Text != "Готово." {
		t.Fatalf("message=%#v err=%v", m, err)
	}
}

func TestLiveMessageReplacesStaleConflictingTelegramMessageID(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "conflict.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','',''); INSERT INTO users(id,telegram_user_id,created_at,updated_at) VALUES(1,1,'','')`); err != nil {
		t.Fatal(err)
	}
	s := Service{DB: d}
	if _, _, err = s.Ingest(ctx, Incoming{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 7, UserID: 1, Kind: "text", Text: "user"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.StoreBot(ctx, BotMessage{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 7, Kind: "text", Text: "bot"}); err != nil {
		t.Fatal(err)
	}
	message, _, err := s.Ingest(ctx, Incoming{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 7, UserID: 1, Kind: "text", Text: "live user"})
	if err != nil || message.SenderType != SenderUser || message.Text != "live user" {
		t.Fatalf("message=%#v err=%v", message, err)
	}
}
func intPtr(v int) *int { return &v }

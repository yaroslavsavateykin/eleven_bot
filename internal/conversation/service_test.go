package conversation

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"group411/internal/db"
)

func TestPruneRemovesMessagesWithContextEntries(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "prune-context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err = database.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','',''); INSERT INTO users(id,telegram_user_id,created_at,updated_at) VALUES(1,1,'','')`); err != nil {
		t.Fatal(err)
	}
	s := Service{DB: database}
	m, _, err := s.Ingest(ctx, Incoming{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 1, UserID: 1, Kind: "text", Text: "прошлое сообщение", SentAt: time.Now().Add(-72 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, "UPDATE messages SET created_at=? WHERE id=?", time.Now().Add(-72*time.Hour).UTC().Format(time.RFC3339Nano), m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, "INSERT INTO text_context_entries(message_id,created_at) VALUES(?,?)", m.ID, time.Now().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err = s.Prune(ctx, time.Now().Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = database.QueryRow("SELECT count(*) FROM messages WHERE id=?", m.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("pruned message still present: %d %v", count, err)
	}
	if err = database.QueryRow("SELECT count(*) FROM text_context_entries WHERE message_id=?", m.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphaned context entry left: %d %v", count, err)
	}
}

func TestSearchIsGroupScopedAndUsesFTS(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, query := range []string{
		`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'one',-1,'UTC','one','',''),(2,'two',-2,'UTC','two','','')`,
		`INSERT INTO users(id,telegram_user_id,first_name,created_at,updated_at) VALUES(1,1,'Аня','','')`,
	} {
		if _, err := database.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	s := Service{DB: database}
	if _, _, err := s.Ingest(ctx, Incoming{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 1, UserID: 1, Kind: "text", Text: "По физхимии задали отчёт", SentAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Ingest(ctx, Incoming{GroupID: 2, TelegramChatID: -2, TelegramMessageID: 1, UserID: 1, Kind: "text", Text: "По физхимии другой отчёт", SentAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Search(ctx, 1, "физхимии отчёт", nil, nil, 10)
	if err != nil || len(got) != 1 || got[0].Text != "По физхимии задали отчёт" || got[0].Author != "Аня" {
		t.Fatalf("results=%#v err=%v", got, err)
	}
}

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

func TestRecentReturnsLastTenMessagesChronologically(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "recent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err = database.Exec(`INSERT INTO groups(id,name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(1,'test',-1,'UTC','test','',''); INSERT INTO users(id,telegram_user_id,created_at,updated_at) VALUES(1,1,'','')`); err != nil {
		t.Fatal(err)
	}
	s := Service{DB: database}
	now := time.Now().UTC()
	for i := 1; i <= 12; i++ {
		if _, _, err = s.Ingest(ctx, Incoming{GroupID: 1, TelegramChatID: -1, TelegramMessageID: i, UserID: 1, Kind: "text", Text: fmt.Sprintf("m%d", i), SentAt: now.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err = s.StoreBot(ctx, BotMessage{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 13, Kind: "text", Text: ProvisionalText, SentAt: now}); err != nil {
		t.Fatal(err)
	}
	messages, err := s.Recent(ctx, -1, 10)
	if err != nil || len(messages) != 10 || messages[0].Text != "m3" || messages[9].Text != "m12" {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
}

func TestRecentBoundsMessageTextBudget(t *testing.T) {
	messages := []Message{{Text: "one"}, {Text: "two"}, {Text: "three"}}
	got := boundRecent(messages, 8)
	if len(got) != 2 || got[0].Text != "two" || got[1].Text != "three" {
		t.Fatalf("messages=%#v", got)
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
	if _, _, err = s.StoreBot(ctx, BotMessage{GroupID: 1, TelegramChatID: -1, TelegramMessageID: 7, Kind: "text", Text: ProvisionalText}); err != nil {
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

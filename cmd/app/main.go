package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/go-chi/chi/v5"
	"group411/internal/admin"
	"group411/internal/agent"
	"group411/internal/ai"
	"group411/internal/api"
	"group411/internal/config"
	"group411/internal/conversation"
	"group411/internal/db"
	"group411/internal/schedule"
	telegramapp "group411/internal/telegram"
	webapp "group411/internal/web"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	_ "time/tzdata"
)

func main() {
	health := flag.Bool("healthcheck", false, "check local health endpoint")
	flag.Parse()
	if *health {
		r, e := http.Get("http://127.0.0.1:6767/healthz")
		if e != nil || r.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c, err := config.Load()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}
	if err = os.MkdirAll(filepath.Dir(c.DBPath), 0755); err != nil {
		slog.Error("data directory", "error", err)
		os.Exit(1)
	}
	d, err := db.Open(ctx, c.DBPath)
	if err != nil {
		slog.Error("database", "error", err)
		os.Exit(1)
	}
	defer d.Close()
	loc, _ := time.LoadLocation(c.Timezone)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	chatID := c.GroupChatID
	if chatID == 0 {
		chatID = -411
	}
	_, err = d.ExecContext(ctx, "INSERT INTO groups(name,telegram_chat_id,timezone,dashboard_slug,created_at,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(dashboard_slug) DO UPDATE SET name=excluded.name,telegram_chat_id=excluded.telegram_chat_id,timezone=excluded.timezone,updated_at=excluded.updated_at", c.GroupName, chatID, c.Timezone, "411", now, now)
	if err != nil {
		slog.Error("group", "error", err)
		os.Exit(1)
	}
	var groupID int64
	if err = d.QueryRowContext(ctx, "SELECT id FROM groups WHERE telegram_chat_id=?", chatID).Scan(&groupID); err != nil {
		slog.Error("group lookup", "error", err)
		os.Exit(1)
	}
	mtproto, err := telegramapp.NewMTProto(c.TelegramAPIID, c.TelegramAPIHash, c.Token, c.TelegramSessionPath)
	if err != nil {
		slog.Error("telegram MTProto", "error", err)
		os.Exit(1)
	}
	if mtproto != nil {
		if err := mtproto.Start(ctx); err != nil {
			slog.Error("telegram MTProto", "error", err)
			mtproto = nil
		}
	}
	semester := schedule.Semester{Start: c.Semester.Start, End: c.Semester.End}
	s := schedule.Service{DB: d, GroupID: groupID, TZ: loc, Semester: semester}
	conversationService := conversation.Service{DB: d, MaxDepth: 16, MaxChars: 3000}
	aiClient := ai.Service{AgentModel: c.AIAgentModel, StrictTools: c.AIStrictTools, DisableParallelTools: c.AIDisableParallelTools, ToolMode: c.AIToolMode, BaseURL: c.AIBaseURL, Key: c.AIKey, Model: c.AITextModel, VisionModel: c.AIVisionModel, STTBaseURL: c.AISTTBaseURL, STTKey: c.AISTTKey, STTModel: c.AISTTModel, WhisperBaseURL: c.WhisperBaseURL, WhisperAPIKey: c.WhisperAPIKey}
	botAgent := agent.Agent{
		Client: aiClient,
		Tools: []agent.Tool{
			agent.ScheduleQueryTool{Schedule: s},
			agent.GroupSearchTool{Conversation: conversationService, GroupID: groupID},
		},
		AdminTools: []agent.Tool{
			agent.ScheduleExcludeTool{Schedule: s, Announce: true},
			agent.ScheduleMutationTool{Schedule: s, Operation: "create", Announce: true},
			agent.ScheduleMutationTool{Schedule: s, Operation: "create_batch", Announce: true},
			agent.ScheduleMutationTool{Schedule: s, Operation: "update", Announce: true},
			agent.ScheduleMutationTool{Schedule: s, Operation: "update_batch", Announce: true},
			agent.ScheduleMutationTool{Schedule: s, Operation: "cancel", Announce: true},
		},
		MaxRounds:    10,
		ContextBytes: c.AIContextBytes,
		RunTimeout:   75 * time.Second,
	}
	w := webapp.New(s, c.GroupName, loc)
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK); fmt.Fprint(w, "ok\n") })
	r.Get("/", w.Index)
	r.Mount("/admin", admin.Handler{DB: d, GroupID: groupID, Password: c.AdminPassword}.Router())
	// Keep the established /api/v1 contract and retain /api temporarily for
	// existing private callers during the Hermes Hub rollout.
	apiHandler := api.API{DB: d, Schedule: s, Token: c.ExternalToken, BaseURL: c.BaseURL}.Router()
	r.Mount("/api/v1", apiHandler)
	r.Mount("/api", apiHandler)
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(webapp.Static())))
	srv := &http.Server{Addr: c.HTTPAddr, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	if err := telegramapp.Start(ctx, c.Token, telegramapp.Service{DB: d, Token: c.Token, MTProto: mtproto, Schedule: s, ChatID: c.GroupChatID, GroupID: groupID, AdminID: c.AdminTelegramUserID, Discovery: c.DiscoveryMode, BaseURL: c.BaseURL, GroupName: c.GroupName, GitHubRepository: c.GitHubRepository, AI: aiClient, Conversation: conversationService, Agent: botAgent}); err != nil {
		slog.Error("telegram", "error", err)
		slog.Warn("telegram disabled due to startup error, http will continue")
	}
	go worker(ctx, conversationService, c.Retention)
	go func() {
		slog.Info("http listening", "addr", c.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http", "error", err)
			stop()
		}
	}()
	<-ctx.Done()
	shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shut)
}
func worker(ctx context.Context, conversations conversation.Service, retention time.Duration) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		if err := conversations.Prune(ctx, time.Now().Add(-retention)); err != nil {
			slog.Error("message retention", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

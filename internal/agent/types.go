package agent

import (
	"context"
	"encoding/json"
	"time"

	"group411/internal/ai"
	"group411/internal/conversation"
)

type Client interface {
	Chat(context.Context, ai.ChatRequest) (ai.AssistantTurn, error)
}

type Conversation struct {
	RunID    string
	Messages []conversation.Message
	Now      time.Time
	Timezone string
	Mode     Mode
}

type Mode string

const (
	ModeGroup        Mode = "group"
	ModeGroupWrite   Mode = "group_write"
	ModeAdminPrivate Mode = "admin_private"
)

type ToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}
type ToolResult struct {
	Content string `json:"content"`
}
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	Execute(context.Context, json.RawMessage) (ToolResult, error)
}
type Result struct{ Reply string }

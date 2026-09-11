package agent

import (
	"context"
	"encoding/json"
	"time"

	"group411/internal/conversation"
)

type Client interface {
	Complete(context.Context, string, string) (string, error)
}

// StructuredClient requests a JSON object from transports that support the
// OpenAI-compatible response_format parameter.
type StructuredClient interface {
	CompleteJSON(context.Context, string, string) (string, error)
}

type Conversation struct {
	Messages []conversation.Message
	Now      time.Time
	Timezone string
	Mode     Mode
}

type Mode string

const (
	ModeGroup        Mode = "group"
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

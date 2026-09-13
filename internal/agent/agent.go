// Package agent implements a bounded, server-authorized tool runtime.
package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"group411/internal/ai"
	"group411/internal/schedule"
	"group411/prompts"
)

type Agent struct {
	Client            Client
	Tools, AdminTools []Tool
	MaxRounds         int // Tool rounds; one additional turn is allowed for the final answer.
	ContextBytes      int
	Progress          func(string)
}

func (a Agent) Run(ctx context.Context, input Conversation) (Result, error) {
	if a.Client == nil {
		return Result{}, fmt.Errorf("agent client is not configured")
	}
	if input.RunID == "" {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return Result{}, err
		}
		input.RunID = fmt.Sprintf("%x", id)
	}
	rounds := a.MaxRounds
	if rounds <= 0 {
		rounds = 10
	}
	budget := a.ContextBytes
	if budget <= 0 {
		budget = 128 << 10
	}
	tools := map[string]Tool{}
	request := ai.ChatRequest{RunID: input.RunID}
	for _, tool := range a.ToolsFor(input.Mode) {
		tools[tool.Name()] = tool
		request.Tools = append(request.Tools, ai.ToolDefinition{Name: tool.Name(), Description: tool.Description(), Parameters: tool.Schema()})
	}
	system := prompts.AgentSystem + "\n\n" + prompts.GroupContext + "\nСейчас: " + input.Now.In(timezone(input.Timezone)).Format(time.RFC3339) + "; timezone: " + input.Timezone
	if input.Mode == ModeAdminPrivate {
		system += "\nПриватный административный чат: изменения применяются сразу и попадают в очередь /sync."
	}
	request.Messages = []ai.ChatMessage{{Role: "system", Content: system}}
	for _, m := range input.Messages {
		role := "user"
		if m.SenderType == "bot" {
			role = "assistant"
		}
		request.Messages = append(request.Messages, ai.ChatMessage{Role: role, Content: m.Text})
	}
	// Only old input history is removable. Current/parent and every tool exchange
	// remain intact; if they cannot fit, fail explicitly rather than losing data.
	removable := make([]bool, len(request.Messages))
	for i := 1; i < len(request.Messages)-2; i++ {
		removable[i] = true
	}
	if len(input.Messages) > 0 {
		current := input.Messages[len(input.Messages)-1]
		if current.ReplyToMessageID != nil {
			for i, m := range input.Messages {
				if m.ID == *current.ReplyToMessageID {
					removable[i+1] = false
				}
			}
		}
	}
	called := map[string]bool{}
	for round := 0; round <= rounds; round++ {
		request.Round = round
		for contextSize(request) > budget {
			index := -1
			for i, canRemove := range removable {
				if canRemove {
					index = i
					break
				}
			}
			if index < 0 {
				break
			}
			request.Messages = append(request.Messages[:index], request.Messages[index+1:]...)
			removable = append(removable[:index], removable[index+1:]...)
		}
		if contextSize(request) > budget {
			slog.Warn("agent stopped", "run_id", input.RunID, "category", "context_limit")
			return Result{Reply: "Контекст запроса слишком большой. Отправьте более короткий запрос или меньший список событий."}, nil
		}
		turn, err := a.Client.Chat(ctx, request)
		if err != nil {
			slog.Error("agent provider failure", "run_id", input.RunID, "round", round, "error", err)
			return Result{}, err
		}
		slog.Info("agent turn", "run_id", input.RunID, "round", round, "model", turn.Model, "finish_reason", turn.FinishReason)
		if len(turn.ToolCalls) == 0 {
			if strings.TrimSpace(turn.Content) == "" {
				return Result{}, &ai.Error{Kind: "provider_protocol"}
			}
			slog.Info("agent final response", "run_id", input.RunID, "round", round, "success", true)
			return Result{Reply: strings.TrimSpace(turn.Content)}, nil
		}
		if round == rounds {
			break
		}
		ids := map[string]bool{}
		if len(turn.ToolCalls) > 100 {
			return Result{}, &ai.Error{Kind: "provider_protocol"}
		}
		for _, c := range turn.ToolCalls {
			if c.ID == "" || ids[c.ID] {
				return Result{}, &ai.Error{Kind: "provider_protocol"}
			}
			ids[c.ID] = true
		}
		request.Messages = append(request.Messages, ai.ChatMessage{Role: "assistant", Content: turn.Content, ToolCalls: turn.ToolCalls})
		for _, call := range turn.ToolCalls {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			start := time.Now()
			code, message := "", ""
			var data any
			tool, ok := tools[call.Name]
			var args any
			if !ok {
				code, message = "unknown_tool", "Инструмент недоступен в этом диалоге."
			} else if err := validateArguments(tool.Schema(), call.Arguments); err != nil {
				code, message = "invalid_arguments", err.Error()
			} else if json.Unmarshal(call.Arguments, &args) != nil || args == nil {
				code, message = "invalid_arguments", "Аргументы должны соответствовать схеме инструмента."
			} else {
				canonical, _ := json.Marshal(args)
				key := fmt.Sprintf("%x", sha256.Sum256(append([]byte(call.Name+"\x00"), canonical...)))
				if called[key] {
					code, message = "duplicate_call_in_run", "Этот вызов уже выполнялся. Используйте предыдущий результат."
				} else {
					called[key] = true
					if a.Progress != nil {
						a.Progress(toolProgress(call.Name))
					}
					result, err := tool.Execute(schedule.WithInvocation(ctx, input.RunID+"/"+key), call.Arguments)
					if err != nil {
						code, message = "tool_execution", "Не удалось выполнить инструмент. Проверьте аргументы и доступность события."
						var invalid *ArgumentError
						if errors.As(err, &invalid) {
							code, message = "invalid_arguments", invalid.Message
						}
					} else if json.Unmarshal([]byte(result.Content), &data) != nil {
						data = result.Content
					}
				}
			}
			envelope := map[string]any{"ok": code == ""}
			if code != "" {
				envelope["error"] = map[string]string{"code": code, "message": message}
			} else {
				envelope["data"] = data
			}
			raw, _ := json.Marshal(envelope)
			request.Messages = append(request.Messages, ai.ChatMessage{Role: "tool", ToolCallID: call.ID, Content: string(raw)})
			slog.Info("tool result", "run_id", input.RunID, "round", round, "tool", call.Name, "tool_call_id", call.ID, "argument_shape", argumentShape(call.Arguments), "duration", time.Since(start), "success", code == "", "category", code)
		}
	}
	slog.Warn("agent stopped", "run_id", input.RunID, "category", "round_limit")
	return Result{Reply: "Не удалось завершить проверку данных за один запрос. Уточните, пожалуйста, название или дату нужного занятия."}, nil
}
func contextSize(r ai.ChatRequest) int {
	size := 0
	for _, t := range r.Tools {
		size += len(t.Name) + len(t.Description) + len(t.Parameters) + 100
	}
	for _, m := range r.Messages {
		b, _ := json.Marshal(m.Content)
		size += len(b) + len(m.Role) + len(m.ToolCallID) + 100
		for _, c := range m.ToolCalls {
			b, _ := json.Marshal(string(c.Arguments))
			size += len(b) + len(c.Name) + len(c.ID) + 100
		}
	}
	return size
}
func (a Agent) ToolsFor(mode Mode) []Tool {
	tools := append([]Tool(nil), a.Tools...)
	if mode == ModeGroupWrite || mode == ModeAdminPrivate {
		tools = append(tools, a.AdminTools...)
	}
	return tools
}
func argumentShape(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return "invalid_json"
	}
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return "object:" + strings.Join(keys, ",")
	case []any:
		return fmt.Sprintf("array:%d", len(v))
	default:
		return fmt.Sprintf("%T", v)
	}
}
func toolProgress(name string) string {
	switch name {
	case "schedule_query":
		return "Ищу нужное занятие в расписании…"
	case "group_search":
		return "Проверяю сообщения группы…"
	case "schedule_create", "schedule_create_batch":
		return "Добавляю события в расписание…"
	case "schedule_update", "schedule_update_batch":
		return "Обновляю записи в расписании…"
	case "schedule_cancel":
		return "Отменяю событие в расписании…"
	default:
		return "Проверяю данные…"
	}
}
func timezone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

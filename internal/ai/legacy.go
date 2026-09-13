package ai

import (
	"context"
	"encoding/json"
	"fmt"
)

// legacyChat is explicit compatibility only. The runtime still receives turns,
// and the same registry and strict argument validation apply to both modes.
func (s Service) legacyChat(ctx context.Context, r ChatRequest) (AssistantTurn, error) {
	if s.Key == "" || s.Model == "" {
		return AssistantTurn{}, &Error{Kind: "provider_configuration"}
	}
	definitions, _ := json.Marshal(r.Tools)
	messages := []wireMessage{{Role: "system", Content: `Legacy compatibility protocol: return exactly one JSON object with reply (string) and tool_calls (array of objects with name and arguments object). Use an empty tool_calls array for the final answer. Available tool schemas: ` + string(definitions)}}
	for _, m := range r.Messages {
		switch {
		case m.Role == "tool":
			// Even the compatibility adapter does not promote tool data to user intent.
			messages = append(messages, wireMessage{Role: "assistant", Content: "Tool result data for " + m.ToolCallID + ": " + m.Content})
		case len(m.ToolCalls) > 0:
			raw, _ := json.Marshal(m.ToolCalls)
			messages = append(messages, wireMessage{Role: "assistant", Content: string(raw)})
		default:
			messages = append(messages, wireMessage{Role: m.Role, Content: m.Content})
		}
	}
	turn, err := s.requestChat(ctx, r, map[string]any{"model": s.Model, "messages": messages, "max_tokens": 4096})
	if err != nil {
		return turn, err
	}
	var response struct {
		Reply string `json:"reply"`
		Calls []struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"tool_calls"`
	}
	if json.Unmarshal([]byte(turn.Content), &response) != nil {
		return AssistantTurn{}, &Error{Kind: "provider_protocol"}
	}
	turn.Content = response.Reply
	turn.ToolCalls = nil
	for i, c := range response.Calls {
		if c.Name == "" {
			return AssistantTurn{}, &Error{Kind: "provider_protocol"}
		}
		turn.ToolCalls = append(turn.ToolCalls, ToolCall{ID: fmt.Sprintf("legacy_%d_%d", r.Round, i), Name: c.Name, Arguments: c.Arguments})
	}
	if turn.Content == "" && len(turn.ToolCalls) == 0 {
		return AssistantTurn{}, &Error{Kind: "provider_protocol"}
	}
	return turn, nil
}

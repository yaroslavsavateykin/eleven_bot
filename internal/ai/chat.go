package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
)

type ChatMessage struct {
	Role, Content string
	ToolCalls     []ToolCall
	ToolCallID    string
}
type ToolDefinition struct {
	Name, Description string
	Parameters        json.RawMessage
}
type ToolCall struct {
	ID, Name  string
	Arguments json.RawMessage
}
type ChatRequest struct {
	Messages []ChatMessage
	Tools    []ToolDefinition
	RunID    string
	Round    int
}
type AssistantTurn struct {
	Content             string
	ToolCalls           []ToolCall
	FinishReason, Model string
}
type Error struct {
	Kind   string
	Status int
	Reason string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s (HTTP %d; reason=%s)", e.Kind, e.Status, e.Reason)
}

type wireCall struct {
	Index    int    `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
type wireMessage struct {
	Role       string     `json:"role,omitempty"`
	Content    string     `json:"content"`
	ToolCalls  []wireCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

func toWire(m ChatMessage) wireMessage {
	w := wireMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
	for _, c := range m.ToolCalls {
		wc := wireCall{ID: c.ID, Type: "function"}
		wc.Function.Name = c.Name
		wc.Function.Arguments = string(c.Arguments)
		w.ToolCalls = append(w.ToolCalls, wc)
	}
	return w
}
func (s Service) Chat(ctx context.Context, r ChatRequest) (AssistantTurn, error) {
	if s.ToolMode == "legacy_json" {
		return s.legacyChat(ctx, r)
	}
	if s.ToolMode != "" && s.ToolMode != "native" {
		return AssistantTurn{}, &Error{Kind: "provider_configuration"}
	}
	if s.Key == "" || s.Model == "" {
		return AssistantTurn{}, &Error{Kind: "provider_configuration"}
	}
	messages := make([]wireMessage, 0, len(r.Messages))
	for _, m := range r.Messages {
		messages = append(messages, toWire(m))
	}
	tools := make([]any, 0, len(r.Tools))
	for _, t := range r.Tools {
		f := map[string]any{"name": t.Name, "description": t.Description, "parameters": t.Parameters}
		if s.StrictTools {
			parameters, err := strictParameters(t.Parameters)
			if err != nil {
				return AssistantTurn{}, err
			}
			f["parameters"] = parameters
			f["strict"] = true
		}
		tools = append(tools, map[string]any{"type": "function", "function": f})
	}
	model := s.Model
	if s.AgentModel != "" {
		model = s.AgentModel
	}
	payload := map[string]any{"model": model, "messages": messages, "max_tokens": 4096, "stream": false}
	if len(tools) > 0 {
		payload["tools"] = tools
		payload["tool_choice"] = "auto"
		if s.DisableParallelTools {
			payload["parallel_tool_calls"] = false
		}
	}
	return s.requestChat(ctx, r, payload)
}

func (s Service) requestChat(ctx context.Context, r ChatRequest, payload map[string]any) (AssistantTurn, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return AssistantTurn{}, &Error{Kind: "provider_protocol"}
	}
	for attempt := 0; attempt < 3; attempt++ {
		start := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL()+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return AssistantTurn{}, &Error{Kind: "provider_configuration"}
		}
		req.Header.Set("Authorization", "Bearer "+s.Key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := s.httpClient().Do(req)
		status := 0
		var data []byte
		if err == nil {
			status = resp.StatusCode
			data, err = io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
			resp.Body.Close()
		}
		slog.InfoContext(ctx, "model request", "run_id", r.RunID, "round", r.Round, "model", payload["model"], "retry_count", attempt, "provider_http_status", status, "duration", time.Since(start))
		if err == nil && status >= 200 && status < 300 {
			if len(data) > 2<<20 {
				return AssistantTurn{}, &Error{Kind: "provider_protocol", Status: status}
			}
			turn, decodeErr := decodeAssistant(data)
			if decodeErr != nil {
				reason := "malformed_completion"
				if e, ok := decodeErr.(*Error); ok && e.Reason != "" {
					reason = e.Reason
				}
				slog.WarnContext(ctx, "provider protocol rejected", "run_id", r.RunID, "round", r.Round, "reason", reason, "response_bytes", len(data), "content_type", resp.Header.Get("Content-Type"), "finish_reason", turn.FinishReason, "model", turn.Model)
				return turn, &Error{Kind: "provider_protocol", Status: status, Reason: reason}
			}
			if s.StrictTools && s.ToolMode != "legacy_json" {
				normalizeStrictCalls(&turn, r.Tools)
			}
			return turn, nil
		}
		if status != 0 && status != 429 && status < 500 {
			return AssistantTurn{}, &Error{Kind: "provider_configuration", Status: status}
		}
		if attempt == 2 {
			return AssistantTurn{}, &Error{Kind: "provider_transport", Status: status}
		}
		select {
		case <-ctx.Done():
			return AssistantTurn{}, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 300 * time.Millisecond):
		}
	}
	return AssistantTurn{}, &Error{Kind: "provider_transport"}
}

type completion struct {
	Model   string `json:"model"`
	Choices []struct {
		Index        int          `json:"index"`
		Message      *wireMessage `json:"message"`
		Delta        *wireMessage `json:"delta"`
		FinishReason string       `json:"finish_reason"`
	} `json:"choices"`
}

func decodeAssistant(data []byte) (AssistantTurn, error) {
	var out AssistantTurn
	calls := map[int]wireCall{}
	consume := func(data []byte) error {
		var envelope map[string]json.RawMessage
		if json.Unmarshal(data, &envelope) == nil && len(envelope["error"]) > 0 && string(envelope["error"]) != "null" {
			return &Error{Kind: "provider_protocol", Reason: "upstream_error_envelope"}
		}
		var c completion
		if json.Unmarshal(data, &c) != nil {
			return &Error{Kind: "provider_protocol", Reason: "invalid_completion_json"}
		}
		if c.Model != "" {
			out.Model = c.Model
		}
		for _, choice := range c.Choices {
			if choice.Index != 0 {
				continue
			}
			if choice.FinishReason != "" {
				out.FinishReason = choice.FinishReason
			}
			m := choice.Message
			delta := false
			if m == nil {
				m = choice.Delta
				delta = true
			}
			if m == nil {
				continue
			}
			out.Content += m.Content
			for i, part := range m.ToolCalls {
				index := i
				if delta {
					index = part.Index
				}
				old := calls[index]
				if part.ID != "" {
					if old.ID != "" && old.ID != part.ID {
						return &Error{Kind: "provider_protocol"}
					}
					old.ID = part.ID
				}
				if part.Type != "" {
					old.Type = part.Type
				}
				old.Function.Name += part.Function.Name
				old.Function.Arguments += part.Function.Arguments
				calls[index] = old
			}
		}
		return nil
	}
	// A leading complete JSON body may be followed by SSE-style "data:" lines
	// (e.g. 9router appends "data: [DONE]" to an otherwise non-streaming reply).
	// Decode the leading JSON value first, then any trailing "data:" chunks.
	if offset := jsonValueEnd(data); offset > 0 {
		if err := consume(data[:offset]); err != nil {
			return out, err
		}
		data = data[offset:]
	}
	if err := consumeSSE(data, consume); err != nil {
		return out, err
	}
	indexes := make([]int, 0, len(calls))
	for i := range calls {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	ids := map[string]bool{}
	for _, i := range indexes {
		c := calls[i]
		if c.ID == "" || c.Function.Name == "" || c.Type != "function" || ids[c.ID] {
			return out, &Error{Kind: "provider_protocol", Reason: "invalid_tool_call_identity"}
		}
		ids[c.ID] = true
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: c.ID, Name: c.Function.Name, Arguments: json.RawMessage(c.Function.Arguments)})
	}
	if out.FinishReason == "length" && len(out.ToolCalls) > 0 {
		return out, &Error{Kind: "provider_protocol", Reason: "completion_truncated"}
	}
	if strings.TrimSpace(out.Content) == "" && len(out.ToolCalls) == 0 {
		return out, &Error{Kind: "provider_protocol", Reason: "empty_assistant_turn"}
	}
	return out, nil
}

// jsonValueEnd returns the byte offset just past the first complete JSON value,
// or -1 if data does not begin with valid JSON. It tolerates trailing content.
func jsonValueEnd(data []byte) int {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(new(any)); err != nil {
		return -1
	}
	return int(dec.InputOffset())
}

// consumeSSE parses "data:" lines from a streaming-shaped body, skipping the
// "[DONE]" terminator and blank keep-alives. Returns nil when there are none.
func consumeSSE(body []byte, consume func([]byte) error) error {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		part := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if part == "" || part == "[DONE]" {
			continue
		}
		if err := consume([]byte(part)); err != nil {
			return err
		}
	}
	return scanner.Err()
}

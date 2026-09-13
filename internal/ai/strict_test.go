package ai

import (
	"encoding/json"
	"testing"
)

func TestStrictSchemaAdapter(t *testing.T) {
	canonical := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"location":{"type":["string","null"]}},"additionalProperties":false}`)
	raw, err := strictParameters(canonical)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Required   []string
		Properties map[string]struct{ Type []string }
	}
	if err = json.Unmarshal(raw, &schema); err != nil || len(schema.Required) != 2 || len(schema.Properties["query"].Type) != 2 {
		t.Fatalf("%s %v", raw, err)
	}
	turn := AssistantTurn{ToolCalls: []ToolCall{{Name: "tool", Arguments: json.RawMessage(`{"query":null,"location":null}`)}}}
	normalizeStrictCalls(&turn, []ToolDefinition{{Name: "tool", Parameters: canonical}})
	if string(turn.ToolCalls[0].Arguments) != `{"location":null}` {
		t.Fatalf("%s", turn.ToolCalls[0].Arguments)
	}
}

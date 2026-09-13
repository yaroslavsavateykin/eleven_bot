package ai

import (
	"encoding/json"
	"sort"
)

// OpenAI strict schemas require every property, including optional fields.
// Nullable transport fields represent omission; domain schemas stay canonical.
func strictParameters(raw json.RawMessage) (json.RawMessage, error) {
	var schema map[string]any
	if json.Unmarshal(raw, &schema) != nil {
		return nil, &Error{Kind: "provider_configuration"}
	}
	var walk func(map[string]any)
	walk = func(s map[string]any) {
		if props, ok := s["properties"].(map[string]any); ok {
			required := map[string]bool{}
			if fields, ok := s["required"].([]any); ok {
				for _, field := range fields {
					required[field.(string)] = true
				}
			}
			names := make([]string, 0, len(props))
			for name, value := range props {
				names = append(names, name)
				p := value.(map[string]any)
				if !required[name] {
					switch kind := p["type"].(type) {
					case string:
						p["type"] = []any{kind, "null"}
					case []any:
						has := false
						for _, k := range kind {
							has = has || k == "null"
						}
						if !has {
							p["type"] = append(kind, "null")
						}
					}
					if values, ok := p["enum"].([]any); ok {
						p["enum"] = append(values, nil)
					}
				}
				walk(p)
			}
			sort.Strings(names)
			s["required"] = names
			s["additionalProperties"] = false
		}
		if item, ok := s["items"].(map[string]any); ok {
			walk(item)
		}
	}
	walk(schema)
	encoded, err := json.Marshal(schema)
	return encoded, err
}

func normalizeStrictCalls(turn *AssistantTurn, tools []ToolDefinition) {
	for i := range turn.ToolCalls {
		call := &turn.ToolCalls[i]
		for _, tool := range tools {
			if tool.Name != call.Name {
				continue
			}
			var schema map[string]any
			var value any
			if json.Unmarshal(tool.Parameters, &schema) != nil || json.Unmarshal(call.Arguments, &value) != nil {
				continue
			}
			normalizeStrictValue(schema, value)
			call.Arguments, _ = json.Marshal(value)
		}
	}
}
func normalizeStrictValue(schema map[string]any, value any) {
	switch v := value.(type) {
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		required := map[string]bool{}
		fields, _ := schema["required"].([]any)
		for _, field := range fields {
			required[field.(string)] = true
		}
		for key, value := range v {
			p, ok := props[key].(map[string]any)
			if !ok {
				continue
			}
			nullable := false
			if kinds, ok := p["type"].([]any); ok {
				for _, k := range kinds {
					nullable = nullable || k == "null"
				}
			}
			if value == nil && !required[key] && !nullable {
				delete(v, key)
				continue
			}
			normalizeStrictValue(p, value)
		}
	case []any:
		if item, ok := schema["items"].(map[string]any); ok {
			for _, v := range v {
				normalizeStrictValue(item, v)
			}
		}
	}
}

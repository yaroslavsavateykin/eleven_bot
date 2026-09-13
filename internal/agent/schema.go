package agent

import "encoding/json"

// Schema completes shared nested contracts once, rather than maintaining
// inconsistent recurrence and patch constraints in each batch variant.
func (t ScheduleMutationTool) Schema() json.RawMessage {
	var schema map[string]any
	if json.Unmarshal(t.schemaBase(), &schema) != nil {
		panic("invalid built-in schema")
	}
	var recurrence map[string]any
	json.Unmarshal([]byte(`{"type":["object","null"],"required":["frequency"],"properties":{"frequency":{"type":"string","enum":["daily","weekly","monthly","yearly"]},"interval":{"type":"integer","minimum":1,"maximum":366},"weekdays":{"type":"array","maxItems":7,"items":{"type":"string","enum":["MO","TU","WE","TH","FR","SA","SU"]}},"count":{"type":["integer","null"],"minimum":1,"maximum":366},"until":{"type":["string","null"],"format":"date-time"}},"additionalProperties":false}`), &recurrence)
	var visit func(map[string]any)
	visit = func(s map[string]any) {
		props, _ := s["properties"].(map[string]any)
		for name, value := range props {
			p, ok := value.(map[string]any)
			if !ok {
				continue
			}
			switch name {
			case "recurrence":
				props[name] = recurrence
				continue
			case "target_id":
				p["minimum"] = 1
				p["maximum"] = 9007199254740991
			case "events", "updates":
				p["maxItems"] = 100
			case "changes":
				p["minProperties"] = 1
			case "title":
				p["minLength"] = 1
				p["maxLength"] = 500
			case "description":
				p["maxLength"] = 2000
			case "location":
				p["maxLength"] = 300
			case "starts_at", "ends_at":
				p["format"] = "date-time"
			case "tags":
				p["maxItems"] = 12
				if item, ok := p["items"].(map[string]any); ok {
					item["maxLength"] = 80
				}
			}
			visit(p)
		}
		if item, ok := s["items"].(map[string]any); ok {
			visit(item)
		}
	}
	visit(schema)
	raw, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return raw
}

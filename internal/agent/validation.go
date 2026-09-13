package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"time"
	"unicode/utf8"
)

// ArgumentError is safe to expose as tool data: it contains schema paths only.
type ArgumentError struct{ Message string }

func (e *ArgumentError) Error() string { return e.Message }

// validateArguments enforces the schema vocabulary used by this registry.
// Typed decoding and schedule domain validation remain authoritative afterwards.
func validateArguments(schema, raw json.RawMessage) error {
	if err := uniqueJSONKeys(raw); err != nil {
		return &ArgumentError{"Arguments contain duplicate fields or invalid JSON."}
	}
	var s map[string]any
	var value any
	if json.Unmarshal(schema, &s) != nil || json.Unmarshal(raw, &value) != nil {
		return &ArgumentError{"Arguments must be valid JSON matching the tool schema."}
	}
	return validateValue(s, value, "arguments")
}

func uniqueJSONKeys(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func() error
	value = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return fmt.Errorf("duplicate key")
				}
				seen[name] = true
				if err = value(); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err = value(); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("invalid delimiter")
		}
		_, err = d.Token()
		return err
	}
	return value()
}
func validateValue(s map[string]any, v any, path string) error {
	fail := func(rule string) error { return &ArgumentError{path + ": " + rule} }
	match := func(kind string) bool {
		switch kind {
		case "null":
			return v == nil
		case "object":
			_, ok := v.(map[string]any)
			return ok
		case "array":
			_, ok := v.([]any)
			return ok
		case "string":
			_, ok := v.(string)
			return ok
		case "boolean":
			_, ok := v.(bool)
			return ok
		case "integer":
			n, ok := v.(float64)
			return ok && math.Trunc(n) == n
		case "number":
			_, ok := v.(float64)
			return ok
		}
		return false
	}
	switch types := s["type"].(type) {
	case string:
		if !match(types) {
			return fail("expected " + types)
		}
	case []any:
		ok := false
		for _, kind := range types {
			if match(kind.(string)) {
				ok = true
			}
		}
		if !ok {
			return fail("unexpected value type")
		}
	}
	if choices, ok := s["enum"].([]any); ok {
		found := false
		encoded, _ := json.Marshal(v)
		for _, choice := range choices {
			candidate, _ := json.Marshal(choice)
			if string(candidate) == string(encoded) {
				found = true
			}
		}
		if !found {
			return fail("value is not in enum")
		}
	}
	switch v := v.(type) {
	case map[string]any:
		if n, ok := s["minProperties"].(float64); ok && len(v) < int(n) {
			return fail("too few properties")
		}
		props, _ := s["properties"].(map[string]any)
		required, _ := s["required"].([]any)
		for _, key := range required {
			if _, ok := v[key.(string)]; !ok {
				return fail("required field " + key.(string))
			}
		}
		for key, value := range v {
			if prop, ok := props[key].(map[string]any); ok {
				if err := validateValue(prop, value, path+"."+key); err != nil {
					return err
				}
			} else if s["additionalProperties"] == false {
				return fail("unknown field")
			}
		}
	case []any:
		if n, ok := s["minItems"].(float64); ok && len(v) < int(n) {
			return fail("too few items")
		}
		if n, ok := s["maxItems"].(float64); ok && len(v) > int(n) {
			return fail("too many items")
		}
		if item, ok := s["items"].(map[string]any); ok {
			for i, value := range v {
				if err := validateValue(item, value, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	case string:
		if n, ok := s["minLength"].(float64); ok && utf8.RuneCountInString(v) < int(n) {
			return fail("string too short")
		}
		if n, ok := s["maxLength"].(float64); ok && utf8.RuneCountInString(v) > int(n) {
			return fail("string too long")
		}
		if s["format"] == "date-time" {
			if _, err := time.Parse(time.RFC3339, v); err != nil {
				return fail("expected RFC3339 timestamp")
			}
		}
	case float64:
		if n, ok := s["minimum"].(float64); ok && v < n {
			return fail("below minimum")
		}
		if n, ok := s["maximum"].(float64); ok && v > n {
			return fail("above maximum")
		}
	}
	return nil
}

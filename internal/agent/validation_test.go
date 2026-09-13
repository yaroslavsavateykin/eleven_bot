package agent

import (
	"encoding/json"
	"testing"
)

func TestCanonicalMutationValidation(t *testing.T) {
	for _, tc := range []struct{ operation, raw string }{
		{"update", `{"target_id":1,"changes":{}}`},
		{"update", `{"target_id":0,"changes":{"title":"test"}}`},
		{"update", `{"target_id":1,"changes":{"starts_at":"tomorrow"}}`},
		{"create_batch", `{"events":[{"kind":"birthday","title":"test","starts_at":"2026-05-03T00:00:00Z","recurrence":{"frequency":"yearly","surprise":true}}]}`},
		{"create_batch", `{"events":[{"kind":"birthday","title":"test","starts_at":"2026-05-03T00:00:00Z","recurrence":{}}]}`},
		{"cancel", `{"target_id":1} {"target_id":2}`},
		{"cancel", `{"target_id":1,"target_id":2}`},
		{"cancel", `{"target_id":9007199254740992}`},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			if validateArguments((ScheduleMutationTool{Operation: tc.operation}).Schema(), json.RawMessage(tc.raw)) == nil {
				t.Fatal("invalid arguments accepted")
			}
		})
	}
}
func TestNullablePatchClearsOnlyExplicitFields(t *testing.T) {
	var input updateInput
	if err := decode(json.RawMessage(`{"target_id":1,"changes":{"location":null}}`), &input); err != nil {
		t.Fatal(err)
	}
	if !input.Changes.Location.Set || input.Changes.Location.Value != nil || input.Changes.Description.Set {
		t.Fatal("null and omission confused")
	}
}

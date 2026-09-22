package agent

import (
	"encoding/json"
	"testing"
)

func TestNullablePatchClearsOnlyExplicitFields(t *testing.T) {
	var input updateInput
	if err := decode(json.RawMessage(`{"target_id":1,"changes":{"location":null}}`), &input); err != nil {
		t.Fatal(err)
	}
	if !input.Changes.Location.Set || input.Changes.Location.Value != nil || input.Changes.Description.Set {
		t.Fatal("null and omission confused")
	}
}

func TestDecodeRejectsTrailingJSON(t *testing.T) {
	var input struct {
		TargetID int64 `json:"target_id"`
	}
	if err := decode(json.RawMessage(`{"target_id":1} {"target_id":2}`), &input); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	var input struct {
		TargetID int64 `json:"target_id"`
	}
	if err := decode(json.RawMessage(`{"target_id":1,"unexpected":true}`), &input); err == nil {
		t.Fatal("unknown field accepted")
	}
}

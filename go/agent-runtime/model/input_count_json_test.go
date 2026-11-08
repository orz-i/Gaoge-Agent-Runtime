package model_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestMarshalRequestForInputCountPreservesSignedRawJSONSpelling(t *testing.T) {
	arguments := json.RawMessage(`{"markup":"<>&","z":1,"a":2}`)
	request := model.Request{
		RunID: "test", Model: "openai/signed",
		Messages: []model.Message{{
			Role:      model.RoleAssistant,
			ToolCalls: []tools.Call{{ID: "call-1", ToolKey: "lookup", Name: "lookup", Arguments: arguments}},
		}},
	}
	ordinary, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	preserved, err := model.MarshalRequestForInputCount(request)
	if err != nil || !json.Valid(preserved) || bytes.HasSuffix(preserved, []byte("\n")) {
		t.Fatalf("typed count serialization invalid: err=%v", err)
	}
	// Default encoding/json HTML-escapes a nested RawMessage, which is
	// physically distinct from the Tool argument used in signed replay.
	if bytes.Equal(ordinary, preserved) || !bytes.Contains(ordinary, []byte(`\u003c`)) ||
		bytes.Contains(preserved, []byte(`\u003c`)) || !bytes.Contains(preserved, []byte("<>&")) {
		t.Fatal("native count request failed to preserve original raw Tool argument spelling")
	}
	var decoded model.Request
	if err = json.Unmarshal(preserved, &decoded); err != nil ||
		len(decoded.Messages) != 1 || len(decoded.Messages[0].ToolCalls) != 1 ||
		!bytes.Equal(decoded.Messages[0].ToolCalls[0].Arguments, arguments) {
		t.Fatal("tool argument JSON keys or HTML spelling changed")
	}
	next, err := model.MarshalRequestForInputCount(decoded)
	if err != nil || !bytes.Equal(next, preserved) {
		t.Fatalf("typed count serialization was not a deterministic round trip: %v", err)
	}
	invalid := model.CloneRequest(request)
	invalid.Messages[0].ToolCalls[0].Arguments = json.RawMessage(`{"invalid":`)
	if raw, err := model.MarshalRequestForInputCount(invalid); err == nil || raw != nil {
		t.Fatal("invalid JSON counted as valid input")
	}
}

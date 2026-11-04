package openai

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// A durable replay is provider evidence, not an independent right to rewrite
// the physical completion status of a previously authorized local Tool call.
// The stored transcript can remain unchanged while a forged replay status
// would otherwise be sent to the next physical Provider invocation.
func TestStoredToolReplayRequiresCompletedPhysicalStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		valid  bool
	}{
		{name: "legacy_omitted", status: "", valid: true},
		{name: "completed", status: `,"status":"completed"`, valid: true},
		{name: "in_progress", status: `,"status":"in_progress"`},
		{name: "incomplete", status: `,"status":"incomplete"`},
		{name: "failed", status: `,"status":"failed"`},
		{name: "explicit_null", status: `,"status":null`},
		{name: "wrong_type", status: `,"status":42`},
		{name: "empty", status: `,"status":""`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := `[{"type":"reasoning","id":"rs_1","encrypted_content":"opaque-fixture"},` +
				`{"type":"function_call","id":"fc_1","name":"lookup","call_id":"call_1","arguments":"{}"` + tc.status + `}]`
			msg := model.Message{
				Role:           model.RoleAssistant,
				ToolCalls:      []tools.Call{{ID: "call_1", ToolKey: "lookup", Name: "lookup", Arguments: json.RawMessage(`{}`)}},
				ProviderReplay: &model.ProviderReplay{Provider: "openai", Format: replayFormat, Data: json.RawMessage(data)},
			}
			_, err := verifiedReplay(msg)
			if tc.valid && err != nil {
				t.Fatalf("valid completed replay rejected: %v", err)
			}
			if !tc.valid && !errors.Is(err, ErrUnsupported) {
				t.Fatalf("unfinished persisted function_call accepted: err=%v replay=%s", err, data)
			}
		})
	}
}

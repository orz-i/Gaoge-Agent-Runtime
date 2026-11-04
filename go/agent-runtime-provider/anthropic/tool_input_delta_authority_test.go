package anthropic_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// A Tool-input JSON delta must not substitute a physical nonempty input
// value already delivered in content_block_start.
func TestAnthropicSSEToolInputDeltaCannotReplaceStartingArguments(t *testing.T) {
	for _, tc := range []struct {
		name, initial, delta string
		reject               bool
	}{
		{"replaced_tool_arguments", `{"x":1}`, `{"x":2}`, true},
		{"replaced_nested_arguments", `{"nested":{"approved":false}}`, `{"nested":{"approved":true}}`, true},
		{"null_initial_replaced", `null`, `{"x":2}`, true},
		{"empty_initial_then_delta", `{}`, `{"x":2}`, false},
		{"initial_args_no_delta", `{"x":1}`, ``, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, item := range []struct{ name, raw string }{
					{"message_start", `{"type":"message_start","message":{"type":"message","role":"assistant","id":"paid-tool","content":[],"usage":{"input_tokens":17,"output_tokens":0}}}`},
					{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call-1","name":"lookup","input":` + tc.initial + `}}`},
				} {
					_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", item.name, item.raw)
				}
				if tc.delta != "" {
					escaped, _ := json.Marshal(tc.delta)
					_, _ = fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":%s}}\n\n", escaped)
				}
				for _, item := range []struct{ name, raw string }{
					{"content_block_stop", `{"type":"content_block_stop","index":0}`},
					{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`},
					{"message_stop", `{"type":"message_stop"}`},
				} {
					_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", item.name, item.raw)
				}
			})
			req := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}}, Tools: []tools.Definition{definition}, RequireToolCall: true}
			res, err := c.GenerateStream(t.Context(), req, nil)
			if tc.reject {
				if err == nil || len(res.ToolCalls) != 0 {
					t.Errorf("SSE INITIAL TOOL INPUT REPLACED: response=%+v err=%v", res, err)
				}
			} else if err != nil || len(res.ToolCalls) != 1 {
				t.Fatalf("valid Tool input rejected: response=%+v err=%v", res, err)
			}
		})
	}
}

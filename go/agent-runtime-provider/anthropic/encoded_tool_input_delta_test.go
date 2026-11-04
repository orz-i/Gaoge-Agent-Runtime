package anthropic_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// An SSE partial_json string bypasses the outer event's object-key validation.
// The final reconstructed Tool input must obey the same strict JSON contract.
func TestAnthropicEncodedPartialJSONNeverContainsDuplicateKeys(t *testing.T) {
	for _, tc := range []struct {
		name, input  string
		valid, usage bool
	}{
		{"valid", `{"x":1}`, true, true},
		{"valid_siblings", `{"a":{"x":1},"b":{"x":2}}`, true, true},
		{"duplicate", `{"x":1,"x":2}`, false, true},
		{"nested_duplicate", `{"nested":{"x":1,"x":2}}`, false, true},
		{"escaped_duplicate", `{"x":1,"\u0078":2}`, false, true},
		{"unknown_usage", `{"x":1,"x":2}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, required := range []bool{false, true} {
				name := "optional"
				if required {
					name = "required"
				}
				t.Run(name, func(t *testing.T) {
					c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "text/event-stream")
						write := func(kind string, item any) {
							raw, _ := json.Marshal(item)
							_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, raw)
						}
						message := map[string]any{"type": "message", "role": "assistant", "id": "paid-delta"}
						if tc.usage {
							message["usage"] = map[string]any{"input_tokens": 17, "output_tokens": 0}
						}
						write("message_start", map[string]any{"type": "message_start", "message": message})
						write("content_block_start", map[string]any{
							"type": "content_block_start", "index": 0,
							"content_block": map[string]any{"type": "tool_use", "id": "call-1", "name": "lookup", "input": map[string]any{}},
						})
						write("content_block_delta", map[string]any{
							"type": "content_block_delta", "index": 0,
							"delta": map[string]any{"type": "input_json_delta", "partial_json": tc.input},
						})
						write("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
						delta := map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}}
						if tc.usage {
							delta["usage"] = map[string]any{"output_tokens": 5}
						}
						write("message_delta", delta)
						write("message_stop", map[string]any{"type": "message_stop"})
					})
					request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}},
						Tools: []tools.Definition{definition}, RequireToolCall: required}
					terminal := 0
					result, err := c.GenerateStream(t.Context(), request, func(e model.StreamEvent) error {
						if e.ResponseID != "" {
							terminal++
						}
						return nil
					})
					if tc.valid {
						if err != nil || result.ResponseID != "paid-delta" || len(result.ToolCalls) != 1 ||
							result.ToolCalls[0].ID != "call-1" || result.Usage == nil || result.ProviderReplay == nil || terminal != 1 {
							t.Fatalf("valid reconstructed Tool rejected: response=%+v err=%v terminal=%d", result, err, terminal)
						}
						return
					}
					if !errors.Is(err, anthropic.ErrUnsupported) || len(result.ToolCalls) != 0 ||
						result.Content != "" || result.ProviderReplay != nil || terminal != 0 {
						t.Fatalf("ambiguous encoded delta authorized Tool: response=%+v err=%v terminal=%d", result, err, terminal)
					}
					if tc.usage {
						if !model.IsResponseRejectedError(err) || model.ClassifiedErrorCode(err) != "model.post_generation_failed" ||
							result.ResponseID != "paid-delta" || result.Usage == nil ||
							result.Usage.InputTokens != 17 || result.Usage.OutputTokens != 5 {
							t.Fatalf("completed invalid Tool lost billing: response=%+v err=%v", result, err)
						}
					} else if model.IsResponseRejectedError(err) || result.ResponseID != "" || result.Usage != nil {
						t.Fatalf("unknown billing minted completion: response=%+v err=%v", result, err)
					}
				})
			}
		})
	}
}

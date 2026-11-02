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

// Both decoding paths have observed a completed Anthropic message with a
// cumulative Usage receipt. Reject invalid local Tool blocks without losing
// already-billed tokens or promoting an executable Tool intent.
func TestCompletedMalformedToolRetainsAnthropicUsageReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, toolID, toolName string
		knownUsage             bool
	}{
		{name: "missing_tool_id", toolName: "lookup", knownUsage: true},
		{name: "unknown_tool_name", toolID: "call-1", toolName: "foreign", knownUsage: true},
		{name: "missing_tool_id_unknown_usage", toolName: "lookup"},
		{name: "unknown_tool_name_unknown_usage", toolID: "call-1", toolName: "foreign"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, streaming := range []bool{false, true} {
				mode := "unary"
				if streaming {
					mode = "stream"
				}
				t.Run(mode, func(t *testing.T) {
					client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
						if !streaming {
							reply := map[string]any{
								"id": "paid-anthropic-invalid-tool", "stop_reason": "tool_use",
								"content": []any{map[string]any{"type": "tool_use", "id": tc.toolID,
									"name": tc.toolName, "input": map[string]any{}}},
							}
							if tc.knownUsage {
								reply["usage"] = map[string]any{"input_tokens": 17, "output_tokens": 5}
							}
							buf, _ := json.Marshal(reply)
							_, _ = w.Write(buf)
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						send := func(event string, value any) {
							b, _ := json.Marshal(value)
							_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
						}
						initial := map[string]any{"id": "paid-anthropic-invalid-tool"}
						if tc.knownUsage {
							initial["usage"] = map[string]any{"input_tokens": 17, "output_tokens": 0}
						}
						send("message_start", map[string]any{"type": "message_start", "message": initial})
						send("content_block_start", map[string]any{"type": "content_block_start", "index": 0,
							"content_block": map[string]any{"type": "tool_use", "id": tc.toolID, "name": tc.toolName, "input": map[string]any{}}})
						send("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
						delta := map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}}
						if tc.knownUsage {
							delta["usage"] = map[string]any{"output_tokens": 5}
						}
						send("message_delta", delta)
						send("message_stop", map[string]any{"type": "message_stop"})
					})
					request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("must call lookup")}},
						Tools: []tools.Definition{definition}, RequireToolCall: true}
					var result model.Response
					var err error
					terminal := 0
					if streaming {
						result, err = client.GenerateStream(t.Context(), request, func(event model.StreamEvent) error {
							if event.ResponseID != "" {
								terminal++
							}
							return nil
						})
					} else {
						result, err = client.Generate(t.Context(), request)
					}
					if !errors.Is(err, anthropic.ErrUnsupported) || len(result.ToolCalls) != 0 ||
						result.Content != "" || result.ProviderReplay != nil || terminal != 0 {
						t.Fatalf("unsafe malformed Tool response: result=%+v err=%v terminal=%d", result, err, terminal)
					}
					if !tc.knownUsage {
						if model.IsResponseRejectedError(err) || result.ResponseID != "" || result.Usage != nil {
							t.Fatalf("unverified Anthropic receipt: result=%+v err=%v", result, err)
						}
					} else if !model.IsResponseRejectedError(err) ||
						model.ClassifiedErrorCode(err) != "model.post_generation_failed" ||
						result.ResponseID != "paid-anthropic-invalid-tool" || result.Usage == nil ||
						result.Usage.InputTokens != 17 || result.Usage.OutputTokens != 5 {
						t.Fatalf("completed malformed Anthropic Tool lost paid receipt: result=%+v err=%v", result, err)
					}
				})
			}
		})
	}
}

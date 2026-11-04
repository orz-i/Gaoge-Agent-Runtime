package anthropic_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// An Anthropic message_delta changes only the final Message, never its
// completed content blocks. Late, overlapping, unindexed or reordered Tool
// blocks cannot become executable just because message_stop later arrives.
// Multiple consistent message_delta usage observations remain legal.
func TestAnthropicSSEFinalPhaseCannotAuthorizeLateOrMalformedTool(t *testing.T) {
	type frame struct{ event, json string }
	start := frame{"message_start", `{"type":"message_start","message":{"type":"message","role":"assistant","id":"msg-terminal-proof","usage":{"input_tokens":17,"output_tokens":0}}}`}
	startWithReason := frame{"message_start", `{"type":"message_start","message":{"type":"message","role":"assistant","id":"msg-terminal-proof","stop_reason":"tool_use","usage":{"input_tokens":17,"output_tokens":0}}}`}
	tool0 := frame{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool-0","name":"lookup","input":{}}}`}
	tool1 := frame{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tool-1","name":"lookup","input":{}}}`}
	stop0 := frame{"content_block_stop", `{"type":"content_block_stop","index":0}`}
	stop1 := frame{"content_block_stop", `{"type":"content_block_stop","index":1}`}
	delta5 := frame{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`}
	delta3 := frame{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`}
	delta3NoReason := frame{"message_delta", `{"type":"message_delta","delta":{},"usage":{"output_tokens":3}}`}
	deltaEnd := frame{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`}
	deltaNoStop := frame{"message_delta", `{"type":"message_delta","delta":{},"usage":{"output_tokens":5}}`}
	final := frame{"message_stop", `{"type":"message_stop"}`}
	ping := frame{"ping", `{"type":"ping"}`}
	cases := []struct {
		name     string
		frames   []frame
		valid    bool
		wantTool int
	}{
		{"normal_tool", []frame{start, tool0, stop0, delta5, final}, true, 1},
		{"normal_multiple_final_deltas", []frame{start, tool0, stop0, delta3, delta5, final}, true, 1},
		{"normal_usage_first_then_final_reason", []frame{start, tool0, stop0, delta3NoReason, delta5, final}, true, 1},
		{"normal_ping_between_phases", []frame{start, tool0, stop0, ping, delta5, ping, final}, true, 1},
		{"tool_started_after_message_delta", []frame{start, delta5, tool0, stop0, final}, false, 0},
		{"tool_stopped_after_message_delta", []frame{start, tool0, delta5, stop0, final}, false, 0},
		{"late_second_tool", []frame{start, tool0, stop0, delta5, tool1, stop1, final}, false, 0},
		{"message_delta_missing", []frame{startWithReason, tool0, stop0, final}, false, 0},
		{"message_delta_missing_stop_reason", []frame{startWithReason, tool0, stop0, deltaNoStop, final}, false, 0},
		{"cumulative_output_tokens_regress", []frame{start, tool0, stop0, delta5, delta3, final}, false, 0},
		{"contradictory_final_stop_reason", []frame{start, tool0, stop0, delta5, deltaEnd, final}, false, 0},
		{"out_of_order_block_indices", []frame{start, tool1, stop1, tool0, stop0, delta5, final}, false, 0},
		{"overlapping_content_blocks", []frame{start, tool0, tool1, stop0, stop1, delta5, final}, false, 0},
		{"start_missing_index", []frame{start, {"content_block_start", `{"type":"content_block_start","content_block":{"type":"tool_use","id":"tool-0","name":"lookup","input":{}}}`}, stop0, delta5, final}, false, 0},
		{"stop_missing_index", []frame{start, tool0, {"content_block_stop", `{"type":"content_block_stop"}`}, delta5, final}, false, 0},
		{"delta_missing_index", []frame{start, tool0, {"content_block_delta", `{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{}"}}`}, stop0, delta5, final}, false, 0},
		{"delta_null_index", []frame{start, tool0, {"content_block_delta", `{"type":"content_block_delta","index":null,"delta":{"type":"input_json_delta","partial_json":"{}"}}`}, stop0, delta5, final}, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, event := range tc.frames {
					_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.event, event.json)
				}
			})
			req := model.Request{
				Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("call lookup")}},
				Tools:    []tools.Definition{definition}, RequireToolCall: true,
			}
			terminal := 0
			result, err := client.GenerateStream(t.Context(), req, func(evt model.StreamEvent) error {
				if evt.ResponseID != "" {
					terminal++
				}
				return nil
			})
			if tc.valid {
				if err != nil || terminal != 1 || result.ResponseID != "msg-terminal-proof" ||
					result.Usage == nil || result.Usage.InputTokens != 17 || result.Usage.OutputTokens != 5 ||
					len(result.ToolCalls) != tc.wantTool || result.ToolCalls[0].ToolKey != definition.Key {
					t.Fatalf("valid Anthropic multi-delta SSE rejected: result=%+v err=%v terminal=%d", result, err, terminal)
				}
				return
			}
			if err == nil || model.IsResponseRejectedError(err) || terminal != 0 ||
				result.ResponseID != "" || result.Usage != nil || len(result.ToolCalls) != 0 ||
				result.Content != "" || result.ProviderReplay != nil {
				t.Fatalf("invalid Anthropic SSE phase authorized Tool or completion: result=%+v err=%v terminal=%d", result, err, terminal)
			}
		})
	}
}

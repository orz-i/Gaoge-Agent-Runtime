package anthropic_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/internal/providerhttp"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// An earlier semantic-only rejection must never disable the physical frame
// contract. The final message_stop/usage alone cannot certify billing when
// a later content_block_delta is not valid for its declared block type.
func TestRejectedAnthropicSSEStillValidatesKnownBlockDeltaFraming(t *testing.T) {
	for _, tc := range []struct {
		name, block, delta, secondInput, secondDelta string
		invalid                                      bool
	}{
		{
			name:  "valid_rejected_text_delta",
			block: `{"type":"text","text":"unrepresentable","citations":[{"type":"char_location","cited_text":"source"}]}`,
			delta: `{"type":"text_delta","text":"ignored"}`,
		},
		{
			name:    "input_delta_on_rejected_text",
			block:   `{"type":"text","text":"unrepresentable","citations":[{"type":"char_location","cited_text":"source"}]}`,
			delta:   `{"type":"input_json_delta","partial_json":"{}"}`,
			invalid: true,
		},
		{
			name:    "signature_delta_on_rejected_text",
			block:   `{"type":"text","text":"unrepresentable","citations":[{"type":"char_location","cited_text":"source"}]}`,
			delta:   `{"type":"signature_delta","signature":"forged"}`,
			invalid: true,
		},
		{
			name:    "unknown_delta_on_rejected_text",
			block:   `{"type":"text","text":"unrepresentable","citations":[{"type":"char_location","cited_text":"source"}]}`,
			delta:   `{"type":"foreign_delta","text":"forged"}`,
			invalid: true,
		},
		{
			name:    "text_delta_on_rejected_tool",
			block:   `{"type":"tool_use","id":" ","name":"lookup","input":{}}`,
			delta:   `{"type":"text_delta","text":"forged"}`,
			invalid: true,
		},
		{
			name:  "valid_rejected_tool_delta",
			block: `{"type":"tool_use","id":" ","name":"lookup","input":{}}`,
			delta: `{"type":"input_json_delta","partial_json":"{}"}`,
		},
		{
			name:        "later_tool_input_delta_substitutes_original_after_rejection",
			block:       `{"type":"text","text":"unrepresentable","citations":[{"type":"char_location","cited_text":"source"}]}`,
			delta:       `{"type":"text_delta","text":"suppressed"}`,
			secondInput: `{"x":1}`,
			secondDelta: `{"type":"input_json_delta","partial_json":"{\\\"x\\\":2}"}`,
			invalid:     true,
		},
		{
			name:        "valid_later_tool_input_delta_after_rejection",
			block:       `{"type":"text","text":"unrepresentable","citations":[{"type":"char_location","cited_text":"source"}]}`,
			delta:       `{"type":"text_delta","text":"suppressed"}`,
			secondInput: `{}`,
			secondDelta: `{"type":"input_json_delta","partial_json":"{\\\"x\\\":2}"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				send := func(event, data string) {
					_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
				}
				send("message_start", `{"type":"message_start","message":{"type":"message","role":"assistant","id":"paid-frame","usage":{"input_tokens":17,"output_tokens":0}}}`)
				send("content_block_start", `{"type":"content_block_start","index":0,"content_block":`+tc.block+`}`)
				send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":`+tc.delta+`}`)
				send("content_block_stop", `{"type":"content_block_stop","index":0}`)
				input := tc.secondInput
				if input == "" {
					input = `{}`
				}
				send("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call-1","name":"lookup","input":`+input+`}}`)
				if tc.secondDelta != "" {
					send("content_block_delta", `{"type":"content_block_delta","index":1,"delta":`+tc.secondDelta+`}`)
				}
				send("content_block_stop", `{"type":"content_block_stop","index":1}`)
				send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`)
				send("message_stop", `{"type":"message_stop"}`)
			})
			events := 0
			response, err := client.GenerateStream(t.Context(), model.Request{
				Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}},
				Tools:    []tools.Definition{definition}, RequireToolCall: true,
			}, func(event model.StreamEvent) error {
				if event.ResponseID != "" || event.Delta != "" {
					events++
				}
				return nil
			})
			if tc.invalid {
				if !errors.Is(err, providerhttp.ErrProtocol) || model.IsResponseRejectedError(err) ||
					response.ResponseID != "" || response.Usage != nil || response.ProviderReplay != nil ||
					len(response.ToolCalls) != 0 || events != 0 {
					t.Fatalf("broken rejected-content frame promoted billing: response=%+v err=%v events=%d", response, err, events)
				}
				return
			}
			if !errors.Is(err, anthropic.ErrUnsupported) || !model.IsResponseRejectedError(err) ||
				response.ResponseID != "paid-frame" || response.Usage == nil ||
				response.Usage.InputTokens != 17 || response.Usage.OutputTokens != 5 ||
				len(response.ToolCalls) != 0 || events != 0 {
				t.Fatalf("valid framed completed rejection lost paid receipt: response=%+v err=%v events=%d", response, err, events)
			}
		})
	}
}

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

// A previous rejected rich-text block cannot erase Thinking signature
// sequencing or authorize deltas on a redacted Thinking block. Future
// unsupported block types remain semantic rejections rather than tools.
func TestRejectedAnthropicSSEStillEnforcesThinkingDeltaSequence(t *testing.T) {
	for _, tc := range []struct {
		name, block string
		deltas      []string
		invalid     bool
	}{
		{
			name:   "valid_thinking_then_signature",
			block:  `{"type":"thinking","thinking":""}`,
			deltas: []string{`{"type":"thinking_delta","thinking":"opaque"}`, `{"type":"signature_delta","signature":"signed"}`},
		},
		{
			name:    "duplicate_thinking_signatures",
			block:   `{"type":"thinking","thinking":""}`,
			deltas:  []string{`{"type":"signature_delta","signature":"first"}`, `{"type":"signature_delta","signature":"second"}`},
			invalid: true,
		},
		{
			name:    "late_thinking_after_signature",
			block:   `{"type":"thinking","thinking":""}`,
			deltas:  []string{`{"type":"signature_delta","signature":"signed"}`, `{"type":"thinking_delta","thinking":"too late"}`},
			invalid: true,
		},
		{
			name:    "pre_signed_thinking_then_signature",
			block:   `{"type":"thinking","thinking":"","signature":"original"}`,
			deltas:  []string{`{"type":"signature_delta","signature":"second"}`},
			invalid: true,
		},
		{
			name:    "unsupported_pre_signed_thinking_then_signature",
			block:   `{"type":"thinking","thinking":"","signature":"original","unsupported":true}`,
			deltas:  []string{`{"type":"signature_delta","signature":"second"}`},
			invalid: true,
		},
		{
			name:    "unsupported_pre_signed_thinking_then_thinking",
			block:   `{"type":"thinking","thinking":"","signature":"original","unsupported":true}`,
			deltas:  []string{`{"type":"thinking_delta","thinking":"late"}`},
			invalid: true,
		},
		{
			name:    "redacted_thinking_delta_forbidden",
			block:   `{"type":"redacted_thinking","data":"opaque"}`,
			deltas:  []string{`{"type":"text_delta","text":"not allowed"}`},
			invalid: true,
		},
		{
			name:   "unsupported_future_block_stays_billed_rejection",
			block:  `{"type":"future_block","content":"not representable"}`,
			deltas: []string{`{"type":"future_delta","content":"not representable"}`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				send := func(kind, data string) { _, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data) }
				send("message_start", `{"type":"message_start","message":{"type":"message","role":"assistant","id":"paid-thinking-framing","usage":{"input_tokens":17,"output_tokens":0}}}`)
				send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"unrepresentable","citations":[{"type":"char_location","cited_text":"source"}]}}`)
				send("content_block_stop", `{"type":"content_block_stop","index":0}`)
				send("content_block_start", `{"type":"content_block_start","index":1,"content_block":`+tc.block+`}`)
				for _, delta := range tc.deltas {
					send("content_block_delta", `{"type":"content_block_delta","index":1,"delta":`+delta+`}`)
				}
				send("content_block_stop", `{"type":"content_block_stop","index":1}`)
				send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`)
				send("message_stop", `{"type":"message_stop"}`)
			})
			terminal := 0
			res, err := client.GenerateStream(t.Context(), model.Request{
				Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}},
				Tools:    []tools.Definition{definition}, RequireToolCall: true,
			}, func(event model.StreamEvent) error {
				if event.ResponseID != "" || event.Delta != "" {
					terminal++
				}
				return nil
			})
			if tc.invalid {
				if !errors.Is(err, providerhttp.ErrProtocol) || model.IsResponseRejectedError(err) ||
					res.ResponseID != "" || res.Usage != nil || res.ProviderReplay != nil ||
					len(res.ToolCalls) != 0 || terminal != 0 {
					t.Fatalf("broken Thinking delta minted receipt: res=%+v err=%v terminal=%d", res, err, terminal)
				}
				return
			}
			if !errors.Is(err, anthropic.ErrUnsupported) || !model.IsResponseRejectedError(err) ||
				res.ResponseID != "paid-thinking-framing" || res.Usage == nil ||
				res.Usage.InputTokens != 17 || res.Usage.OutputTokens != 5 ||
				len(res.ToolCalls) != 0 || terminal != 0 {
				t.Fatalf("valid rejected sequence lost paid receipt: res=%+v err=%v terminal=%d", res, err, terminal)
			}
		})
	}
}

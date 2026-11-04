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

// Missing mandatory local Tools do not erase completed provider billing,
// even when the response contained only unsupported rich text or a future
// content block. Known rejection receipts expose no transcript/replay data.
func TestRequiredToolFreeAnthropicSemanticRejectionRetainsOnlyPaidReceipt(t *testing.T) {
	const plain = `{"type":"text","text":"unauthorized answer"}`
	const rich = `{"type":"text","text":"unrepresentable","citations":[{"type":"char_location","cited_text":"source"}]}`
	const unknown = `{"type":"future_non_tool_block","text":"opaque"}`
	const refusal = `{"type":"text","text":"Cannot comply."}`
	tests := []struct {
		name, block, stop                         string
		required, knownUsage, accounted, accepted bool
	}{
		{name: "ordinary_text", block: plain, required: true, knownUsage: true, accounted: true},
		{name: "empty_text", block: `{"type":"text","text":""}`, required: true, knownUsage: true, accounted: true},
		{name: "rich_citations", block: rich, required: true, knownUsage: true, accounted: true},
		{name: "future_non_tool", block: unknown, required: true, knownUsage: true, accounted: true},
		{name: "unforced_rich_text", block: rich, knownUsage: true},
		{name: "rich_text_missing_usage", block: rich, required: true},
		{name: "incomplete_outer", block: rich, stop: "max_tokens", required: true, knownUsage: true},
		{name: "safety_refusal", block: refusal, stop: "refusal", required: true, knownUsage: true, accepted: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, streaming := range []bool{false, true} {
				mode := "unary"
				if streaming {
					mode = "sse"
				}
				t.Run(mode, func(t *testing.T) {
					client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
						var sent struct {
							Stream bool `json:"stream"`
							Choice struct {
								Type string `json:"type"`
							} `json:"tool_choice"`
						}
						if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
							t.Error(err)
						}
						wantChoice := ""
						if tc.required {
							wantChoice = "any"
						}
						if sent.Stream != streaming || sent.Choice.Type != wantChoice {
							t.Errorf("incorrect Tool force or streaming option: %+v", sent)
						}
						stop := tc.stop
						if stop == "" {
							stop = "end_turn"
						}
						if streaming {
							w.Header().Set("Content-Type", "text/event-stream")
							send := func(kind, data string) {
								_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data)
							}
							send("message_start", `{"type":"message_start","message":{"type":"message","role":"assistant","id":"paid-anthropic-no-tool","usage":{"input_tokens":17,"output_tokens":0}}}`)
							send("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":0,"content_block":%s}`, tc.block))
							send("content_block_stop", `{"type":"content_block_stop","index":0}`)
							usage := ""
							if tc.knownUsage {
								usage = `,"usage":{"output_tokens":5}`
							}
							send("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q}%s}`, stop, usage))
							send("message_stop", `{"type":"message_stop"}`)
						} else {
							usage := ""
							if tc.knownUsage {
								usage = `,"usage":{"input_tokens":17,"output_tokens":5}`
							}
							_, _ = fmt.Fprintf(w, `{"type":"message","role":"assistant","id":"paid-anthropic-no-tool","stop_reason":%q,"content":[%s]%s}`, stop, tc.block, usage)
						}
					})
					request := model.Request{
						Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("call lookup")}},
						Tools:    []tools.Definition{definition}, RequireToolCall: tc.required,
					}
					terminal := 0
					var result model.Response
					var err error
					if streaming {
						result, err = client.GenerateStream(t.Context(), request, func(evt model.StreamEvent) error {
							if evt.ResponseID != "" {
								terminal++
							}
							return nil
						})
					} else {
						result, err = client.Generate(t.Context(), request)
					}
					if tc.accepted {
						if err != nil || !result.Refused || result.Content != "Cannot comply." ||
							result.ResponseID != "paid-anthropic-no-tool" || result.Usage == nil ||
							(streaming && terminal != 1) {
							t.Fatalf("valid refusal denied: response=%+v err=%v terminal=%d", result, err, terminal)
						}
						return
					}
					if err == nil || terminal != 0 || len(result.ToolCalls) != 0 {
						t.Fatalf("missing required Tool incorrectly accepted: response=%+v err=%v terminal=%d", result, err, terminal)
					}
					if tc.accounted {
						if !errors.Is(err, anthropic.ErrUnsupported) || !model.IsResponseRejectedError(err) ||
							model.ClassifiedErrorCode(err) != "model.post_generation_failed" ||
							result.ResponseID != "paid-anthropic-no-tool" || result.Usage == nil ||
							result.Usage.InputTokens != 17 || result.Usage.OutputTokens != 5 ||
							result.Content != "" || result.Refused || result.ProviderReplay != nil {
							t.Fatalf("completed rejection lost sanitized billing: result=%+v err=%v", result, err)
						}
					} else if model.IsResponseRejectedError(err) || result.ResponseID != "" ||
						result.Usage != nil || result.Content != "" || result.ProviderReplay != nil {
						t.Fatalf("minted receipt for unverified/optional/incomplete response: result=%+v err=%v", result, err)
					}
				})
			}
		})
	}
}

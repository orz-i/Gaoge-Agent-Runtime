package openai_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// A mandatory Tool request can receive a physically complete paid response
// without a function_call. An unsupported text projection is still a known
// post-generation rejection, not an unaccounted pending invocation. Rejected
// successful projections must be sanitized too.
func TestRequiredToolFreeSemanticRejectionRetainsOnlyPaidReceipt(t *testing.T) {
	const text = `{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"unauthorized answer"}]}`
	const phased = `{"type":"message","role":"assistant","phase":"final_answer","status":"completed","content":[{"type":"output_text","text":"unauthorized phased answer"}]}`
	const unsupported = `{"type":"message","role":"assistant","status":"completed","content":[{"type":"unsupported_part","text":"not representable"}]}`
	const finalRefusal = `{"type":"message","role":"assistant","status":"completed","content":[{"type":"refusal","refusal":"Cannot comply."}]}`
	const usage = `,"usage":{"input_tokens":17,"output_tokens":5}`
	tests := []struct {
		name, output, status, usage   string
		required, accepted, accounted bool
	}{
		{name: "ordinary_text", output: "[" + text + "]", usage: usage, required: true, accounted: true},
		{name: "phased_final_text", output: "[" + phased + "]", usage: usage, required: true, accounted: true},
		{name: "unsupported_text_part", output: "[" + unsupported + "]", usage: usage, required: true, accounted: true},
		{name: "unknown_item", output: `[{"type":"future_non_tool_output"}]`, usage: usage, required: true, accounted: true},
		{name: "ill_typed_item", output: `[{"type":123}]`, usage: usage, required: true, accounted: true},
		{name: "empty_message", output: `[{"type":"message","content":[]}]`, usage: usage, required: true, accounted: true},
		{name: "unforced_unsupported_text", output: "[" + unsupported + "]", usage: usage},
		{name: "unsupported_missing_usage", output: "[" + unsupported + "]", required: true},
		{name: "unsupported_negative_usage", output: "[" + unsupported + "]", usage: `,"usage":{"input_tokens":-1,"output_tokens":5}`, required: true},
		{name: "incomplete_outer", output: "[" + unsupported + "]", status: "incomplete", usage: usage, required: true},
		{name: "terminal_safety_refusal", output: "[" + finalRefusal + "]", usage: usage, required: true, accepted: true},
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
							Stream bool   `json:"stream"`
							Choice string `json:"tool_choice"`
						}
						if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
							t.Error(err)
						}
						wantChoice := ""
						if tc.required {
							wantChoice = "required"
						}
						if sent.Stream != streaming || sent.Choice != wantChoice {
							t.Errorf("incorrect request choice/transport: %+v", sent)
						}
						status := tc.status
						if status == "" {
							status = "completed"
						}
						body := fmt.Sprintf(`{"object":"response","id":"paid-no-tool","status":%q,"output":%s%s}`, status, tc.output, tc.usage)
						if streaming {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", body)
						} else {
							_, _ = fmt.Fprint(w, body)
						}
					})
					request := model.Request{
						Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("call lookup")}},
						Tools:    []tools.Definition{definition}, RequireToolCall: tc.required,
					}
					terminal := 0
					var response model.Response
					var err error
					if streaming {
						response, err = client.GenerateStream(t.Context(), request, func(e model.StreamEvent) error {
							if e.ResponseID != "" {
								terminal++
							}
							return nil
						})
					} else {
						response, err = client.Generate(t.Context(), request)
					}
					if tc.accepted {
						if err != nil || !response.Refused || response.Content != "Cannot comply." ||
							response.ResponseID != "paid-no-tool" || response.Usage == nil ||
							(streaming && terminal != 1) {
							t.Fatalf("genuine terminal safety refusal rejected: response=%+v err=%v terminal=%d", response, err, terminal)
						}
						return
					}
					if err == nil || terminal != 0 || len(response.ToolCalls) != 0 {
						t.Fatalf("unapproved no-Tool response accepted: response=%+v err=%v terminal=%d", response, err, terminal)
					}
					if tc.accounted {
						if !errors.Is(err, openai.ErrUnsupported) || !model.IsResponseRejectedError(err) ||
							model.ClassifiedErrorCode(err) != "model.post_generation_failed" ||
							response.ResponseID != "paid-no-tool" || response.Usage == nil ||
							response.Usage.InputTokens != 17 || response.Usage.OutputTokens != 5 ||
							response.Content != "" || response.Refused || response.ProviderReplay != nil {
							t.Fatalf("known rejected completion not fully sanitized and billed: response=%+v err=%v", response, err)
						}
					} else if model.IsResponseRejectedError(err) || response.ResponseID != "" ||
						response.Usage != nil || response.Content != "" || response.ProviderReplay != nil {
						t.Fatalf("invented receipt for unknown/incomplete/optional failure: response=%+v err=%v", response, err)
					}
				})
			}
		})
	}
}

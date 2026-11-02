package anthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Some compatible-looking gateways silently ignore tool_choice=any. A final
// text-only reply must not satisfy a forced Tool request.
func TestForcedToolResponseMustActuallyContainToolCall(t *testing.T) {
	for _, tt := range []struct {
		name, content string
		required      bool
		refusal       bool
		toolCall      bool
		reject        bool
	}{
		{name: "forced_plain_text_fails_closed", content: "no tool executed", required: true, reject: true},
		{name: "forced_real_tool_call_unchanged", required: true, toolCall: true},
		{name: "unforced_plain_text_unchanged", content: "normal answer"},
		{name: "explicit_safety_refusal_preserved", content: "Cannot comply.", required: true, refusal: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				var sent struct {
					ToolChoice struct {
						Type string `json:"type"`
					} `json:"tool_choice"`
					Stream bool `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
					t.Error(err)
				}
				expectedChoice := ""
				if tt.required {
					expectedChoice = "any"
				}
				if sent.ToolChoice.Type != expectedChoice {
					t.Errorf("tool choice not sent: got=%q want=%q", sent.ToolChoice.Type, expectedChoice)
				}
				stopReason := "end_turn"
				if tt.toolCall {
					stopReason = "tool_use"
				}
				if tt.refusal {
					stopReason = "refusal"
				}
				if sent.Stream {
					w.Header().Set("Content-Type", "text/event-stream")
					write := func(event, data string) {
						_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
					}
					write("message_start", `{"type":"message_start","message":{"type":"message","role":"assistant","id":"msg-no-tool","usage":{"input_tokens":2,"output_tokens":0}}}`)
					if tt.toolCall {
						write("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call-1","name":"lookup","input":{}}}`)
						write("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"x\":1}"}}`)
					} else {
						write("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
						write("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, tt.content))
					}
					write("content_block_stop", `{"type":"content_block_stop","index":0}`)
					write("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":2}}`, stopReason))
					write("message_stop", `{"type":"message_stop"}`)
				} else {
					content := []any{map[string]any{"type": "text", "text": tt.content}}
					if tt.toolCall {
						content = []any{map[string]any{"type": "tool_use", "id": "call-1", "name": "lookup", "input": map[string]any{"x": 1}}}
					}
					response, err := json.Marshal(map[string]any{
						"type": "message", "role": "assistant", "id": "msg-no-tool", "stop_reason": stopReason,
						"content": content,
						"usage":   map[string]any{"input_tokens": 2, "output_tokens": 2},
					})
					if err != nil {
						t.Fatal(err)
					}
					_, _ = w.Write(response)
				}
			})
			request := model.Request{
				Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("call lookup")}},
				Tools:    []tools.Definition{definition}, RequireToolCall: tt.required,
			}
			for _, mode := range []string{"unary", "stream"} {
				t.Run(mode, func(t *testing.T) {
					var result model.Response
					var err error
					receipts := 0
					if mode == "stream" {
						result, err = client.GenerateStream(context.Background(), request, func(event model.StreamEvent) error {
							if event.ResponseID != "" {
								receipts++
							}
							return nil
						})
					} else {
						result, err = client.Generate(context.Background(), request)
					}
					if tt.reject {
						if !errors.Is(err, anthropic.ErrUnsupported) || !model.IsResponseRejectedError(err) ||
							result.ResponseID == "" || result.Usage == nil || len(result.ToolCalls) != 0 || receipts != 0 {
							t.Fatalf("tool-choice violation accepted or emitted terminal receipt: response=%+v err=%v receipts=%d", result, err, receipts)
						}
						return
					}
					if err != nil || result.Content != tt.content || result.Refused != tt.refusal ||
						(mode == "stream" && receipts != 1) ||
						(tt.toolCall && (len(result.ToolCalls) != 1 || result.ToolCalls[0].ToolKey != definition.Key)) {
						t.Fatalf("allowed reply lost: response=%+v err=%v receipts=%d", result, err, receipts)
					}
				})
			}
			if requests != 2 {
				t.Fatalf("unexpected requests=%d", requests)
			}
		})
	}
}

// Permanent regression: the provider has FINISHED and supplied cumulative token usage.
// Failing a required-Tool semantic contract must retain this known completion
// and its consumption evidence rather than look like a lost/unknown Model call.
func TestIndependentRequiredToolRejectionRetainsKnownUsageReceipt(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"type":"message","role":"assistant","id":"paid-anthropic-reply","stop_reason":"end_turn","content":[{"type":"text","text":"no tool call"}],"usage":{"input_tokens":17,"output_tokens":5}}`))
	})
	request := model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("must call lookup")}},
		Tools:    []tools.Definition{definition}, RequireToolCall: true,
	}
	response, err := client.Generate(context.Background(), request)
	if !errors.Is(err, anthropic.ErrUnsupported) || !model.IsResponseRejectedError(err) ||
		response.ResponseID != "paid-anthropic-reply" || response.Usage == nil ||
		response.Usage.InputTokens != 17 || response.Usage.OutputTokens != 5 || len(response.ToolCalls) != 0 {
		t.Fatalf("KNOWN PROVIDER COMPLETION/USAGE LOST ON REJECTED TOOL: response=%+v err=%v rejected=%v", response, err, model.IsResponseRejectedError(err))
	}
}

// Anthropic SSE initially reports input tokens, but that observation is not
// sufficient for final billing. Missing, incomplete or invalid cumulative
// Usage cannot authorize the completed-response rejection receipt.
func TestForcedToolUnknownUsageNeverMintsCompletedReceipt(t *testing.T) {
	for _, tc := range []struct{ name, usage, streamInput, streamOutput string }{
		{name: "missing"},
		{name: "partial", usage: `,"usage":{"input_tokens":17}`, streamInput: `,"usage":{"input_tokens":17}`},
		{name: "negative", usage: `,"usage":{"input_tokens":-1,"output_tokens":5}`, streamInput: `,"usage":{"input_tokens":-1,"output_tokens":0}`, streamOutput: `,"usage":{"output_tokens":5}`},
		{name: "overflow", usage: `,"usage":{"input_tokens":9223372036854775807,"output_tokens":1}`, streamInput: `,"usage":{"input_tokens":9223372036854775807,"output_tokens":0}`, streamOutput: `,"usage":{"output_tokens":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Stream bool `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if req.Stream {
					w.Header().Set("Content-Type", "text/event-stream")
					write := func(event, data string) { _, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data) }
					write("message_start", `{"type":"message_start","message":{"type":"message","role":"assistant","id":"unknown-usage"`+tc.streamInput+`}}`)
					write("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
					write("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"no required Tool"}}`)
					write("content_block_stop", `{"type":"content_block_stop","index":0}`)
					write("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}`+tc.streamOutput+`}`)
					write("message_stop", `{"type":"message_stop"}`)
				} else {
					_, _ = fmt.Fprint(w, `{"type":"message","role":"assistant","id":"unknown-usage","stop_reason":"end_turn","content":[{"type":"text","text":"no required Tool"}]`+tc.usage+`}`)
				}
			})
			req := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("must use lookup")}}, Tools: []tools.Definition{definition}, RequireToolCall: true}
			for _, mode := range []string{"unary", "stream"} {
				t.Run(mode, func(t *testing.T) {
					var response model.Response
					var err error
					terminal := 0
					if mode == "stream" {
						response, err = client.GenerateStream(t.Context(), req, func(event model.StreamEvent) error {
							if event.ResponseID != "" {
								terminal++
							}
							return nil
						})
					} else {
						response, err = client.Generate(t.Context(), req)
					}
					if !errors.Is(err, anthropic.ErrUnsupported) || model.IsResponseRejectedError(err) ||
						response.ResponseID != "" || response.Usage != nil || len(response.ToolCalls) != 0 || terminal != 0 {
						t.Fatalf("unknown usage was promoted to trusted completion: response=%+v err=%v terminal=%d", response, err, terminal)
					}
				})
			}
		})
	}
}

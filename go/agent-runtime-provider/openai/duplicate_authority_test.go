package openai_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/internal/providerhttp"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Nested and top-level duplicate object members are ambiguous physical
// identity/completion evidence, even when the second value appears valid.
// This applies to both JSON responses and SSE completed-event envelopes.
func TestRejectDuplicateOpenAIAuthorityKeysBeforeToolExecution(t *testing.T) {
	const output = `[{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{}"}]`
	const valid = `{"object":"response","id":"paid-tool","status":"completed","output":` + output + `,"usage":{"input_tokens":17,"output_tokens":5}}`
	cases := []struct {
		name, reply string
		valid       bool
	}{
		{name: "valid_control", reply: valid, valid: true},
		{name: "same_type_in_sibling_objects", reply: `{"object":"response","id":"paid-tool","status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"working"}]},{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{}"}],"usage":{"input_tokens":17,"output_tokens":5}}`, valid: true},
		{name: "folded_outer_object", reply: `{"Object":"chat.completion","object":"response","id":"paid-tool","status":"completed","output":` + output + `,"usage":{"input_tokens":17,"output_tokens":5}}`},
		{name: "folded_outer_status", reply: `{"object":"response","id":"paid-tool","Status":"incomplete","status":"completed","output":` + output + `,"usage":{"input_tokens":17,"output_tokens":5}}`},
		{name: "folded_item_type", reply: `{"object":"response","id":"paid-tool","status":"completed","output":[{"Type":"message","type":"function_call","call_id":"call-1","name":"lookup","arguments":"{}"}],"usage":{"input_tokens":17,"output_tokens":5}}`},
		{name: "folded_function_call_id", reply: `{"object":"response","id":"paid-tool","status":"completed","output":[{"type":"function_call","Call_ID":"foreign","call_id":"call-1","name":"lookup","arguments":"{}"}],"usage":{"input_tokens":17,"output_tokens":5}}`},
		{name: "folded_usage", reply: `{"object":"response","id":"paid-tool","status":"completed","output":` + output + `,"usage":{"Input_Tokens":1,"input_tokens":17,"output_tokens":5}}`},
		{name: "unicode_folded_status", reply: `{"object":"response","id":"paid-tool","ſtatus":"incomplete","status":"completed","output":` + output + `,"usage":{"input_tokens":17,"output_tokens":5}}`},
		{name: "duplicate_outer_object", reply: `{"object":"chat.completion","object":"response","id":"paid-tool","status":"completed","output":` + output + `,"usage":{"input_tokens":17,"output_tokens":5}}`},
		{name: "duplicate_outer_status", reply: `{"object":"response","id":"paid-tool","status":"incomplete","status":"completed","output":` + output + `,"usage":{"input_tokens":17,"output_tokens":5}}`},
		{name: "duplicate_call_id", reply: `{"object":"response","id":"paid-tool","status":"completed","output":[{"type":"function_call","call_id":"forged","call_id":"call-1","name":"lookup","arguments":"{}"}],"usage":{"input_tokens":17,"output_tokens":5}}`},
		{name: "duplicate_function_status", reply: `{"object":"response","id":"paid-tool","status":"completed","output":[{"type":"function_call","status":"incomplete","status":"completed","call_id":"call-1","name":"lookup","arguments":"{}"}],"usage":{"input_tokens":17,"output_tokens":5}}`},
		{name: "duplicate_usage", reply: `{"object":"response","id":"paid-tool","status":"completed","output":` + output + `,"usage":{"input_tokens":1,"input_tokens":17,"output_tokens":5}}`},
		{name: "escaped_duplicate_outer_object", reply: `{"object":"chat.completion","\u006fbject":"response","id":"paid-tool","status":"completed","output":` + output + `,"usage":{"input_tokens":17,"output_tokens":5}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				mode := "unary"
				if stream {
					mode = "sse"
				}
				t.Run(mode, func(t *testing.T) {
					client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", tc.reply)
						} else {
							_, _ = fmt.Fprint(w, tc.reply)
						}
					})
					request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}},
						Tools: []tools.Definition{definition}, RequireToolCall: true}
					terminal := 0
					var got model.Response
					var err error
					if stream {
						got, err = client.GenerateStream(t.Context(), request, func(e model.StreamEvent) error {
							if e.ResponseID != "" {
								terminal++
							}
							return nil
						})
					} else {
						got, err = client.Generate(t.Context(), request)
					}
					if tc.valid {
						if err != nil || len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != "call-1" ||
							got.ResponseID != "paid-tool" || got.Usage == nil || (stream && terminal != 1) {
							t.Fatalf("valid provider object rejected: response=%+v err=%v terminal=%d", got, err, terminal)
						}
						return
					}
					if !errors.Is(err, providerhttp.ErrProtocol) || got.ResponseID != "" || got.Usage != nil ||
						len(got.ToolCalls) != 0 || got.ProviderReplay != nil || terminal != 0 {
						t.Fatalf("duplicate authority certified local Tool or billing: response=%+v err=%v terminal=%d", got, err, terminal)
					}
				})
			}
		})
	}
	for _, tc := range []struct{ name, frameType string }{
		{"duplicate", `"type":"error","type":"response.completed"`},
		{"folded", `"Type":"error","type":"response.completed"`},
	} {
		t.Run(tc.name+"_sse_event_type", func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {%s,\"response\":%s}\n\n", tc.frameType, valid)
			})
			req := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}},
				Tools: []tools.Definition{definition}, RequireToolCall: true}
			got, err := client.GenerateStream(t.Context(), req, nil)
			if !errors.Is(err, providerhttp.ErrProtocol) || len(got.ToolCalls) != 0 || got.ResponseID != "" {
				t.Fatalf("ambiguous SSE event type promoted to completed Tool: response=%+v err=%v", got, err)
			}
		})
	}
}

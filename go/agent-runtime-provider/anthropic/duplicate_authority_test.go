package anthropic_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/internal/providerhttp"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Duplicate JSON owner/Tool fields must never be resolved by last-key-wins
// decoding to produce executable ToolCalls on either Messages wire path.
func TestRejectDuplicateAnthropicAuthorityKeysBeforeToolExecution(t *testing.T) {
	const unary = `{"type":"message","role":"assistant","id":"paid-message","stop_reason":"tool_use","content":[{"type":"tool_use","id":"call-1","name":"lookup","input":{}}],"usage":{"input_tokens":17,"output_tokens":5}}`
	const start = `{"type":"message_start","message":{"type":"message","role":"assistant","id":"paid-message","usage":{"input_tokens":17,"output_tokens":0}}}`
	const block = `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call-1","name":"lookup","input":{}}}`
	const delta = `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`
	type replacement struct{ old, new string }
	cases := []struct {
		name          string
		unary, stream replacement
		target        string
		valid         bool
	}{
		{name: "valid_control", valid: true},
		{name: "folded_role", unary: replacement{`"role":"assistant"`, `"Role":"user","role":"assistant"`},
			stream: replacement{`"role":"assistant"`, `"Role":"user","role":"assistant"`}, target: "start"},
		{name: "folded_stop_reason", unary: replacement{`"stop_reason":"tool_use"`, `"Stop_Reason":"max_tokens","stop_reason":"tool_use"`},
			stream: replacement{`"stop_reason":"tool_use"`, `"Stop_Reason":"max_tokens","stop_reason":"tool_use"`}, target: "delta"},
		{name: "unicode_folded_stop_reason", unary: replacement{`"stop_reason":"tool_use"`, `"ſtop_reason":"max_tokens","stop_reason":"tool_use"`},
			stream: replacement{`"stop_reason":"tool_use"`, `"ſtop_reason":"max_tokens","stop_reason":"tool_use"`}, target: "delta"},
		{name: "folded_tool_name", unary: replacement{`"name":"lookup"`, `"Name":"forged","name":"lookup"`},
			stream: replacement{`"name":"lookup"`, `"Name":"forged","name":"lookup"`}, target: "block"},
		{name: "folded_tool_id", unary: replacement{`"id":"call-1"`, `"ID":"forged","id":"call-1"`},
			stream: replacement{`"id":"call-1"`, `"ID":"forged","id":"call-1"`}, target: "block"},
		{name: "folded_usage", unary: replacement{`"input_tokens":17`, `"Input_Tokens":1,"input_tokens":17`},
			stream: replacement{`"input_tokens":17`, `"Input_Tokens":1,"input_tokens":17`}, target: "start"},
		{name: "duplicate_role", unary: replacement{`"role":"assistant"`, `"role":"user","role":"assistant"`},
			stream: replacement{`"role":"assistant"`, `"role":"user","role":"assistant"`}, target: "start"},
		{name: "duplicate_stop_reason", unary: replacement{`"stop_reason":"tool_use"`, `"stop_reason":"max_tokens","stop_reason":"tool_use"`},
			stream: replacement{`"stop_reason":"tool_use"`, `"stop_reason":"max_tokens","stop_reason":"tool_use"`}, target: "delta"},
		{name: "duplicate_tool_name", unary: replacement{`"name":"lookup"`, `"name":"forged","name":"lookup"`},
			stream: replacement{`"name":"lookup"`, `"name":"forged","name":"lookup"`}, target: "block"},
		{name: "duplicate_tool_id", unary: replacement{`"id":"call-1"`, `"id":"forged","id":"call-1"`},
			stream: replacement{`"id":"call-1"`, `"id":"forged","id":"call-1"`}, target: "block"},
		{name: "duplicate_input_members", unary: replacement{`"input":{}`, `"input":{"x":1,"x":2}`},
			stream: replacement{`"input":{}`, `"input":{"x":1,"x":2}`}, target: "block"},
		{name: "duplicate_usage", unary: replacement{`"input_tokens":17`, `"input_tokens":1,"input_tokens":17`},
			stream: replacement{`"input_tokens":17`, `"input_tokens":1,"input_tokens":17`}, target: "start"},
		{name: "escaped_duplicate_role", unary: replacement{`"role":"assistant"`, `"role":"user","\u0072ole":"assistant"`},
			stream: replacement{`"role":"assistant"`, `"role":"user","\u0072ole":"assistant"`}, target: "start"},
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
						if !stream {
							raw := unary
							if tc.unary.old != "" {
								raw = strings.Replace(raw, tc.unary.old, tc.unary.new, 1)
							}
							_, _ = fmt.Fprint(w, raw)
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						frames := []struct{ name, raw string }{
							{"message_start", start},
							{"content_block_start", block},
							{"content_block_stop", `{"type":"content_block_stop","index":0}`},
							{"message_delta", delta},
							{"message_stop", `{"type":"message_stop"}`},
						}
						for _, frame := range frames {
							if tc.stream.old != "" {
								switch tc.target {
								case "start":
									if frame.name == "message_start" {
										frame.raw = strings.Replace(frame.raw, tc.stream.old, tc.stream.new, 1)
									}
								case "block":
									if frame.name == "content_block_start" {
										frame.raw = strings.Replace(frame.raw, tc.stream.old, tc.stream.new, 1)
									}
								case "delta":
									if frame.name == "message_delta" {
										frame.raw = strings.Replace(frame.raw, tc.stream.old, tc.stream.new, 1)
									}
								}
							}
							_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", frame.name, frame.raw)
						}
					})
					req := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}},
						Tools: []tools.Definition{definition}, RequireToolCall: true}
					terminal := 0
					var got model.Response
					var err error
					if stream {
						got, err = client.GenerateStream(t.Context(), req, func(event model.StreamEvent) error {
							if event.ResponseID != "" {
								terminal++
							}
							return nil
						})
					} else {
						got, err = client.Generate(t.Context(), req)
					}
					if tc.valid {
						if err != nil || len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != "call-1" ||
							got.ResponseID != "paid-message" || got.Usage == nil || (stream && terminal != 1) {
							t.Fatalf("valid Anthropic response rejected: response=%+v err=%v terminal=%d", got, err, terminal)
						}
						return
					}
					if !errors.Is(err, providerhttp.ErrProtocol) || got.ResponseID != "" || got.Usage != nil ||
						len(got.ToolCalls) != 0 || got.ProviderReplay != nil || terminal != 0 {
						t.Fatalf("duplicate physical authority certified Tool or billing: response=%+v err=%v terminal=%d", got, err, terminal)
					}
				})
			}
		})
	}
}

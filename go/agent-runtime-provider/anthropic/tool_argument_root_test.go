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

// A permissive local schema must not override the physical Messages API
// tool_use.input wire contract: it must be a JSON object.
func TestToolInputRequiresPhysicalJSONObject(t *testing.T) {
	for _, tc := range []struct {
		name, input            string
		valid, optional, delta bool
	}{
		{name: "object", input: `{"x":1}`, valid: true},
		{name: "array", input: `[1]`},
		{name: "null", input: `null`},
		{name: "scalar", input: `true`},
		{name: "optional_array", input: `[1]`, optional: true},
		{name: "delta_array", input: `[1]`, delta: true},
	} {
		for _, streaming := range []bool{false, true} {
			if tc.delta && !streaming {
				continue
			}
			mode := "unary"
			if streaming {
				mode = "sse"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						send := func(kind, raw string) {
							_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, raw)
						}
						send("message_start", `{"type":"message_start","message":{"type":"message","role":"assistant","id":"paid-input","usage":{"input_tokens":17,"output_tokens":0}}}`)
						initialInput := tc.input
						if tc.delta {
							initialInput = `{}`
						}
						send("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool-1","name":"lookup","input":%s}}`, initialInput))
						if tc.delta {
							send("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%q}}`, tc.input))
						}
						send("content_block_stop", `{"type":"content_block_stop","index":0}`)
						send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`)
						send("message_stop", `{"type":"message_stop"}`)
					} else {
						_, _ = fmt.Fprintf(w, `{"type":"message","role":"assistant","id":"paid-input","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tool-1","name":"lookup","input":%s}],"usage":{"input_tokens":17,"output_tokens":5}}`, tc.input)
					}
				})
				request := model.Request{
					Messages:        []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}},
					Tools:           []tools.Definition{{Key: "lookup:v1", Name: "lookup", InputSchema: json.RawMessage(`{}`)}},
					RequireToolCall: !tc.optional,
				}
				var result model.Response
				var err error
				var terminal int
				if streaming {
					result, err = client.GenerateStream(context.Background(), request, func(e model.StreamEvent) error {
						if e.ResponseID != "" {
							terminal++
						}
						return nil
					})
				} else {
					result, err = client.Generate(context.Background(), request)
				}
				if tc.valid {
					if err != nil || len(result.ToolCalls) != 1 || result.ToolCalls[0].ToolKey != "lookup:v1" ||
						result.ResponseID != "paid-input" || result.Usage == nil || (streaming && terminal != 1) {
						t.Fatalf("valid object rejected: result=%+v err=%v terminal=%d", result, err, terminal)
					}
					return
				}
				if !errors.Is(err, anthropic.ErrUnsupported) || !model.IsResponseRejectedError(err) ||
					result.ResponseID != "paid-input" || result.Usage == nil || result.Usage.InputTokens != 17 ||
					result.Usage.OutputTokens != 5 || result.Content != "" || len(result.ToolCalls) != 0 ||
					result.ProviderReplay != nil || terminal != 0 {
					t.Fatalf("non-object physical tool input authorized: result=%+v err=%v terminal=%d", result, err, terminal)
				}
			})
		}
	}
}

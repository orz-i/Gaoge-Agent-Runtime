package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// A permissive local schema must not override the physical Responses
// function_call wire contract: arguments is an encoded JSON object.
func TestToolArgumentsRequirePhysicalJSONObject(t *testing.T) {
	for _, tc := range []struct {
		name, arguments string
		valid, optional bool
	}{
		{name: "object", arguments: `{"x":1}`, valid: true},
		{name: "array", arguments: `[1]`},
		{name: "null", arguments: `null`},
		{name: "scalar", arguments: `true`},
		{name: "optional_array", arguments: `[1]`, optional: true},
	} {
		for _, streaming := range []bool{false, true} {
			mode := "unary"
			if streaming {
				mode = "sse"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
					body := fmt.Sprintf(`{"object":"response","id":"paid-arguments","status":"completed","output":[{"type":"function_call","status":"completed","call_id":"tool-1","name":"lookup","arguments":%q}],"usage":{"input_tokens":17,"output_tokens":5}}`, tc.arguments)
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", body)
					} else {
						_, _ = fmt.Fprint(w, body)
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
						result.ResponseID != "paid-arguments" || result.Usage == nil || (streaming && terminal != 1) {
						t.Fatalf("valid object rejected: result=%+v err=%v terminal=%d", result, err, terminal)
					}
					return
				}
				if !errors.Is(err, openai.ErrUnsupported) || !model.IsResponseRejectedError(err) ||
					result.ResponseID != "paid-arguments" || result.Usage == nil || result.Usage.InputTokens != 17 ||
					result.Usage.OutputTokens != 5 || result.Content != "" || len(result.ToolCalls) != 0 ||
					result.ProviderReplay != nil || terminal != 0 {
					t.Fatalf("non-object physical tool arguments authorized: result=%+v err=%v terminal=%d", result, err, terminal)
				}
			})
		}
	}
}

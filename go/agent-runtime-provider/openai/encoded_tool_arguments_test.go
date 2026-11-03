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

// Encoded function_call.arguments must not gain different meanings under
// last-key-wins JSON decoding, irrespective of outer response/SSE framing.
func TestDoubleEncodedOpenAIToolArgumentsMustBeUnambiguous(t *testing.T) {
	for _, tc := range []struct {
		name, args   string
		valid, usage bool
	}{
		{"valid", `{"x":1}`, true, true},
		{"valid_siblings", `{"a":{"x":1},"b":{"x":2}}`, true, true},
		{"duplicate", `{"x":1,"x":2}`, false, true},
		{"nested_duplicate", `{"nested":{"x":1,"x":2}}`, false, true},
		{"escaped_duplicate", `{"x":1,"\u0078":2}`, false, true},
		{"unknown_usage", `{"x":1,"x":2}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, required := range []bool{false, true} {
				for _, stream := range []bool{false, true} {
					name := "optional_unary"
					if required {
						name = "required_unary"
					}
					if stream {
						name += "_sse"
					}
					t.Run(name, func(t *testing.T) {
						c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
							body := map[string]any{"object": "response", "id": "paid-encoded", "status": "completed",
								"output": []any{map[string]any{"type": "function_call", "status": "completed", "call_id": "call-1", "name": "lookup", "arguments": tc.args}}}
							if tc.usage {
								body["usage"] = map[string]any{"input_tokens": 17, "output_tokens": 5}
							}
							raw, _ := json.Marshal(body)
							if stream {
								w.Header().Set("Content-Type", "text/event-stream")
								_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", raw)
							} else {
								_, _ = w.Write(raw)
							}
						})
						req := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}},
							Tools: []tools.Definition{definition}, RequireToolCall: required}
						var result model.Response
						var err error
						terminal := 0
						if stream {
							result, err = c.GenerateStream(t.Context(), req, func(e model.StreamEvent) error {
								if e.ResponseID != "" {
									terminal++
								}
								return nil
							})
						} else {
							result, err = c.Generate(t.Context(), req)
						}
						if tc.valid {
							if err != nil || result.ResponseID != "paid-encoded" || len(result.ToolCalls) != 1 || result.Usage == nil || (stream && terminal != 1) {
								t.Fatalf("valid encoded Tool rejected: result=%+v err=%v terminal=%d", result, err, terminal)
							}
							return
						}
						if !errors.Is(err, openai.ErrUnsupported) || len(result.ToolCalls) != 0 || result.ProviderReplay != nil || terminal != 0 {
							t.Fatalf("ambiguous encoded Tool authorized: result=%+v err=%v terminal=%d", result, err, terminal)
						}
						if tc.usage {
							if !model.IsResponseRejectedError(err) || result.ResponseID != "paid-encoded" || result.Usage == nil ||
								result.Usage.InputTokens != 17 || result.Usage.OutputTokens != 5 || model.ClassifiedErrorCode(err) != "model.post_generation_failed" {
								t.Fatalf("lost known rejected billing: result=%+v err=%v", result, err)
							}
						} else if model.IsResponseRejectedError(err) || result.ResponseID != "" || result.Usage != nil {
							t.Fatalf("invented completion: result=%+v err=%v", result, err)
						}
					})
				}
			}
		})
	}
}

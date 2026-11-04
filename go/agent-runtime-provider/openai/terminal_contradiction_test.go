package openai_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// A completed physical Responses envelope cannot simultaneously advertise
// provider failure or truncation. Never let contradictory terminal evidence
// authorize a local function_call or certify paid completion.
func TestCompletedResponseRejectsContradictoryTerminalErrorEvidence(t *testing.T) {
	cases := []struct {
		name, extra string
		valid       bool
	}{
		{name: "fields_absent", valid: true},
		{name: "explicit_nulls", extra: `,"error":null,"incomplete_details":null`, valid: true},
		{name: "error_object", extra: `,"error":{"code":"server_error","message":"upstream failed"}`},
		{name: "incomplete_details", extra: `,"incomplete_details":{"reason":"max_output_tokens"}`},
		{name: "both_contradictory", extra: `,"error":{"code":"server_error"},"incomplete_details":{"reason":"max_output_tokens"}`},
		{name: "empty_error_object", extra: `,"error":{}`},
		{name: "empty_incomplete_details", extra: `,"incomplete_details":{}`},
		{name: "invalid_error_scalar", extra: `,"error":"failed"`},
		{name: "invalid_incomplete_scalar", extra: `,"incomplete_details":true`},
	}
	for _, tc := range cases {
		for _, streaming := range []bool{false, true} {
			mode := "unary"
			if streaming {
				mode = "stream"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
					reply := fmt.Sprintf(`{"object":"response","id":"physical-completion","status":"completed"%s,"output":[{"type":"function_call","status":"completed","call_id":"call-1","name":"lookup","arguments":"{}"}],"usage":{"input_tokens":17,"output_tokens":5}}`, tc.extra)
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", reply)
					} else {
						_, _ = fmt.Fprint(w, reply)
					}
				})
				request := model.Request{
					Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}},
					Tools:    []tools.Definition{definition}, RequireToolCall: true,
				}
				terminal := 0
				var result model.Response
				var err error
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
					if err != nil || result.ResponseID != "physical-completion" || result.Usage == nil ||
						len(result.ToolCalls) != 1 || result.ToolCalls[0].ToolKey != definition.Key ||
						(streaming && terminal != 1) {
						t.Fatalf("valid completion rejected: response=%+v err=%v terminal=%d", result, err, terminal)
					}
					return
				}
				if !errors.Is(err, openai.ErrUnsupported) || model.IsResponseRejectedError(err) ||
					result.ResponseID != "" || result.Usage != nil || len(result.ToolCalls) != 0 ||
					result.ProviderReplay != nil || result.Content != "" || terminal != 0 {
					t.Fatalf("contradictory response gained local Tool or billing authority: response=%+v err=%v terminal=%d", result, err, terminal)
				}
			})
		}
	}
}

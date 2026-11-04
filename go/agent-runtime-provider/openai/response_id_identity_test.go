package openai_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// A completed Provider response may authorize a Tool only with its exact
// physical response ID. Agent persists a trimmed ID, so any normalized
// identity would bind the eventual receipt to a different physical value.
func TestOpenAICompletedResponseIDMustBePhysicalAndStable(t *testing.T) {
	for _, tc := range []struct {
		name, id string
		valid    bool
	}{
		{name: "valid", id: "resp_1", valid: true},
		{name: "empty"},
		{name: "space", id: " "},
		{name: "tab", id: "\t"},
		{name: "leading_space", id: " resp_1"},
		{name: "trailing_space", id: "resp_1 "},
		{name: "nonbreaking_space", id: "\u00a0resp_1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				name := "unary"
				if stream {
					name = "sse"
				}
				t.Run(name, func(t *testing.T) {
					client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
						response := fmt.Sprintf(`{"object":"response","id":%q,"status":"completed","output":[{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{}"}],"usage":{"input_tokens":17,"output_tokens":5}}`, tc.id)
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
							return
						}
						_, _ = fmt.Fprint(w, response)
					})
					request := model.Request{
						Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}},
						Tools:    []tools.Definition{definition}, RequireToolCall: true,
					}
					terminal := 0
					var result model.Response
					var err error
					if stream {
						result, err = client.GenerateStream(t.Context(), request, func(event model.StreamEvent) error {
							if event.ResponseID != "" {
								terminal++
							}
							return nil
						})
					} else {
						result, err = client.Generate(t.Context(), request)
					}
					if tc.valid {
						if err != nil || len(result.ToolCalls) != 1 || result.ToolCalls[0].ID != "call-1" ||
							result.ResponseID != tc.id || result.Usage == nil || result.Usage.InputTokens != 17 ||
							result.Usage.OutputTokens != 5 || (stream && terminal != 1) {
							t.Fatalf("valid physical response ID rejected: result=%+v err=%v terminal=%d", result, err, terminal)
						}
						return
					}
					if !errors.Is(err, openai.ErrUnsupported) || len(result.ToolCalls) != 0 ||
						result.ResponseID != "" || result.Usage != nil || result.ProviderReplay != nil || terminal != 0 {
						t.Fatalf("unstable physical response ID authorized Tool or billing: result=%+v err=%v terminal=%d", result, err, terminal)
					}
				})
			}
		})
	}
}

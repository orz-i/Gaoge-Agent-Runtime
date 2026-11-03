package anthropic_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// The physical completed Anthropic message ID cannot be empty, blank or
// padded: Agent would normalize it before committing the ModelInvocation.
func TestAnthropicCompletedMessageIDMustBePhysicalAndStable(t *testing.T) {
	for _, tc := range []struct {
		name, id string
		valid    bool
	}{
		{name: "valid", id: "msg_1", valid: true},
		{name: "empty"},
		{name: "space", id: " "},
		{name: "tab", id: "\t"},
		{name: "leading_space", id: " msg_1"},
		{name: "trailing_space", id: "msg_1 "},
		{name: "nonbreaking_space", id: "\u00a0msg_1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				name := "unary"
				if stream {
					name = "sse"
				}
				t.Run(name, func(t *testing.T) {
					client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
						if !stream {
							_, _ = fmt.Fprintf(w, `{"type":"message","role":"assistant","id":%q,"stop_reason":"tool_use","content":[{"type":"tool_use","id":"tool-1","name":"lookup","input":{}}],"usage":{"input_tokens":17,"output_tokens":5}}`, tc.id)
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"role\":\"assistant\",\"id\":%q,\"usage\":{\"input_tokens\":17,\"output_tokens\":0}}}\n\n", tc.id)
						_, _ = fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool-1\",\"name\":\"lookup\",\"input\":{}}}\n\n")
						_, _ = fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
						_, _ = fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":5}}\n\n")
						_, _ = fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
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
						if err != nil || len(result.ToolCalls) != 1 || result.ToolCalls[0].ID != "tool-1" ||
							result.ResponseID != tc.id || result.Usage == nil || result.Usage.InputTokens != 17 ||
							result.Usage.OutputTokens != 5 || (stream && terminal != 1) {
							t.Fatalf("valid physical message ID rejected: result=%+v err=%v terminal=%d", result, err, terminal)
						}
						return
					}
					if !errors.Is(err, anthropic.ErrUnsupported) || len(result.ToolCalls) != 0 ||
						result.ResponseID != "" || result.Usage != nil || result.ProviderReplay != nil || terminal != 0 {
						t.Fatalf("unstable physical message ID authorized Tool or billing: result=%+v err=%v terminal=%d", result, err, terminal)
					}
				})
			}
		})
	}
}

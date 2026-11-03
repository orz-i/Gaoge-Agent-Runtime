package anthropic_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/internal/providerhttp"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// A message_start envelope describes an empty, not-yet-generated content
// collection. A gateway must not replace its physical Tool/text evidence
// with a different sequence of subsequent content_block_* events.
func TestAnthropicSSEMessageStartContentCannotBeSubstituted(t *testing.T) {
	for _, tc := range []struct {
		name, startingContent string
		valid                 bool
	}{
		{name: "absent_legacy", valid: true},
		{name: "empty_array", startingContent: "[]", valid: true},
		{name: "foreign_tool", startingContent: `[{"type":"tool_use","id":"foreign-call","name":"lookup","input":{}}]`},
		{name: "different_text", startingContent: `[{"type":"text","text":"unverified result"}]`},
		{name: "explicit_null", startingContent: "null"},
		{name: "object_not_array", startingContent: `{"type":"tool_use","id":"foreign-call"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				start := `"type":"message","role":"assistant","id":"msg-one","usage":{"input_tokens":17,"output_tokens":0}`
				if tc.startingContent != "" {
					start += `,"content":` + tc.startingContent
				}
				for _, frame := range []struct{ event, payload string }{
					{"message_start", `{"type":"message_start","message":{` + start + `}}`},
					{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call-one","name":"lookup","input":{}}}`},
					{"content_block_stop", `{"type":"content_block_stop","index":0}`},
					{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`},
					{"message_stop", `{"type":"message_stop"}`},
				} {
					_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", frame.event, frame.payload)
				}
			})
			req := model.Request{
				Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}},
				Tools:    []tools.Definition{definition}, RequireToolCall: true,
			}
			terminal := 0
			result, err := client.GenerateStream(t.Context(), req, func(event model.StreamEvent) error {
				if event.ResponseID != "" {
					terminal++
				}
				return nil
			})
			if tc.valid {
				if err != nil || len(result.ToolCalls) != 1 || result.ToolCalls[0].ID != "call-one" ||
					result.ResponseID != "msg-one" || result.Usage == nil || terminal != 1 {
					t.Fatalf("valid SSE start was rejected: response=%+v err=%v terminal=%d", result, err, terminal)
				}
				return
			}
			if !errors.Is(err, providerhttp.ErrProtocol) || len(result.ToolCalls) != 0 ||
				result.ResponseID != "" || result.Usage != nil || result.ProviderReplay != nil || terminal != 0 {
				t.Fatalf("conflicting SSE initial content authorized a Tool or billing: response=%+v err=%v terminal=%d", result, err, terminal)
			}
		})
	}
}

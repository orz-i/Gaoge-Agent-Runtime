package anthropic_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/internal/providerhttp"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Anthropic Messages owns local tool_use only when the completed outer
// provider response proves it is an assistant Message. Merely observing a
// familiar tool_use shape, valid ID and cumulative Usage is not authority.
func TestAnthropicMessageOwnerMustBeAssistantMessage(t *testing.T) {
	for _, tc := range []struct {
		name, identity string
		allowed        bool
	}{
		{"valid_assistant_message", `,"type":"message","role":"assistant"`, true},
		{"foreign_user_role", `,"type":"message","role":"user"`, false},
		{"foreign_system_role", `,"type":"message","role":"system"`, false},
		{"foreign_tool_role", `,"type":"message","role":"tool"`, false},
		{"foreign_type", `,"type":"tool_result","role":"assistant"`, false},
		{"missing_role", `,"type":"message"`, false},
		{"missing_type", `,"role":"assistant"`, false},
		{"missing_both", "", false},
		{"null_role", `,"type":"message","role":null`, false},
		{"null_type", `,"type":null,"role":"assistant"`, false},
		{"empty_role", `,"type":"message","role":""`, false},
		{"empty_type", `,"type":"","role":"assistant"`, false},
		{"nonstring_role", `,"type":"message","role":42`, false},
		{"nonstring_type", `,"type":42,"role":"assistant"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, streaming := range []bool{false, true} {
				mode := "unary"
				if streaming {
					mode = "sse"
				}
				t.Run(mode, func(t *testing.T) {
					client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
						var req struct {
							Stream     bool `json:"stream"`
							ToolChoice struct {
								Type string `json:"type"`
							} `json:"tool_choice"`
						}
						if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
							t.Error(err)
						}
						if req.Stream != streaming || req.ToolChoice.Type != "any" {
							t.Errorf("wrong transport/force: %+v", req)
						}
						if streaming {
							w.Header().Set("Content-Type", "text/event-stream")
							send := func(kind, raw string) { _, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, raw) }
							send("message_start", `{"type":"message_start","message":{"id":"untrusted-owner"`+tc.identity+`,"usage":{"input_tokens":17,"output_tokens":0}}}`)
							send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"foreign-local","name":"lookup","input":{}}}`)
							send("content_block_stop", `{"type":"content_block_stop","index":0}`)
							send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`)
							send("message_stop", `{"type":"message_stop"}`)
						} else {
							_, _ = fmt.Fprint(w, `{"id":"untrusted-owner"`+tc.identity+`,"stop_reason":"tool_use","usage":{"input_tokens":17,"output_tokens":5},"content":[{"type":"tool_use","id":"foreign-local","name":"lookup","input":{}}]}`)
						}
					})
					req := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}}, Tools: []tools.Definition{definition}, RequireToolCall: true}
					terminal := 0
					var response model.Response
					var err error
					if streaming {
						response, err = client.GenerateStream(t.Context(), req, func(evt model.StreamEvent) error {
							if evt.ResponseID != "" {
								terminal++
							}
							return nil
						})
					} else {
						response, err = client.Generate(t.Context(), req)
					}
					if tc.allowed {
						if err != nil || len(response.ToolCalls) != 1 || response.ToolCalls[0].ToolKey != definition.Key || response.ResponseID != "untrusted-owner" || response.Usage == nil || (streaming && terminal != 1) {
							t.Fatalf("valid assistant owner incorrectly rejected: response=%+v err=%v terminal=%d", response, err, terminal)
						}
						return
					}
					if err == nil || model.IsResponseRejectedError(err) ||
						response.ResponseID != "" || response.Usage != nil || response.Content != "" ||
						response.ProviderReplay != nil || len(response.ToolCalls) != 0 || terminal != 0 {
						t.Fatalf("foreign or unproven message authorized local Tool: response=%+v err=%v terminal=%d", response, err, terminal)
					}
					if !errors.Is(err, anthropic.ErrUnsupported) && !errors.Is(err, providerhttp.ErrProtocol) {
						t.Fatalf("unexpected owner rejection error: %v", err)
					}
				})
			}
		})
	}
}

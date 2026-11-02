package anthropic_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Optional tools require the same exact physical ID as mandatory tools.
// Unknown final Usage cannot certify any completed paid rejection.
func TestAnthropicPhysicalToolUseIDMustSurviveAgentNormalization(t *testing.T) {
	for _, tc := range []struct {
		name, id          string
		knownUsage, valid bool
	}{
		{name: "valid", id: "toolu_123", knownUsage: true, valid: true},
		{name: "whitespace_only", id: " ", knownUsage: true},
		{name: "tab_only", id: "\t", knownUsage: true},
		{name: "leading_space", id: " toolu_123", knownUsage: true},
		{name: "trailing_space", id: "toolu_123 ", knownUsage: true},
		{name: "nonbreaking_space", id: "\u00a0", knownUsage: true},
		{name: "missing_final_usage", id: " ", knownUsage: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, required := range []bool{false, true} {
				force := "optional"
				if required {
					force = "mandatory"
				}
				t.Run(force, func(t *testing.T) {
					for _, streaming := range []bool{false, true} {
						mode := "unary"
						if streaming {
							mode = "sse"
						}
						t.Run(mode, func(t *testing.T) {
							client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
								var sent struct {
									Stream     bool `json:"stream"`
									ToolChoice struct {
										Type string `json:"type"`
									} `json:"tool_choice"`
								}
								if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
									t.Error(err)
								}
								want := ""
								if required {
									want = "any"
								}
								if sent.Stream != streaming || sent.ToolChoice.Type != want {
									t.Errorf("wrong physical request: %+v", sent)
								}
								if streaming {
									w.Header().Set("Content-Type", "text/event-stream")
									write := func(kind string, frame any) {
										raw, err := json.Marshal(frame)
										if err != nil {
											t.Error(err)
											return
										}
										_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, raw)
									}
									write("message_start", map[string]any{
										"type": "message_start",
										"message": map[string]any{"type": "message", "role": "assistant", "id": "paid-tool-use",
											"usage": map[string]any{"input_tokens": 17, "output_tokens": 0}},
									})
									write("content_block_start", map[string]any{
										"type": "content_block_start", "index": 0,
										"content_block": map[string]any{"type": "tool_use", "id": tc.id, "name": "lookup", "input": map[string]any{}},
									})
									write("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
									delta := map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}}
									if tc.knownUsage {
										delta["usage"] = map[string]any{"output_tokens": 5}
									}
									write("message_delta", delta)
									write("message_stop", map[string]any{"type": "message_stop"})
								} else {
									payload := map[string]any{"type": "message", "role": "assistant", "id": "paid-tool-use", "stop_reason": "tool_use",
										"content": []any{map[string]any{"type": "tool_use", "id": tc.id, "name": "lookup", "input": map[string]any{}}}}
									if tc.knownUsage {
										payload["usage"] = map[string]any{"input_tokens": 17, "output_tokens": 5}
									}
									_ = json.NewEncoder(w).Encode(payload)
								}
							})
							request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("use lookup")}},
								Tools: []tools.Definition{definition}, RequireToolCall: required}
							terminal := 0
							var result model.Response
							var err error
							if streaming {
								result, err = client.GenerateStream(t.Context(), request, func(e model.StreamEvent) error {
									if e.ResponseID != "" {
										terminal++
									}
									return nil
								})
							} else {
								result, err = client.Generate(t.Context(), request)
							}
							if tc.valid {
								if err != nil || len(result.ToolCalls) != 1 || result.ToolCalls[0].ID != tc.id ||
									result.ToolCalls[0].ToolKey != definition.Key || result.ResponseID != "paid-tool-use" ||
									result.Usage == nil || (streaming && terminal != 1) {
									t.Fatalf("valid physical Anthropic Tool ID rejected: result=%+v err=%v terminal=%d", result, err, terminal)
								}
								return
							}
							if !errors.Is(err, anthropic.ErrUnsupported) || len(result.ToolCalls) != 0 ||
								result.Content != "" || result.ProviderReplay != nil || terminal != 0 {
								t.Fatalf("normalized physical Anthropic ID authorized Tool: result=%+v err=%v terminal=%d", result, err, terminal)
							}
							if tc.knownUsage {
								if !model.IsResponseRejectedError(err) || model.ClassifiedErrorCode(err) != "model.post_generation_failed" ||
									result.ResponseID != "paid-tool-use" || result.Usage == nil ||
									result.Usage.InputTokens != 17 || result.Usage.OutputTokens != 5 {
									t.Fatalf("lost paid Anthropic rejection receipt: result=%+v err=%v", result, err)
								}
							} else if model.IsResponseRejectedError(err) || result.ResponseID != "" || result.Usage != nil {
								t.Fatalf("fabricated unknown Anthropic Usage: result=%+v err=%v", result, err)
							}
						})
					}
				})
			}
		})
	}
}

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

// A physical Provider Call ID is an exact execution identity, not a human
// label. Agent trims IDs (or generates one if empty), so accepting any ID
// that normalization changes permits a Tool which the Provider did not
// identify with a stable original ID.
func TestOpenAIPhysicalFunctionCallIDMustSurviveAgentNormalization(t *testing.T) {
	for _, tc := range []struct {
		name, callID string
		valid, usage bool
	}{
		{name: "valid", callID: "call_123", valid: true, usage: true},
		{name: "whitespace_only", callID: " ", usage: true},
		{name: "tab_only", callID: "\t", usage: true},
		{name: "leading_space", callID: " call_123", usage: true},
		{name: "trailing_space", callID: "call_123 ", usage: true},
		{name: "nonbreaking_space", callID: "\u00a0", usage: true},
		{name: "missing_final_usage", callID: " ", usage: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, required := range []bool{false, true} {
				choice := "optional"
				if required {
					choice = "required"
				}
				t.Run(choice, func(t *testing.T) {
					for _, streaming := range []bool{false, true} {
						mode := "unary"
						if streaming {
							mode = "sse"
						}
						t.Run(mode, func(t *testing.T) {
							client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
								var outbound struct {
									Stream     bool   `json:"stream"`
									ToolChoice string `json:"tool_choice"`
								}
								if err := json.NewDecoder(r.Body).Decode(&outbound); err != nil {
									t.Error(err)
								}
								wantChoice := ""
								if required {
									wantChoice = "required"
								}
								if outbound.Stream != streaming || outbound.ToolChoice != wantChoice {
									t.Errorf("wrong outbound contract: %+v", outbound)
								}
								body := map[string]any{"object": "response", "id": "completed-physical-tool",
									"status": "completed", "output": []any{map[string]any{
										"type": "function_call", "status": "completed", "call_id": tc.callID,
										"name": "lookup", "arguments": `{}`,
									}}}
								if tc.usage {
									body["usage"] = map[string]any{"input_tokens": 17, "output_tokens": 5}
								}
								raw, err := json.Marshal(body)
								if err != nil {
									t.Fatal(err)
								}
								if streaming {
									w.Header().Set("Content-Type", "text/event-stream")
									_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", raw)
								} else {
									_, _ = w.Write(raw)
								}
							})
							req := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("call lookup")}},
								Tools: []tools.Definition{definition}, RequireToolCall: required}
							terminal := 0
							var result model.Response
							var err error
							if streaming {
								result, err = client.GenerateStream(t.Context(), req, func(evt model.StreamEvent) error {
									if evt.ResponseID != "" {
										terminal++
									}
									return nil
								})
							} else {
								result, err = client.Generate(t.Context(), req)
							}
							if tc.valid {
								if err != nil || len(result.ToolCalls) != 1 || result.ToolCalls[0].ID != tc.callID ||
									result.ToolCalls[0].ToolKey != definition.Key || result.Usage == nil ||
									result.ResponseID != "completed-physical-tool" || (streaming && terminal != 1) {
									t.Fatalf("valid physical ID rejected: result=%+v err=%v terminal=%d", result, err, terminal)
								}
								return
							}
							if !errors.Is(err, openai.ErrUnsupported) || result.Content != "" ||
								len(result.ToolCalls) != 0 || result.ProviderReplay != nil || terminal != 0 {
								t.Fatalf("normalized physical ID authorized Tool: result=%+v err=%v terminal=%d", result, err, terminal)
							}
							if tc.usage {
								if !model.IsResponseRejectedError(err) || model.ClassifiedErrorCode(err) != "model.post_generation_failed" ||
									result.ResponseID != "completed-physical-tool" || result.Usage == nil ||
									result.Usage.InputTokens != 17 || result.Usage.OutputTokens != 5 {
									t.Fatalf("known paid invalid Tool ID lost billing: result=%+v err=%v", result, err)
								}
							} else if model.IsResponseRejectedError(err) || result.ResponseID != "" || result.Usage != nil {
								t.Fatalf("invented final billing for missing Usage: result=%+v err=%v", result, err)
							}
						})
					}
				})
			}
		})
	}
}

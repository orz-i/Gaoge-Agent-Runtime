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

// A completed outer response does not certify an unfinished physical
// reasoning item. Encrypted content is replay evidence, never execution
// authority for its sibling local function_call.
func TestFinalReasoningItemMustBeCompletedBeforeLocalTool(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     any
		present    bool
		valid      bool
		knownUsage bool
	}{
		{name: "legacy_omitted_status", valid: true, knownUsage: true},
		{name: "completed", present: true, status: "completed", valid: true, knownUsage: true},
		{name: "in_progress", present: true, status: "in_progress", knownUsage: true},
		{name: "incomplete", present: true, status: "incomplete", knownUsage: true},
		{name: "failed", present: true, status: "failed", knownUsage: true},
		{name: "null", present: true, knownUsage: true},
		{name: "empty", present: true, status: "", knownUsage: true},
		{name: "wrong_type", present: true, status: 42, knownUsage: true},
		{name: "unknown", present: true, status: "custom", knownUsage: true},
		{name: "unknown_final_usage", present: true, status: "in_progress"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, forced := range []bool{false, true} {
				choice := "optional"
				if forced {
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
								var request struct {
									Stream     bool   `json:"stream"`
									ToolChoice string `json:"tool_choice"`
								}
								if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
									t.Error(err)
								}
								expected := ""
								if forced {
									expected = "required"
								}
								if request.Stream != streaming || request.ToolChoice != expected {
									t.Errorf("unexpected physical Tool request: %+v", request)
								}
								reasoning := map[string]any{
									"type": "reasoning", "id": "rs_1", "summary": []any{}, "encrypted_content": "sealed-reasoning",
								}
								if tc.present {
									reasoning["status"] = tc.status
								}
								body := map[string]any{
									"object": "response", "id": "paid-reasoning", "status": "completed",
									"output": []any{reasoning, map[string]any{
										"type": "function_call", "status": "completed",
										"call_id": "call-1", "name": "lookup", "arguments": `{}`,
									}},
								}
								if tc.knownUsage {
									body["usage"] = map[string]any{"input_tokens": 17, "output_tokens": 5}
								}
								encoded, err := json.Marshal(body)
								if err != nil {
									t.Fatal(err)
								}
								if streaming {
									w.Header().Set("Content-Type", "text/event-stream")
									_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", encoded)
								} else {
									_, _ = w.Write(encoded)
								}
							})
							request := model.Request{
								Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}},
								Tools:    []tools.Definition{definition}, RequireToolCall: forced,
							}
							terminal := 0
							var response model.Response
							var err error
							if streaming {
								response, err = client.GenerateStream(t.Context(), request, func(event model.StreamEvent) error {
									if event.ResponseID != "" {
										terminal++
									}
									return nil
								})
							} else {
								response, err = client.Generate(t.Context(), request)
							}
							if tc.valid {
								if err != nil || len(response.ToolCalls) != 1 ||
									response.ToolCalls[0].ID != "call-1" || response.ToolCalls[0].ToolKey != definition.Key ||
									response.ResponseID != "paid-reasoning" || response.Usage == nil ||
									response.ProviderReplay == nil || (streaming && terminal != 1) {
									t.Fatalf("valid physical reasoning/tool response incorrectly rejected: response=%+v err=%v terminal=%d", response, err, terminal)
								}
								return
							}
							if !errors.Is(err, openai.ErrUnsupported) || len(response.ToolCalls) != 0 ||
								response.Content != "" || response.Refused || response.ProviderReplay != nil || terminal != 0 {
								t.Fatalf("unfinished reasoning authorized local Tool: response=%+v err=%v terminal=%d", response, err, terminal)
							}
							if tc.knownUsage {
								if !model.IsResponseRejectedError(err) || model.ClassifiedErrorCode(err) != "model.post_generation_failed" ||
									response.ResponseID != "paid-reasoning" || response.Usage == nil ||
									response.Usage.InputTokens != 17 || response.Usage.OutputTokens != 5 {
									t.Fatalf("known completed rejected Tool lost paid receipt: response=%+v err=%v", response, err)
								}
							} else if model.IsResponseRejectedError(err) || response.ResponseID != "" || response.Usage != nil {
								t.Fatalf("unknown Usage minted completed response receipt: response=%+v err=%v", response, err)
							}
						})
					}
				})
			}
		})
	}
}

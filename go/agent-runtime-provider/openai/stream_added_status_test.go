package openai_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/internal/providerhttp"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Added items are not completion receipts, but an explicit incomplete or
// malformed physical status cannot be erased by a later done/final frame.
func TestStreamAddedItemStatusCannotBeRewritten(t *testing.T) {
	const finalTool = `{"type":"function_call","id":"fc_1","status":"completed","call_id":"call_1","name":"lookup","arguments":"{}"}`
	for _, item := range []struct {
		name, added, final string
	}{
		{"function_call", `"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":""`, finalTool},
		{"message", `"type":"message","id":"msg_1","role":"assistant","content":[]`, `{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Working"}]}`},
		{"reasoning", `"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"partial"`, `{"type":"reasoning","id":"rs_1","status":"completed","summary":[],"encrypted_content":"finished"}`},
	} {
		t.Run(item.name, func(t *testing.T) {
			for _, status := range []struct {
				name, value string
				valid       bool
			}{
				{"legacy_omitted", "", true},
				{"in_progress", `"in_progress"`, true},
				{"completed", `"completed"`, true},
				{"incomplete", `"incomplete"`, false},
				{"failed", `"failed"`, false},
				{"null", "null", false},
				{"empty", `""`, false},
				{"unknown", `"future_status"`, false},
				{"wrong_type", "42", false},
			} {
				t.Run(status.name, func(t *testing.T) {
					for _, withDone := range []bool{false, true} {
						for _, required := range []bool{false, true} {
							t.Run(fmt.Sprintf("done_%t_required_%t", withDone, required), func(t *testing.T) {
								client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
									w.Header().Set("Content-Type", "text/event-stream")
									added := "{" + item.added
									if status.value != "" {
										added += `,"status":` + status.value
									}
									added += "}"
									if status.name == "completed" {
										added = item.final
									}
									writeReasoningEvent(t, w, "response.output_item.added", map[string]any{
										"type": "response.output_item.added", "output_index": 0, "item": json.RawMessage(added),
									})
									if withDone {
										writeReasoningEvent(t, w, "response.output_item.done", map[string]any{
											"type": "response.output_item.done", "output_index": 0, "item": json.RawMessage(item.final),
										})
									}
									output := []json.RawMessage{json.RawMessage(item.final)}
									if item.name != "function_call" {
										output = append(output, json.RawMessage(finalTool))
									}
									writeReasoningEvent(t, w, "response.completed", map[string]any{
										"type": "response.completed",
										"response": map[string]any{"object": "response", "id": "resp-added-status", "status": "completed",
											"output": output, "usage": map[string]any{"input_tokens": 17, "output_tokens": 5}},
									})
								})
								terminal := 0
								response, err := client.GenerateStream(t.Context(), model.Request{
									Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}},
									Tools:    []tools.Definition{definition}, RequireToolCall: required,
								}, func(event model.StreamEvent) error {
									if event.ResponseID != "" {
										terminal++
									}
									return nil
								})
								if status.valid {
									if err != nil || response.ResponseID != "resp-added-status" || response.Usage == nil ||
										len(response.ToolCalls) != 1 || terminal != 1 {
										t.Fatalf("valid added item rejected: response=%+v err=%v terminal=%d", response, err, terminal)
									}
									return
								}
								if !errors.Is(err, providerhttp.ErrProtocol) || model.IsResponseRejectedError(err) ||
									response.ResponseID != "" || response.Usage != nil || response.Content != "" ||
									len(response.ToolCalls) != 0 || response.ProviderReplay != nil || terminal != 0 {
									t.Fatalf("invalid added status authorized Tool or billing: response=%+v err=%v terminal=%d", response, err, terminal)
								}
							})
						}
					}
				})
			}
		})
	}
}

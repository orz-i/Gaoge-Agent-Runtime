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

// An added item that already claims completion carries full terminal
// evidence. Subsequent done/final events cannot substitute its payload.
func TestStreamCompletedAddedItemPreservesTerminalEvidence(t *testing.T) {
	const tool = `{"type":"function_call","id":"fc_1","status":"completed","call_id":"call_1","name":"lookup","arguments":"{}"}`
	const message = `{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Working"}]}`
	const reasoning = `{"type":"reasoning","id":"rs_1","status":"completed","summary":[],"encrypted_content":"finished"}`
	for _, tc := range []struct {
		name, added, final string
		sibling, valid     bool
	}{
		{"matching_function", tool, tool, false, true},
		{"matching_message", message, message, true, true},
		{"matching_reasoning", reasoning, reasoning, true, true},
		{"substituted_arguments", `{"type":"function_call","id":"fc_1","status":"completed","call_id":"call_1","name":"lookup","arguments":"{\"x\":1}"}`, tool, false, false},
		{"malformed_completed_arguments", `{"type":"function_call","id":"fc_1","status":"completed","call_id":"call_1","name":"lookup","arguments":""}`, tool, false, false},
		{"substituted_message_text", `{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Different"}]}`, message, true, false},
		{"removed_message_refusal", `{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"refusal","refusal":"Cannot do this"}]}`, message, true, false},
		{"substituted_reasoning", `{"type":"reasoning","id":"rs_1","status":"completed","summary":[],"encrypted_content":"different"}`, reasoning, true, false},
		{"omitted_final_reasoning_bytes", reasoning, `{"type":"reasoning","id":"rs_1","status":"completed","summary":[]}`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, withDone := range []bool{false, true} {
				t.Run(fmt.Sprintf("done_%t", withDone), func(t *testing.T) {
					client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
						w.Header().Set("Content-Type", "text/event-stream")
						writeReasoningEvent(t, w, "response.output_item.added", map[string]any{
							"type": "response.output_item.added", "output_index": 0, "item": json.RawMessage(tc.added),
						})
						if withDone {
							writeReasoningEvent(t, w, "response.output_item.done", map[string]any{
								"type": "response.output_item.done", "output_index": 0, "item": json.RawMessage(tc.final),
							})
						}
						output := []json.RawMessage{json.RawMessage(tc.final)}
						if tc.sibling {
							output = append(output, json.RawMessage(tool))
						}
						writeReasoningEvent(t, w, "response.completed", map[string]any{
							"type": "response.completed",
							"response": map[string]any{"object": "response", "id": "resp-completed-added", "status": "completed",
								"output": output, "usage": map[string]any{"input_tokens": 17, "output_tokens": 5}},
						})
					})
					terminal := 0
					response, err := client.GenerateStream(t.Context(), model.Request{
						Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}},
						Tools:    []tools.Definition{definition}, RequireToolCall: true,
					}, func(event model.StreamEvent) error {
						if event.ResponseID != "" {
							terminal++
						}
						return nil
					})
					if tc.valid {
						if err != nil || len(response.ToolCalls) != 1 || response.ResponseID != "resp-completed-added" || terminal != 1 {
							t.Fatalf("matching completed added item rejected: response=%+v err=%v terminal=%d", response, err, terminal)
						}
						return
					}
					if !errors.Is(err, providerhttp.ErrProtocol) || model.IsResponseRejectedError(err) ||
						response.ResponseID != "" || response.Usage != nil || len(response.ToolCalls) != 0 ||
						response.Content != "" || response.ProviderReplay != nil || terminal != 0 {
						t.Fatalf("completed added payload was replaced: response=%+v err=%v terminal=%d", response, err, terminal)
					}
				})
			}
		})
	}
}

package openai_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/internal/providerhttp"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// The added event is not a completion receipt: arguments and encrypted
// reasoning may still be partial. Its item identity, however, must not be
// silently replaced by a later done/completed event with a different local
// function-call identity.
func TestStreamAddedItemIdentityCannotBeReplaced(t *testing.T) {
	const finalTool = `{"type":"function_call","id":"fc_1","status":"completed","call_id":"call-1","name":"lookup","arguments":"{}"}`
	const validAdded = `{"type":"function_call","id":"fc_1","status":"in_progress","call_id":"call-1","name":"lookup","arguments":""}`
	cases := []struct {
		name      string
		added     []string
		valid     bool
		afterDone bool
	}{
		{name: "final_only", valid: true},
		{name: "matching_added", added: []string{validAdded}, valid: true},
		{name: "changed_item_id", added: []string{`{"type":"function_call","id":"fc_other","status":"in_progress","call_id":"call-1","name":"lookup","arguments":""}`}},
		{name: "changed_call_id", added: []string{`{"type":"function_call","id":"fc_1","status":"in_progress","call_id":"call-other","name":"lookup","arguments":""}`}},
		{name: "changed_tool_name", added: []string{`{"type":"function_call","id":"fc_1","status":"in_progress","call_id":"call-1","name":"other","arguments":""}`}},
		{name: "changed_item_type", added: []string{`{"type":"message","id":"fc_1","status":"in_progress","role":"assistant","content":[]}`}},
		{name: "duplicate_added", added: []string{validAdded, validAdded}},
		{name: "added_after_done", added: []string{validAdded}, afterDone: true},
		{name: "absent_item_id", added: []string{`{"type":"function_call","status":"in_progress","call_id":"call-1","name":"lookup","arguments":""}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				writeDone := func() {
					writeReasoningEvent(t, w, "response.output_item.done", map[string]any{
						"type": "response.output_item.done", "output_index": 0, "item": json.RawMessage(finalTool),
					})
				}
				if tc.afterDone {
					writeDone()
				}
				for _, item := range tc.added {
					writeReasoningEvent(t, w, "response.output_item.added", map[string]any{
						"type": "response.output_item.added", "output_index": 0, "item": json.RawMessage(item),
					})
				}
				if !tc.afterDone {
					writeDone()
				}
				writeReasoningEvent(t, w, "response.completed", map[string]any{
					"type": "response.completed",
					"response": map[string]any{"object": "response", "id": "resp-added", "status": "completed",
						"output": []json.RawMessage{json.RawMessage(finalTool)},
						"usage":  map[string]any{"input_tokens": 11, "output_tokens": 6}},
				})
			})
			request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}},
				Tools: []tools.Definition{definition}, RequireToolCall: true}
			response, err := client.GenerateStream(t.Context(), request, nil)
			if tc.valid {
				if err != nil || response.ResponseID != "resp-added" || len(response.ToolCalls) != 1 {
					t.Fatalf("valid stream rejected: response=%+v err=%v", response, err)
				}
				return
			}
			if !errors.Is(err, providerhttp.ErrProtocol) || model.IsResponseRejectedError(err) ||
				response.ResponseID != "" || response.Usage != nil || len(response.ToolCalls) != 0 {
				t.Fatalf("SSE added identity contradiction authorized a local Tool: response=%+v err=%v", response, err)
			}
		})
	}
}

// Earlier adapters allow a genuinely omitted final message role on legacy
// Responses gateways. An explicit assistant role on the added event is
// still useful identity evidence and must not manufacture a contradiction.
func TestStreamAddedMessageAllowsOmittedLegacyFinalRole(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeReasoningEvent(t, w, "response.output_item.added", map[string]any{
			"type": "response.output_item.added", "output_index": 0,
			"item": json.RawMessage(`{"type":"message","id":"msg_1","role":"assistant","status":"in_progress","content":[]}`),
		})
		writeReasoningEvent(t, w, "response.completed", map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"object": "response", "id": "resp-legacy", "status": "completed",
				"output": []json.RawMessage{json.RawMessage(`{"type":"message","id":"msg_1","content":[{"type":"output_text","text":"answer"}]}`)},
				"usage":  map[string]any{"input_tokens": 10, "output_tokens": 3},
			},
		})
	})
	response, err := client.GenerateStream(t.Context(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("hello")}},
	}, nil)
	if err != nil || response.Content != "answer" || response.ResponseID != "resp-legacy" {
		t.Fatalf("legacy final role omission became a false added-item conflict: %+v %v", response, err)
	}
}

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

// A response.output_item.done is independent physical completion evidence.
// If it was observed, a later response.completed cannot silently replace a
// Tool call, its arguments, its index or its completion status. Final-only
// gateways remain supported, as do genuinely omitted completed statuses.
func TestStreamCompletedOutputItemsReconcileBeforeToolDispatch(t *testing.T) {
	const tool = `{"type":"function_call","id":"fc_1","status":"completed","call_id":"call-1","name":"lookup","arguments":"{}"}`
	const noStatusTool = `{"type":"function_call","id":"fc_1","call_id":"call-1","name":"lookup","arguments":"{}"}`
	const msg = `{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"doing lookup"}]}`
	const msgNoStatus = `{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"doing lookup"}]}`
	type doneItem struct {
		index        int
		item         string
		repeat       bool
		missingIndex bool
	}
	cases := []struct {
		name  string
		done  []doneItem
		final []string
		valid bool
	}{
		{name: "valid_final_only", final: []string{tool}, valid: true},
		{name: "valid_matching_done", done: []doneItem{{item: tool}}, final: []string{tool}, valid: true},
		{name: "valid_omitted_done_status", done: []doneItem{{item: noStatusTool}}, final: []string{tool}, valid: true},
		{name: "valid_omitted_final_status", done: []doneItem{{item: tool}}, final: []string{noStatusTool}, valid: true},
		{name: "valid_message_then_tool", done: []doneItem{{item: msg}}, final: []string{msg, tool}, valid: true},
		{name: "valid_message_omitted_done_status", done: []doneItem{{item: msgNoStatus}}, final: []string{msg, tool}, valid: true},
		{name: "incomplete_done", done: []doneItem{{item: `{"type":"function_call","id":"fc_1","status":"incomplete","call_id":"call-1","name":"lookup","arguments":"{}"}`}}, final: []string{tool}},
		{name: "in_progress_done", done: []doneItem{{item: `{"type":"function_call","id":"fc_1","status":"in_progress","call_id":"call-1","name":"lookup","arguments":"{}"}`}}, final: []string{tool}},
		{name: "null_status_done", done: []doneItem{{item: `{"type":"function_call","id":"fc_1","status":null,"call_id":"call-1","name":"lookup","arguments":"{}"}`}}, final: []string{tool}},
		{name: "different_call_id", done: []doneItem{{item: `{"type":"function_call","id":"fc_1","status":"completed","call_id":"forged","name":"lookup","arguments":"{}"}`}}, final: []string{tool}},
		{name: "different_name", done: []doneItem{{item: `{"type":"function_call","id":"fc_1","status":"completed","call_id":"call-1","name":"other","arguments":"{}"}`}}, final: []string{tool}},
		{name: "different_arguments", done: []doneItem{{item: `{"type":"function_call","id":"fc_1","status":"completed","call_id":"call-1","name":"lookup","arguments":"{\"x\":1}"}`}}, final: []string{tool}},
		{name: "different_item_id", done: []doneItem{{item: `{"type":"function_call","id":"fc_different","status":"completed","call_id":"call-1","name":"lookup","arguments":"{}"}`}}, final: []string{tool}},
		{name: "wrong_output_index", done: []doneItem{{index: 2, item: tool}}, final: []string{tool}},
		{name: "missing_output_index", done: []doneItem{{item: tool, missingIndex: true}}, final: []string{tool}},
		{name: "duplicated_done", done: []doneItem{{item: tool, repeat: true}}, final: []string{tool}},
		{name: "message_done_for_tool", done: []doneItem{{item: msg}}, final: []string{tool}},
		{name: "unknown_done_type_for_tool", done: []doneItem{{item: `{"type":"future_tool_call","id":"unknown"}`}}, final: []string{tool}},
		{name: "null_done_item_for_tool", done: []doneItem{{item: `null`}}, final: []string{tool}},
		{name: "message_role_disagreement", done: []doneItem{{item: `{"type":"message","id":"msg_1","status":"completed","role":"user","content":[{"type":"output_text","text":"doing lookup"}]}`}}, final: []string{msg, tool}},
		{name: "message_status_disagreement", done: []doneItem{{item: `{"type":"message","id":"msg_1","status":"in_progress","role":"assistant","content":[{"type":"output_text","text":"doing lookup"}]}`}}, final: []string{msg, tool}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				write := func(event string, frame any) {
					raw, err := json.Marshal(frame)
					if err != nil {
						t.Error(err)
						return
					}
					_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
				}
				for _, d := range tc.done {
					frame := map[string]any{"type": "response.output_item.done", "item": json.RawMessage(d.item)}
					if !d.missingIndex {
						frame["output_index"] = d.index
					}
					write("response.output_item.done", frame)
					if d.repeat {
						write("response.output_item.done", frame)
					}
				}
				output := make([]json.RawMessage, len(tc.final))
				for i, item := range tc.final {
					output[i] = json.RawMessage(item)
				}
				write("response.completed", map[string]any{
					"type": "response.completed",
					"response": map[string]any{"object": "response", "id": "response-evidence", "status": "completed",
						"output": output, "usage": map[string]any{"input_tokens": 17, "output_tokens": 5}},
				})
			})
			req := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}},
				Tools: []tools.Definition{definition}, RequireToolCall: true}
			terminal := 0
			response, err := client.GenerateStream(t.Context(), req, func(event model.StreamEvent) error {
				if event.ResponseID != "" {
					terminal++
				}
				return nil
			})
			if tc.valid {
				if err != nil || terminal != 1 || len(response.ToolCalls) != 1 ||
					response.ToolCalls[0].ID != "call-1" || response.ToolCalls[0].ToolKey != definition.Key ||
					response.ResponseID != "response-evidence" || response.Usage == nil ||
					response.Usage.InputTokens != 17 || response.Usage.OutputTokens != 5 {
					t.Fatalf("valid completed provider stream rejected: response=%+v err=%v terminal=%d", response, err, terminal)
				}
				return
			}
			if !errors.Is(err, providerhttp.ErrProtocol) || model.IsResponseRejectedError(err) ||
				len(response.ToolCalls) != 0 || response.ProviderReplay != nil ||
				response.ResponseID != "" || response.Usage != nil || terminal != 0 {
				t.Fatalf("contradictory SSE done evidence authorized Tool/receipt: response=%+v err=%v terminal=%d", response, err, terminal)
			}
		})
	}
}

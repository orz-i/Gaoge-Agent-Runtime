package openai_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/internal/providerhttp"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestStreamFinalizedArgumentsReconcile(t *testing.T) {
	for _, tc := range []struct {
		name, evidence string
		valid          bool
	}{
		{"matching", `{"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"arguments":"{\"x\":2}"}`, true},
		{"conflicting", `{"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"arguments":"{\"x\":1}"}`, false},
		{"wrong_id", `{"type":"response.function_call_arguments.done","item_id":"other","output_index":0,"arguments":"{\"x\":2}"}`, false},
		{"missing_index", `{"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":"{\"x\":2}"}`, false},
		{"missing_arguments", `{"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "event: response.function_call_arguments.done\ndata: %s\n\n", tc.evidence)
				_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"object\":\"response\",\"id\":\"resp\",\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"id\":\"fc_1\",\"status\":\"completed\",\"call_id\":\"call-1\",\"name\":\"lookup\",\"arguments\":\"{\\\"x\\\":2}\"}],\"usage\":{\"input_tokens\":17,\"output_tokens\":5}}}\n\n")
			})
			res, err := client.GenerateStream(t.Context(), model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}}, Tools: []tools.Definition{definition}}, nil)
			if tc.valid {
				if err != nil || len(res.ToolCalls) != 1 {
					t.Fatalf("valid: %+v %v", res, err)
				}
				return
			}
			if !errors.Is(err, providerhttp.ErrProtocol) || len(res.ToolCalls) != 0 || res.Usage != nil {
				t.Fatalf("conflicting evidence: %+v %v", res, err)
			}
		})
	}
}

func TestStreamFinalizedMessageContentReconcile(t *testing.T) {
	for _, tc := range []struct {
		name, event, data, final string
		valid                    bool
	}{
		{"matching_text", "response.output_text.done", `{"type":"response.output_text.done","item_id":"msg_1","output_index":0,"content_index":0,"text":"allowed"}`, `{"type":"output_text","text":"allowed"}`, true},
		{"changed_text", "response.output_text.done", `{"type":"response.output_text.done","item_id":"msg_1","output_index":0,"content_index":0,"text":"allowed"}`, `{"type":"refusal","refusal":"blocked"}`, false},
		{"matching_refusal", "response.refusal.done", `{"type":"response.refusal.done","item_id":"msg_1","output_index":0,"content_index":0,"refusal":"blocked"}`, `{"type":"refusal","refusal":"blocked"}`, true},
		{"changed_refusal", "response.refusal.done", `{"type":"response.refusal.done","item_id":"msg_1","output_index":0,"content_index":0,"refusal":"blocked"}`, `{"type":"output_text","text":"allowed"}`, false},
		{"matching_part", "response.content_part.done", `{"type":"response.content_part.done","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":"allowed"}}`, `{"type":"output_text","text":"allowed"}`, true},
		{"changed_part", "response.content_part.done", `{"type":"response.content_part.done","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"refusal","refusal":"blocked"}}`, `{"type":"output_text","text":"allowed"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", tc.event, tc.data)
				_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"object\":\"response\",\"id\":\"resp\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[%s]}],\"usage\":{\"input_tokens\":17,\"output_tokens\":5}}}\n\n", tc.final)
			})
			res, err := client.GenerateStream(t.Context(), model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("hello")}}}, nil)
			if tc.valid {
				if err != nil {
					t.Fatalf("valid: %+v %v", res, err)
				}
				return
			}
			if !errors.Is(err, providerhttp.ErrProtocol) || res.Usage != nil || len(res.ToolCalls) != 0 {
				t.Fatalf("contradiction: %+v %v", res, err)
			}
		})
	}
}

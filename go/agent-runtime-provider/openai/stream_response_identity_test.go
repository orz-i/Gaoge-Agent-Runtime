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

func TestStreamResponseIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, events string
		valid        bool
	}{
		{"final_only", "", true},
		{"same_created", `data: {"type":"response.created","response":{"object":"response","id":"resp_final","status":"in_progress"}}` + "\n\n", true},
		{"same_both", `data: {"type":"response.created","response":{"object":"response","id":"resp_final","status":"in_progress"}}` + "\n\n" + `data: {"type":"response.in_progress","response":{"object":"response","id":"resp_final","status":"in_progress"}}` + "\n\n", true},
		{"created_mismatch", `data: {"type":"response.created","response":{"object":"response","id":"other","status":"in_progress"}}` + "\n\n", false},
		{"progress_mismatch", `data: {"type":"response.in_progress","response":{"object":"response","id":"other","status":"in_progress"}}` + "\n\n", false},
		{"foreign_object", `data: {"type":"response.created","response":{"object":"chat.completion","id":"resp_final","status":"in_progress"}}` + "\n\n", false},
		{"missing_id", `data: {"type":"response.created","response":{"object":"response","status":"in_progress"}}` + "\n\n", false},
		{"duplicate_created", `data: {"type":"response.created","response":{"object":"response","id":"resp_final","status":"in_progress"}}` + "\n\n" + `data: {"type":"response.created","response":{"object":"response","id":"resp_final","status":"in_progress"}}` + "\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, tc.events)
				_, _ = fmt.Fprint(w, `data: {"type":"response.completed","response":{"object":"response","id":"resp_final","status":"completed","output":[{"type":"function_call","id":"fc_1","status":"completed","call_id":"call_1","name":"lookup","arguments":"{}"}],"usage":{"input_tokens":17,"output_tokens":5}}}`+"\n\n")
			})
			terminal := 0
			response, err := client.GenerateStream(t.Context(), model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}}, Tools: []tools.Definition{definition}, RequireToolCall: true}, func(e model.StreamEvent) error {
				if e.ResponseID != "" {
					terminal++
				}
				return nil
			})
			if tc.valid {
				if err != nil || len(response.ToolCalls) != 1 || terminal != 1 {
					t.Fatalf("valid: %+v %v terminal=%d", response, err, terminal)
				}
				return
			}
			if !errors.Is(err, providerhttp.ErrProtocol) || len(response.ToolCalls) != 0 || response.Usage != nil || terminal != 0 {
				t.Fatalf("invalid authorized: %+v %v terminal=%d", response, err, terminal)
			}
		})
	}
}

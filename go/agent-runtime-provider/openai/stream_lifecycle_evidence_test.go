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

// An earlier physical lifecycle frame claiming terminal failure, completion,
// or truncation cannot be superseded by a later completed Tool response.
func TestOpenAIStreamRejectsContradictoryEarlyLifecycleEvidence(t *testing.T) {
	cases := []struct {
		name, event, status, extra string
		allowed                    bool
	}{
		{"created_failed", "response.created", "failed", "", false},
		{"created_completed", "response.created", "completed", "", false},
		{"created_incomplete", "response.created", "incomplete", "", false},
		{"in_progress_failed", "response.in_progress", "failed", "", false},
		{"in_progress_queued", "response.in_progress", "queued", "", false},
		{"created_null_status", "response.created", "", `,"status":null`, false},
		{"created_nonnull_error", "response.created", "in_progress", `,"error":{"code":"server_error"}`, false},
		{"in_progress_truncation", "response.in_progress", "in_progress", `,"incomplete_details":{"reason":"max_output_tokens"}`, false},
		{"created_in_progress", "response.created", "in_progress", "", true},
		{"created_queued", "response.created", "queued", "", true},
		{"created_legacy_omitted_status", "response.created", "", "", true},
		{"in_progress_valid", "response.in_progress", "in_progress", "", true},
		{"created_explicit_null_details", "response.created", "in_progress", `,"error":null,"incomplete_details":null`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				early := `{"object":"response","id":"resp-lifecycle"` + tc.extra
				if tc.status != "" {
					early += fmt.Sprintf(`,"status":%q`, tc.status)
				}
				early += `}`
				final := `{"object":"response","id":"resp-lifecycle","status":"completed","output":[{"type":"function_call","status":"completed","call_id":"call-1","name":"lookup","arguments":"{}"}],"usage":{"input_tokens":3,"output_tokens":2}}`
				_, _ = fmt.Fprintf(w, "event: %s\ndata: {\"type\":%q,\"response\":%s}\n\n", tc.event, tc.event, early)
				_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", final)
			})
			req := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}}, Tools: []tools.Definition{definition}, RequireToolCall: true}
			terminal := 0
			response, err := client.GenerateStream(t.Context(), req, func(event model.StreamEvent) error {
				if event.ResponseID != "" {
					terminal++
				}
				return nil
			})
			if tc.allowed {
				if err != nil || len(response.ToolCalls) != 1 || terminal != 1 {
					t.Fatalf("valid early lifecycle denied: response=%+v err=%v terminal=%d", response, err, terminal)
				}
			} else if !errors.Is(err, providerhttp.ErrProtocol) || response.ResponseID != "" || response.Usage != nil || len(response.ToolCalls) != 0 || terminal != 0 {
				t.Fatalf("contradictory physical lifecycle authorized: response=%+v err=%v terminal=%d", response, err, terminal)
			}
		})
	}
}

func TestOpenAIStreamAcceptsQueuedInProgressCompletedSequence(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(event string, response any) {
			payload, err := json.Marshal(map[string]any{"type": event, "response": response})
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
		}
		send("response.created", map[string]any{"object": "response", "id": "resp-life", "status": "queued"})
		send("response.in_progress", map[string]any{"object": "response", "id": "resp-life", "status": "in_progress"})
		send("response.completed", map[string]any{
			"object": "response", "id": "resp-life", "status": "completed",
			"output": []any{map[string]any{"type": "function_call", "status": "completed", "call_id": "call-1", "name": "lookup", "arguments": "{}"}},
			"usage":  map[string]any{"input_tokens": 3, "output_tokens": 2},
		})
	})
	req := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}}, Tools: []tools.Definition{definition}, RequireToolCall: true}
	response, err := client.GenerateStream(t.Context(), req, nil)
	if err != nil || response.ResponseID != "resp-life" || len(response.ToolCalls) != 1 {
		t.Fatalf("valid queued-progress-completed sequence denied: response=%+v err=%v", response, err)
	}
}

package openai_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestStreamErrorsRetryOnlyKnownTransientCodes(t *testing.T) {
	for _, event := range []string{"response.failed", "error"} {
		for _, tc := range []struct {
			code      string
			retryable bool
		}{
			{"server_error", true},
			{"rate_limit_exceeded", true},
			{"slow_down", true},
			{"server_is_overloaded", true},
			{"invalid_prompt", false},
			{"insufficient_quota", false},
			{"credit_balance_exhausted", false},
			{"organization_spend_limit_exceeded", false},
			{"unknown_error", false},
			{"server_error PRIVATE_CODE", false},
			{"", false},
		} {
			t.Run(event+"/"+tc.code, func(t *testing.T) {
				client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					failure := map[string]any{"code": tc.code, "message": "PRIVATE_STREAM_DETAIL server_error"}
					frame := map[string]any{"type": event, "code": tc.code, "message": failure["message"]}
					if event == "response.failed" {
						frame = map[string]any{"type": event, "response": map[string]any{"status": "failed", "error": failure}}
					}
					data, err := json.Marshal(frame)
					if err != nil {
						t.Error(err)
						return
					}
					_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
				})
				_, err := client.GenerateStream(t.Context(), model.Request{
					Messages: []model.Message{{Role: model.RoleUser, Content: "hi"}},
				}, nil)
				if err == nil || model.IsRetryableError(err) != tc.retryable {
					t.Fatalf("retryable=%v want=%v err=%v", model.IsRetryableError(err), tc.retryable, err)
				}
				if strings.Contains(err.Error(), "PRIVATE") {
					t.Fatalf("stream failure exposed upstream detail: %v", err)
				}
			})
		}
	}
}

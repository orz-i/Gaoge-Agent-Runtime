package anthropic_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestStreamErrorsRetryOnlyKnownTransientTypes(t *testing.T) {
	for _, tc := range []struct {
		kind      string
		retryable bool
	}{
		{"overloaded_error", true},
		{"api_error", true},
		{"rate_limit_error", true},
		{"timeout_error", true},
		{"invalid_request_error", false},
		{"authentication_error", false},
		{"permission_error", false},
		{"billing_error", false},
		{"unknown_error", false},
		{"overloaded_error PRIVATE_TYPE", false},
		{"", false},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				frame := map[string]any{"type": "error", "error": map[string]any{
					"type": tc.kind, "message": "PRIVATE_STREAM_DETAIL overloaded_error",
				}}
				data, err := json.Marshal(frame)
				if err != nil {
					t.Error(err)
					return
				}
				_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
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

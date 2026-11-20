package agent

import "testing"

func TestRequiredToolCompletionAcceptsCommittedJSONDataShapes(t *testing.T) {
	for _, content := range []string{`["D-01","D-02"]`, `"source text"`, `42`, `false`, `null`, `{}`, `{"documents":[]}`} {
		if toolResultRejectedOrRetryable(content) {
			t.Fatalf("valid committed Tool data was treated as a failed required call: %s", content)
		}
	}
	for _, content := range []string{`not JSON`, `{"ok":false}`, `{"retryable":true}`, `{"status":"rejected"}`} {
		if !toolResultRejectedOrRetryable(content) {
			t.Fatalf("an existing rejection was weakened: %s", content)
		}
	}
}

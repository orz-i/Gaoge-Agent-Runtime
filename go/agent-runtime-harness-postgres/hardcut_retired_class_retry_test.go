package harnesspostgres

import (
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

// Store.RetryInvocation checks this SQL gate before any transaction can
// rotate an old persisted Application's execution reference into an Agent.
func TestRetiredApplicationRejectedBySQLRetryAdmission(t *testing.T) {
	original := harness.Invocation{
		ExecutionClass: harness.ExecutionAgent,
		Revision:       1, Status: harness.InvocationFailed,
		Input: []byte(`{"goal":"original Agent"}`),
	}
	if !validStoredInvocationRetry(original, 1) {
		t.Fatal("valid root Agent retry unexpectedly rejected")
	}
	original.ParentItemID = "parent-item"
	if validStoredInvocationRetry(original, 1) {
		t.Fatal("retired Native Child retry admitted by SQL")
	}
	original.ParentItemID = ""
	original.ExecutionClass = harness.ExecutionClass("application")
	if validStoredInvocationRetry(original, 1) {
		t.Fatal("retired Application row can rotate its SQL execution identity")
	}
}

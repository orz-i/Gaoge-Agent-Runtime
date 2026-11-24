package harness

import (
	"errors"
	"fmt"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// The first-admission Agent cancellation exception must never be inherited by
// RetryInvocation (Attempt > 1) or any composite Feature, even after sharing
// one top-level result settlement method.
func TestHardcutInitialAgentCancellationConflictIsNotRetryCompatibility(t *testing.T) {
	tests := []struct {
		name       string
		attempt    int
		status     TurnStatus
		err        error
		suppressed bool
	}{
		{name: "initial-Agent-cancel", attempt: 1, status: TurnCancelled, err: kernel.ErrConflict, suppressed: true},
		{name: "wrapped-initial-Agent-cancel", attempt: 1, status: TurnCancelled, err: fmt.Errorf("committed cancel: %w", kernel.ErrConflict), suppressed: true},
		{name: "second-Agent-attempt", attempt: 2, status: TurnCancelled, err: kernel.ErrConflict},
		{name: "third-Agent-attempt", attempt: 3, status: TurnCancelled, err: kernel.ErrConflict},
		{name: "missing-Attempt-proof", attempt: 0, status: TurnCancelled, err: kernel.ErrConflict},
		{name: "Agent-still-running", attempt: 1, status: TurnRunning, err: kernel.ErrConflict},
		{name: "Agent-different-error", attempt: 1, status: TurnCancelled, err: errors.New("different failure")},
		{name: "Agent-no-error", attempt: 1, status: TurnCancelled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := initialAgentCancelledStartConflict(Invocation{Attempt: tc.attempt}, tc.status, tc.err)
			if got != tc.suppressed {
				t.Fatalf("suppressed=%t, want %t (attempt=%d status=%s err=%v)", got, tc.suppressed, tc.attempt, tc.status, tc.err)
			}
		})
	}
}

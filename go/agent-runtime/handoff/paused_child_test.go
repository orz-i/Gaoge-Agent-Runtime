package handoff

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// A child paused for a renewable allowance must not turn into a failed Join.
func TestPausedChildProjectsAsPendingDelegation(t *testing.T) {
	t.Parallel()
	child := kernel.Snapshot{Run: kernel.Run{
		ID: "paused-child", Status: kernel.RunStatusPausedBudget,
		ErrorCode: "agent.model_allowance_exhausted",
	}}
	projected := projectChild(Delegation{ID: "delegation", MemberID: "member",
		ChildRunID: "paused-child", Goal: "research"}, child)
	if projected.Status != StatusRunning || projected.ErrorCode != child.Run.ErrorCode {
		t.Fatalf("paused child projected incorrectly: %#v", projected)
	}
	if err := childStateError(child); !errors.Is(err, ErrChildPending) {
		t.Fatalf("paused child must remain pending: %v", err)
	}
}

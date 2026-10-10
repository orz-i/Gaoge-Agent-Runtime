package workflow

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestPausedSubworkflowRemainsPending(t *testing.T) {
	t.Parallel()
	child := kernel.Snapshot{Run: kernel.Run{
		ID: "paused-subworkflow", Status: kernel.RunStatusPausedBudget,
		ErrorCode: "agent.tool_allowance_exhausted",
	}}
	result, err := subworkflowEffectResult(child)
	if err != nil || result.Disposition != DispositionPending ||
		result.ChildRunID != child.Run.ID || result.ErrorCode != "" {
		t.Fatalf("paused subworkflow became terminal: %#v error=%v", result, err)
	}
}

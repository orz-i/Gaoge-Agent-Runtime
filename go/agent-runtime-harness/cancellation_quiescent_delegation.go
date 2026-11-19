package harness

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Resume the SAME elected intent after a transient lease-only admission race.
// This is not a terminal vote: actual Cancel still rechecks the native Run CAS,
// grant barrier and post-Cancel custody before any intent can be closed.
func (runner *Runner) quiescentDelegationCancellationReady(ctx context.Context, runID string, snapshot kernel.Snapshot) bool {
	if runner == nil || snapshot.Run.ID != runID || snapshot.Run.Kind != agent.RunKind ||
		!runner.localRelationCancellerOwnsKernel(agent.RunKind) {
		return false
	}
	owner, err := runner.store.GetInvocationByExecutionRefID(ctx, runID)
	return err == nil && isHandoffDelegationInvocation(owner) &&
		runner.verifyLocalRelationRun(ctx, snapshot, true) == nil
}

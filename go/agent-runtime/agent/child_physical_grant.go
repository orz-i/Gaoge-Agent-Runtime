package agent

import (
	"context"
	"errors"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// withChildPhysicalGrant surrounds the entire first-party Agent physical
// Start/Approval/Resume, including ALL Host middleware, Model and Tool calls.
// Its Store admission is transactionally serialized against terminal mutation
// of every Kernel ancestor. An in-flight Grant blocks parent termination;
// after parent termination no new physical execution may be admitted.
//
// A crashed/unknown Model or Tool outcome retains the Grant. Only an Agent
// that has committed settlement of every physical intent may close it; no
// deadline or process-local mutex can silently authorize another executor.
func (runner *Runner) withChildPhysicalGrant(ctx context.Context, runID string,
	physical func() (kernel.Snapshot, error)) (kernel.Snapshot, error) {
	if runner == nil || runner.runtime == nil || physical == nil {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	child, err := runner.runtime.Load(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if child.Run.ParentRunID == "" {
		return physical()
	}
	grant, err := runner.runtime.BeginChildExecution(ctx, runID)
	if err != nil {
		// Store admission rejected BEFORE invoking middleware or any
		// external Model/Tool. Preserve the original child snapshot as
		// affirmative evidence that this callback did not dispatch work.
		return child, err
	}
	// A panic or worker termination deliberately leaves an unresolved Grant;
	// only a successful, verified physical result can discharge it.
	result, physicalErr := physical()
	// A Host Run middleware may perform effects before returning an error.
	// Even a clean Agent State is not proof that those effects never happened;
	// only a completed callback that reports success may mark its Grant
	// settled. Error/timeout/panic retain the exact original Grant.
	if physicalErr != nil {
		return result, errors.Join(physicalErr, kernel.ErrChildExecutionBusy)
	}
	settlementCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer stop()
	latest, loadErr := runner.runtime.Load(settlementCtx, runID)
	if loadErr != nil || !knownAgentPhysicalSettlement(latest) {
		return result, errors.Join(physicalErr, kernel.ErrChildExecutionBusy, loadErr)
	}
	proof := featurecontrol.Seal(latest.Run.ID, latest.Run.Revision, "agent_child_physical_settled", grant.ID, latest.State, latest.Checkpoint)
	if markErr := runner.runtime.MarkChildExecutionSettled(settlementCtx, grant, proof); markErr != nil {
		return result, errors.Join(kernel.ErrChildExecutionBusy, markErr)
	}
	if finishErr := runner.runtime.FinishChildExecution(settlementCtx, grant, proof); finishErr != nil {
		return result, errors.Join(physicalErr, kernel.ErrChildExecutionBusy, finishErr)
	}
	return result, physicalErr
}

// knownAgentPhysicalSettlement validates a persisted Feature state, including
// nonterminal wait/resume points, without treating pending external work as
// an automatic no-effect receipt. This is stricter than a Kernel RunStatus.
func knownAgentPhysicalSettlement(snapshot kernel.Snapshot) bool {
	state, err := decodeState(snapshot.State)
	if err != nil || state.ToolDispatchID != "" || state.ToolWait != nil ||
		len(state.ExternalSignals) != 0 || len(state.DeferredChildren) != 0 {
		return false
	}
	for _, invocation := range state.ModelInvocations {
		if invocation.Status != ModelInvocationConsumed {
			return false
		}
	}
	return true
}

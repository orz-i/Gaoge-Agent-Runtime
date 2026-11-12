package agent

import (
	"context"
	"errors"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// withChildPhysicalGrant surrounds the entire first-party PARENTED Agent
// Start/Approval/Resume, including Host middleware, Model and Tool calls.
// Store admission atomically fences the executing child ITSELF and all its
// ancestors against unbound physical/terminal writes. A Root Agent retains
// its separate ModelInvocation, ToolDispatch and ExecutionLease protocol;
// do not silently substitute a Workflow Root Grant for its replay semantics.
//
// A crashed/unknown Model or Tool outcome retains the Grant. Only an Agent
// that has committed settlement of every physical intent may close it; no
// deadline or process-local mutex can silently authorize another executor.
func (runner *Runner) withChildPhysicalGrant(ctx context.Context, runID string, expectedRevision uint64,
	physical func(*Runner) (kernel.Snapshot, error)) (kernel.Snapshot, error) {
	if runner == nil || runner.runtime == nil || physical == nil {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	// A child nested in another first-party physical owner acquires its
	// OWN Grant. Never carry the parent's write permit into a foreign Run.
	executionRuntime := runner.runtime.WithoutPhysicalGrant()
	child, err := executionRuntime.Load(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if child.Run.ParentRunID == "" {
		// Root Agent's durable ModelInvocation/ToolDispatch and ExecutionLease
		// authority has distinct restart/replay semantics. Do not conflate it
		// with the Workflow Root physical Grant. The Root Start now creates
		// its Run before any arbitrary Host Run middleware is called.
		return physical(runner)
	}
	grant, err := executionRuntime.BeginChildExecution(ctx, runID, expectedRevision)
	if err != nil {
		return child, err
	}
	proof := featurecontrol.Seal(child.Run.ID, child.Run.Revision,
		"agent_physical_owner", grant.ID, child.State, child.Checkpoint)
	bound, bindErr := executionRuntime.WithPhysicalGrant(ctx, grant, proof)
	if bindErr != nil {
		return child, errors.Join(kernel.ErrChildExecutionBusy, bindErr)
	}
	scoped := *runner
	scoped.runtime = bound
	// The entire first-party Agent Start/Approval/Resume (including Host
	// middleware and Model/Tool callbacks) runs with its original scoped
	// mutation authority. A direct unbound Kernel Cancel/Apply cannot
	// mutate this Run until trusted physical settlement.
	result, physicalErr := physical(&scoped)
	if physicalErr != nil {
		return result, errors.Join(physicalErr, kernel.ErrChildExecutionBusy)
	}
	settlementCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer stop()
	latest, loadErr := executionRuntime.Load(settlementCtx, runID)
	if loadErr != nil || !knownAgentPhysicalSettlement(latest) {
		return result, errors.Join(physicalErr, kernel.ErrChildExecutionBusy, loadErr)
	}
	settledProof := featurecontrol.Seal(latest.Run.ID, latest.Run.Revision,
		"agent_child_physical_settled", grant.ID, latest.State, latest.Checkpoint)
	if markErr := executionRuntime.MarkChildExecutionSettled(settlementCtx, grant, settledProof); markErr != nil {
		return result, errors.Join(kernel.ErrChildExecutionBusy, markErr)
	}
	if finishErr := executionRuntime.FinishChildExecution(settlementCtx, grant, settledProof); finishErr != nil {
		return result, errors.Join(kernel.ErrChildExecutionBusy, finishErr)
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

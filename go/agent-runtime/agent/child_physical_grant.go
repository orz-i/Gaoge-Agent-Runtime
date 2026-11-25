package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// withChildPhysicalGrant surrounds the entire first-party PARENTED Agent
// Start/Approval/Resume, including Host middleware, Model and Tool calls.
// Store admission atomically fences the executing child ITSELF and all its
// ancestors against unbound physical/terminal writes. A Root Agent retains
// its separate ModelInvocation, ToolDispatch and ExecutionLease protocol;
// never substitute an unrelated child Grant for its replay semantics.
//
// A crashed/unknown Model or Tool outcome retains the Grant. Only an Agent
// that has committed settlement of every physical intent may close it; no
// deadline or process-local mutex can silently authorize another executor.
func (runner *Runner) withChildPhysicalGrant(ctx context.Context, runID string, expectedRevision uint64, operation string,
	physical func(context.Context, *Runner) (kernel.Snapshot, error)) (kernel.Snapshot, error) {
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
		// with a child execution Grant. The Root Start creates
		// its Run before any arbitrary Host Run middleware is called.
		return physical(ctx, runner)
	}
	// This is only the in-process journal source, not new Store authority.
	source, err := json.Marshal(struct {
		Snapshot  kernel.Snapshot
		Operation string
	}{child, operation})
	if err != nil {
		return child, err
	}
	sourceHash := sha256.Sum256(source)
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
	// Never inherit a parent callback's failure flag or journal. Child
	// admission has no Root-owned native lease binding.
	scoped.rootCallbacks = &rootCallbackState{}
	lifecycle := callbackscope.New(callbackscope.Binding{
		RunID: grant.ChildRunID, GrantID: grant.ID,
		SourceHash: hex.EncodeToString(sourceHash[:]), Operation: operation,
	}, runner.boundedCallbacks)
	defer lifecycle.Close()
	// The entire first-party Agent Start/Approval/Resume (including Host
	// middleware and Model/Tool callbacks) runs with its original scoped
	// mutation authority. A direct unbound Kernel Cancel/Apply cannot
	// mutate this Run until trusted physical settlement.
	result, physicalErr := physical(callbackscope.Bind(ctx, lifecycle), &scoped)
	if !lifecycle.Complete() || scoped.rootCallbacks.unknown.Load() {
		return result, errors.Join(physicalErr, kernel.ErrChildExecutionBusy)
	}
	settlementCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer stop()
	latest, loadErr := executionRuntime.Load(settlementCtx, runID)
	if loadErr != nil || !knownAgentPhysicalSettlement(latest) ||
		(physicalErr != nil && !scoped.rootCallbacks.knownReturn.Load().matches(result, latest, physicalErr)) {
		return result, errors.Join(physicalErr, kernel.ErrChildExecutionBusy, loadErr)
	}
	settledProof := featurecontrol.Seal(latest.Run.ID, latest.Run.Revision,
		"agent_child_physical_settled", grant.ID, latest.State, latest.Checkpoint)
	if markErr := executionRuntime.MarkChildExecutionSettled(settlementCtx, grant, settledProof); markErr != nil {
		return result, errors.Join(physicalErr, kernel.ErrChildExecutionBusy, markErr)
	}
	if finishErr := executionRuntime.FinishChildExecution(settlementCtx, grant, settledProof); finishErr != nil {
		return result, errors.Join(physicalErr, kernel.ErrChildExecutionBusy, finishErr)
	}
	recordPhysicalObservation(ctx, runID, PhysicalCallSettled)
	return result, physicalErr
}

// committedAgentReturn qualifies only an exact native business failure
// or no-send yield from the original callback. Neither a Host-supplied error
// classification nor a clean-looking snapshot supplies this evidence.
type committedAgentReturn struct {
	runID    string
	revision uint64
	status   kernel.RunStatus
	cause    error
}

func (f *committedAgentReturn) matches(result, latest kernel.Snapshot, returned error) bool {
	return f != nil && f.cause != nil && f.runID != "" && f.revision != 0 &&
		result.Run.ID == f.runID && latest.Run.ID == f.runID && result.Run.Revision == f.revision && latest.Run.Revision == f.revision &&
		result.Run.Status == f.status && latest.Run.Status == f.status && sameOriginalToolError(f.cause, returned)
}

func (r *Runner) noteNativeAdmissionReturn(s kernel.Snapshot, cause error) {
	if r.rootCallbacks != nil && cause != nil && (s.Run.Status == kernel.RunStatusRunning || s.Run.Status == kernel.RunStatusWaitingInput || s.Run.Status == kernel.RunStatusPausedBudget) && knownAgentPhysicalSettlement(s) {
		r.rootCallbacks.knownReturn.Store(&committedAgentReturn{runID: s.Run.ID, revision: s.Run.Revision, status: s.Run.Status, cause: cause})
	}
}

// knownAgentPhysicalSettlement validates a persisted Feature state, including
// nonterminal wait/resume points, without treating pending external work as
// an automatic no-effect receipt. This is stricter than a Kernel RunStatus.
func knownAgentPhysicalSettlement(snapshot kernel.Snapshot) bool {
	state, err := decodeState(snapshot.State)
	if err != nil || state.ToolDispatchID != "" || state.ToolWait != nil ||
		len(state.ExternalSignals) != 0 || len(state.DeferredChildren) != 0 || (state.ToolAdmission != nil && state.ToolAdmission.Phase == toolAdmissionEvaluating) {
		return false
	}
	for _, invocation := range state.ModelInvocations {
		if invocation.Status != ModelInvocationConsumed {
			return false
		}
	}
	return true
}

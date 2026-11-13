package agent

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// ErrModelOutcomeUnknown retains the original dispatched invocation until an
// authoritative receipt exists. Claim/Run lease TTL never authorizes a resend.
var ErrModelOutcomeUnknown = errors.New("agent external model result is uncertain")
var ErrRootOutcomeUnknown = errors.New("agent original root callback result is uncertain")

type rootCallbackState struct{ unknown atomic.Bool }

func (runner *Runner) noteRootCallbackFailure(err error) {
	if err != nil && runner.rootCallbacks != nil {
		runner.rootCallbacks.unknown.Store(true)
	}
}

// defaultRunExecution preserves optional configuration, not unowned dispatch.
func defaultRunExecution(rt *kernel.Runtime, policy ExecutionPolicy) (*RunExecution, error) {
	owner, err := rt.NewID("agentowner")
	if err != nil {
		return nil, err
	}
	checkedAdd := func(a, b time.Duration) (time.Duration, bool) {
		if a < 0 || b < 0 || a > time.Duration(math.MaxInt64)-b {
			return 0, false
		}
		return a + b, true
	}
	attempts := time.Duration(policy.Tool.MaxAttempts)
	if attempts <= 0 || policy.Tool.Timeout > time.Duration(math.MaxInt64)/attempts {
		return nil, ErrInvalidRequest
	}
	toolWindow := policy.Tool.Timeout * attempts
	if attempts > 1 {
		delay, ok := checkedAdd(policy.Tool.MaxBackoff, policy.Tool.MaxBackoff/5)
		if !ok || delay > time.Duration(math.MaxInt64)/(attempts-1) {
			return nil, ErrInvalidRequest
		}
		toolWindow, ok = checkedAdd(toolWindow, delay*(attempts-1))
		if !ok {
			return nil, ErrInvalidRequest
		}
	}
	duration, ok := checkedAdd(max(policy.Model.Timeout, toolWindow), 5*time.Second)
	if !ok || duration > 24*time.Hour {
		return nil, ErrInvalidRequest
	}
	config := &RunExecution{OwnerID: owner, LeaseDuration: duration}
	if !validRunExecution(*config, policy) {
		return nil, ErrInvalidRequest
	}
	return config, nil
}

func knownRootPhysicalSettlement(snapshot kernel.Snapshot) bool {
	state, err := decodeState(snapshot.State)
	if err != nil || state.ToolDispatchID != "" || state.ToolWait != nil || len(state.DeferredChildren) != 0 {
		return false
	}
	for _, inv := range state.ModelInvocations {
		switch inv.Status {
		case ModelInvocationCompleted, ModelInvocationConsumed:
			if !validModelInvocationReceipt(inv) {
				return false
			}
		case ModelInvocationPending:
			if inv.DispatchAttempt != 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// withRootSegment encompasses middleware before/after Next, never just drive.
func (runner *Runner) withRootSegment(ctx context.Context, snapshot kernel.Snapshot, operation string, execute func(context.Context, *Runner, kernel.Snapshot) (kernel.Snapshot, error)) (kernel.Snapshot, error) {
	if snapshot.Run.ParentRunID != "" {
		return execute(ctx, runner, snapshot)
	}
	if runner.activeRootGrant != nil {
		return execute(ctx, runner, snapshot)
	}
	if !runner.runtime.SupportsOwnedPhysicalExecution() {
		return snapshot, ErrInvalidRequest
	}
	if _, err := runner.runtime.LookupChildExecution(ctx, snapshot.Run.ID); err == nil {
		return snapshot, ErrRootOutcomeUnknown
	} else if !errors.Is(err, kernel.ErrNotFound) {
		return snapshot, err
	}
	lease, external := runner.runtime.BoundExecutionLease()
	var err error
	if external {
		if lease.RunID != snapshot.Run.ID {
			return snapshot, kernel.ErrExecutionConflict
		}
		// Store validates native authority atomically again at owned admission.
	} else {
		lease, err = runner.runtime.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: snapshot.Run.ID, OwnerID: runner.runExecution.OwnerID, LeaseDuration: runner.runExecution.LeaseDuration})
		if err != nil {
			return snapshot, err
		}
	}
	bound, err := runner.runtime.WithExecution(lease)
	if err != nil {
		return snapshot, err
	}
	abort := func(cause error) (kernel.Snapshot, error) {
		if !external {
			cause = errors.Join(cause, runner.runtime.ReleaseExecution(context.WithoutCancel(ctx), lease))
		}
		return snapshot, cause
	}
	current, err := bound.Load(ctx, snapshot.Run.ID)
	if err != nil {
		return abort(err)
	}
	if current.Run.Revision != snapshot.Run.Revision {
		return abort(kernel.ErrConflict)
	}

	proof := featurecontrol.Seal(current.Run.ID, current.Run.Revision, "agent_owned_physical_admission", lease.LeaseID, current.State, current.Checkpoint)
	grant, err := bound.BeginOwnedPhysicalExecution(ctx, current.Run.ID, current.Run.Revision, proof, operation)
	if err != nil {
		return abort(err)
	}

	ownerProof := featurecontrol.Seal(current.Run.ID, current.Run.Revision, "agent_physical_owner", grant.ID, current.State, current.Checkpoint)
	scopedRT, err := bound.WithPhysicalGrant(ctx, grant, ownerProof)
	if err != nil {
		return current, errors.Join(ErrRootOutcomeUnknown, err)
	}
	return runner.executeOwnedRoot(ctx, current, bound, scopedRT, grant, lease, external, execute)
}

func (runner *Runner) executeOwnedRoot(ctx context.Context, current kernel.Snapshot, bound, scopedRT *kernel.Runtime, grant kernel.ChildExecutionGrant, lease kernel.ExecutionLease, external bool, execute func(context.Context, *Runner, kernel.Snapshot) (kernel.Snapshot, error)) (kernel.Snapshot, error) {
	scoped := *runner
	scoped.runtime = scopedRT
	scoped.activeRunLease = &lease
	scoped.activeRootGrant = &grant
	scoped.rootCallbacks = &rootCallbackState{}
	scoped.externalRunLease = external
	lifecycle := callbackscope.New(callbackscope.Binding{RunID: grant.ChildRunID, GrantID: grant.ID, OwnerID: grant.ExecutionOwnerID, LeaseID: grant.ExecutionLeaseID, Epoch: grant.ExecutionEpoch, SourceHash: grant.AdmissionSourceHash, Operation: grant.AdmissionOperation}, runner.boundedCallbacks)
	defer lifecycle.Close()
	result, callErr := execute(callbackscope.Bind(ctx, lifecycle), &scoped, current)
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	latest, loadErr := scopedRT.Load(cleanup, current.Run.ID)
	if loadErr != nil {
		return result, errors.Join(callErr, ErrRootOutcomeUnknown, loadErr)
	}
	hostComplete := lifecycle.Complete() && !scoped.rootCallbacks.unknown.Load()
	settled := hostComplete && knownRootPhysicalSettlement(latest)
	if !settled {
		outcome := kernel.OwnedPhysicalCallbackOutcome{HostCompleted: hostComplete, LeaseOwned: !external}
		payload := kernel.OwnedPhysicalCallbackReturnPayload(latest, outcome)
		returned := featurecontrol.Seal(latest.Run.ID, latest.Run.Revision, "agent_owned_callback_return", grant.ID, payload, nil)
		if recordErr := bound.RecordOwnedPhysicalCallbackReturn(cleanup, grant, latest.Run.Revision, outcome, returned); recordErr != nil {
			return latest, errors.Join(callErr, ErrRootOutcomeUnknown, recordErr)
		}
		state, stateErr := decodeState(latest.State)
		if callErr == nil && hostComplete && stateErr == nil && (state.ToolDispatchID != "" || len(state.DeferredChildren) != 0) {
			recordPhysicalObservation(ctx, latest.Run.ID, PhysicalCallDurableYield)
			return latest, nil
		}
		return latest, errors.Join(callErr, ErrRootOutcomeUnknown)
	}
	proof := featurecontrol.Seal(latest.Run.ID, latest.Run.Revision, "agent_owned_physical_settled", grant.ID, latest.State, latest.Checkpoint)
	if err := bound.MarkChildExecutionSettled(cleanup, grant, proof); err != nil {
		// Mark may have committed before its ACK was lost. Finish accepts only
		// that exact marker; it does not infer settlement from clean state.
		completedProof := featurecontrol.Seal(latest.Run.ID, latest.Run.Revision, "managed_successor_completed", grant.ID, latest.State, latest)
		if completionErr := bound.CompleteManagedRecovery(cleanup, grant, latest, completedProof); completionErr != nil {
			return latest, errors.Join(callErr, ErrRootOutcomeUnknown, completionErr)
		}
		if finishErr := bound.FinishChildExecution(cleanup, grant, proof); finishErr == nil {
			if !external {
				if releaseErr := bound.ReleaseExecution(cleanup, lease); releaseErr != nil {
					return latest, errors.Join(callErr, releaseErr)
				}
			}
			recordPhysicalObservation(ctx, latest.Run.ID, PhysicalCallSettled)
			return latest, callErr
		}
		// The original callback really returned, even if its native lease
		// expired meanwhile. Record that fact without mutating the Run or
		// minting another physical owner, then use the separate recovery seam.
		outcome := kernel.OwnedPhysicalCallbackOutcome{HostCompleted: hostComplete, LeaseOwned: !external}
		payload := kernel.OwnedPhysicalCallbackReturnPayload(latest, outcome)
		returned := featurecontrol.Seal(latest.Run.ID, latest.Run.Revision, "agent_owned_callback_return", grant.ID, payload, nil)
		if recordErr := bound.RecordOwnedPhysicalCallbackReturn(cleanup, grant, latest.Run.Revision, outcome, returned); recordErr != nil {
			return latest, errors.Join(callErr, ErrRootOutcomeUnknown, err, recordErr)
		}
		if recoverErr := runner.ReconcileRootPhysicalExecution(cleanup, latest.Run.ID); recoverErr != nil {
			return latest, errors.Join(callErr, ErrRootOutcomeUnknown, err, recoverErr)
		}
		recordPhysicalObservation(ctx, latest.Run.ID, PhysicalCallSettled)
		return latest, callErr
	}
	completedProof := featurecontrol.Seal(latest.Run.ID, latest.Run.Revision, "managed_successor_completed", grant.ID, latest.State, latest)
	if completionErr := bound.CompleteManagedRecovery(cleanup, grant, latest, completedProof); completionErr != nil {
		return latest, errors.Join(callErr, ErrRootOutcomeUnknown, completionErr)
	}
	if err := bound.FinishChildExecution(cleanup, grant, proof); err != nil {
		return latest, errors.Join(callErr, ErrRootOutcomeUnknown, err)
	}
	if !external {
		if err := bound.ReleaseExecution(cleanup, lease); err != nil {
			return latest, errors.Join(callErr, err)
		}
	}
	recordPhysicalObservation(ctx, latest.Run.ID, PhysicalCallSettled)
	return latest, callErr
}

// ReconcileRootPhysicalExecution retires only an already marked whole callback;
// clean receipts alone never manufacture a missing Host completion marker.
func (runner *Runner) ReconcileRootPhysicalExecution(ctx context.Context, runID string) error {
	if runner == nil || runner.runtime == nil {
		return ErrInvalidRequest
	}
	snapshot, err := runner.runtime.Load(ctx, runID)
	if err != nil {
		return err
	}
	if snapshot.Run.Kind != RunKind || snapshot.Run.ParentRunID != "" {
		return ErrInvalidRequest
	}
	grant, err := runner.runtime.LookupChildExecution(ctx, runID)
	if err != nil {
		return err
	}
	if grant.ExecutionLeaseID == "" || !knownRootPhysicalSettlement(snapshot) {
		return ErrRootOutcomeUnknown
	}
	completion, completionErr := runner.runtime.LookupOwnedPhysicalCallbackReturn(ctx, grant)
	if completionErr == nil && completion.HostCompleted {
		recovered := featurecontrol.Seal(runID, snapshot.Run.Revision, "agent_owned_physical_recovered", grant.ID, snapshot.State, snapshot.Checkpoint)
		if err = runner.runtime.MarkOwnedPhysicalRecovered(ctx, grant, recovered); err != nil {
			return errors.Join(ErrRootOutcomeUnknown, err)
		}
	} else if completionErr != nil && !errors.Is(completionErr, kernel.ErrNotFound) {
		return completionErr
	}
	proof := featurecontrol.Seal(runID, snapshot.Run.Revision, "agent_owned_physical_settled", grant.ID, snapshot.State, snapshot.Checkpoint)
	if err = runner.runtime.FinishChildExecution(ctx, grant, proof); err != nil {
		return errors.Join(ErrRootOutcomeUnknown, err)
	}
	if completionErr == nil && completion.LeaseOwned {
		lease := kernel.ExecutionLease{RunID: grant.ChildRunID, OwnerID: grant.ExecutionOwnerID, LeaseID: grant.ExecutionLeaseID, Epoch: grant.ExecutionEpoch, ExpiresAt: time.Now().UTC().Truncate(time.Millisecond)}
		if err = runner.runtime.ReleaseExecution(ctx, lease); err != nil && !errors.Is(err, kernel.ErrExecutionConflict) {
			return err
		}
	}
	return nil
}

func collectBoundedCallbacks(runs []plugin.RunMiddleware, models []plugin.ModelMiddleware, toolLayers []plugin.ToolMiddleware) []any {
	var result []any
	for _, m := range runs {
		switch value := m.(type) {
		case *BoundedContextAdapter:
			result = append(result, value)
		case *BoundedBudgetAdapter:
			result = append(result, value)
		}
	}
	for _, m := range models {
		switch value := m.(type) {
		case *BoundedBudgetAdapter:
			result = append(result, value)
		case *ModelAdmissionBarrier:
			result = append(result, value)
		}
	}
	for _, m := range toolLayers {
		switch value := m.(type) {
		case *BoundedBudgetAdapter:
			result = append(result, value)
		case *BoundedToolTimelineAdapter:
			result = append(result, value)
		}
	}
	return result
}

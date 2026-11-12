package agent

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// ErrModelOutcomeUnknown retains the original dispatched invocation until an
// authoritative receipt exists. Claim/Run lease TTL never authorizes a resend.
var ErrModelOutcomeUnknown = errors.New("agent external model result is uncertain")
var ErrRootOutcomeUnknown = errors.New("agent original root callback result is uncertain")

type rootSegmentState struct {
	GrantID          string `json:"grantID"`
	LeaseOwned       bool   `json:"leaseOwned,omitempty"`
	Operation        string `json:"operation"`
	CallbackReturned bool   `json:"callbackReturned,omitempty"`
	HostCompleted    bool   `json:"hostCompleted,omitempty"`
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
		// ACK loss cannot authorize a second admission or release an existing scope.
		existing, lookupErr := bound.LookupChildExecution(context.WithoutCancel(ctx), current.Run.ID)
		if lookupErr == nil && existing.ExecutionOwnerID == lease.OwnerID && existing.ExecutionLeaseID == lease.LeaseID && existing.ExecutionEpoch == lease.Epoch && existing.ExpectedRevision == current.Run.Revision && existing.AdmissionOperation == operation {
			grant = existing
		} else {
			return abort(err)
		}
	}
	ownerProof := featurecontrol.Seal(current.Run.ID, current.Run.Revision, "agent_physical_owner", grant.ID, current.State, current.Checkpoint)
	scopedRT, err := bound.WithPhysicalGrant(ctx, grant, ownerProof)
	if err != nil {
		return current, errors.Join(ErrRootOutcomeUnknown, err)
	}
	scoped := *runner
	scoped.runtime = scopedRT
	scoped.activeRunLease = &lease
	scoped.activeRootGrant = &grant
	scoped.externalRunLease = external
	result, callErr := execute(ctx, &scoped, current)
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	latest, loadErr := scopedRT.Load(cleanup, current.Run.ID)
	if loadErr != nil {
		return result, errors.Join(callErr, ErrRootOutcomeUnknown, loadErr)
	}
	hostComplete := callErr == nil || !runner.runMiddlewareConfigured
	// Persist callback-returned only while the Run remains writable. Terminal
	// clean completion is recorded by Mark; absence of that marker remains unknown.
	if latest.Run.Status == kernel.RunStatusRunning {
		lastState, decodeErr := decodeState(latest.State)
		if decodeErr == nil {
			lastState.RootSegment = &rootSegmentState{GrantID: grant.ID, Operation: operation, LeaseOwned: !external}
			lastState.RootSegment.CallbackReturned = true
			lastState.RootSegment.HostCompleted = hostComplete
			value, encodeErr := encodeState(lastState)
			if encodeErr == nil {
				updated, applyErr := scopedRT.Apply(cleanup, latest.Run.ID, latest.Run.Revision, kernel.Mutation{Status: latest.Run.Status, State: value, Checkpoint: latest.Checkpoint})
				if applyErr != nil {
					return latest, errors.Join(callErr, ErrRootOutcomeUnknown, applyErr)
				}
				latest = updated
			}
		}
	}
	if !hostComplete || !knownRootPhysicalSettlement(latest) {
		// A durable pending descriptor is a legitimate synchronous result, not
		// a settled physical scope. Keep the barrier without changing nil into
		// a fatal error; subsequent Resume still cannot redispatch it.
		state, stateErr := decodeState(latest.State)
		if callErr == nil && hostComplete && stateErr == nil && (state.ToolWait != nil || len(state.DeferredChildren) != 0) {
			return latest, nil
		}
		return latest, errors.Join(callErr, ErrRootOutcomeUnknown)
	}
	proof = featurecontrol.Seal(latest.Run.ID, latest.Run.Revision, "agent_owned_physical_settled", grant.ID, latest.State, latest.Checkpoint)
	if err = bound.MarkChildExecutionSettled(cleanup, grant, proof); err != nil {
		return latest, errors.Join(callErr, ErrRootOutcomeUnknown, err)
	}
	if err = bound.FinishChildExecution(cleanup, grant, proof); err != nil {
		return latest, errors.Join(callErr, ErrRootOutcomeUnknown, err)
	}
	if !external {
		if err = bound.ReleaseExecution(cleanup, lease); err != nil {
			return latest, errors.Join(callErr, err)
		}
	}
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
	state, decodeErr := decodeState(snapshot.State)
	if decodeErr != nil {
		return decodeErr
	}
	// An original, persisted callback-returned fact permits receipt recovery;
	// a clean snapshot or a previous segment's fact never does.
	if state.RootSegment != nil && state.RootSegment.GrantID == grant.ID && state.RootSegment.CallbackReturned && state.RootSegment.HostCompleted {
		recovered := featurecontrol.Seal(runID, snapshot.Run.Revision, "agent_owned_physical_recovered", grant.ID, snapshot.State, snapshot.Checkpoint)
		if err = runner.runtime.MarkOwnedPhysicalRecovered(ctx, grant, recovered); err != nil {
			return errors.Join(ErrRootOutcomeUnknown, err)
		}
	}
	proof := featurecontrol.Seal(runID, snapshot.Run.Revision, "agent_owned_physical_settled", grant.ID, snapshot.State, snapshot.Checkpoint)
	if err = runner.runtime.FinishChildExecution(ctx, grant, proof); err != nil {
		return errors.Join(ErrRootOutcomeUnknown, err)
	}
	if state.RootSegment != nil && state.RootSegment.GrantID == grant.ID && state.RootSegment.LeaseOwned {
		lease := kernel.ExecutionLease{RunID: grant.ChildRunID, OwnerID: grant.ExecutionOwnerID, LeaseID: grant.ExecutionLeaseID, Epoch: grant.ExecutionEpoch, ExpiresAt: time.Now().UTC()}
		if err = runner.runtime.ReleaseExecution(ctx, lease); err != nil && !errors.Is(err, kernel.ErrExecutionConflict) {
			return err
		}
	}
	return nil
}

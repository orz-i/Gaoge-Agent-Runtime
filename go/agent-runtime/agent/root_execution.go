package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	if !runner.runtime.SupportsOwnedPhysicalExecution() || !runner.runtime.SupportsOwnedRetirement() {
		return snapshot, kernel.ErrExecutionRequired
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
	outcome := kernel.OwnedPhysicalCallbackOutcome{HostCompleted: hostComplete, LeaseOwned: !external}
	payload := kernel.OwnedPhysicalCallbackReturnPayload(latest, outcome)
	returned := featurecontrol.Seal(latest.Run.ID, latest.Run.Revision, "agent_owned_callback_return", grant.ID, payload, nil)
	hash := sha256.Sum256(payload)
	completion := kernel.OwnedPhysicalCallbackCompletion{
		GrantID: grant.ID, ReturnRevision: latest.Run.Revision, ReturnSourceHash: hex.EncodeToString(hash[:]),
		HostCompleted: hostComplete, LeaseOwned: !external,
	}
	// The actual whole callback owns this fact, including the fast-known path.
	// Persist it before Mark so any competing recovery retains the same original
	// lease disposition. Public state or a clean receipt cannot manufacture it.
	if recordErr := bound.RecordOwnedPhysicalCallbackReturn(cleanup, grant, latest.Run.Revision, outcome, returned); recordErr != nil {
		if settled {
			if found, ackErr := acknowledgeExactOwnedRetirement(cleanup, bound, grant, latest, &completion); found {
				if ackErr != nil {
					return latest, errors.Join(callErr, ErrRootOutcomeUnknown, recordErr, ackErr)
				}
				recordPhysicalObservation(ctx, latest.Run.ID, PhysicalCallSettled)
				return latest, callErr
			}
		}
		// A lost Record ACK may leave the identical immutable fact on the active
		// grant. Only that exact fact allows this call to continue.
		stored, lookupErr := bound.LookupOwnedPhysicalCallbackReturn(cleanup, grant)
		if lookupErr != nil || stored != completion {
			return latest, errors.Join(callErr, ErrRootOutcomeUnknown, recordErr, lookupErr)
		}
	}
	if !settled {
		state, stateErr := decodeState(latest.State)
		if callErr == nil && hostComplete && stateErr == nil && (state.ToolDispatchID != "" || len(state.DeferredChildren) != 0) {
			recordPhysicalObservation(ctx, latest.Run.ID, PhysicalCallDurableYield)
			return latest, nil
		}
		return latest, errors.Join(callErr, ErrRootOutcomeUnknown)
	}
	if err := settleOwnedRoot(cleanup, bound, grant, latest, &completion, true); err != nil {
		return latest, errors.Join(callErr, ErrRootOutcomeUnknown, err)
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
	if errors.Is(err, kernel.ErrNotFound) {
		// A process may die after genuine Finish but before ACK. This narrow
		// current-run index returns only a unique RETIRED receipt still matching
		// the complete source/native owner and with no replacement grants. It
		// never selects a historical ACK or grants dispatch permission.
		receipt, lookupErr := runner.runtime.LookupOwnedRetirementForRun(ctx, runID)
		if lookupErr != nil {
			return lookupErr
		}
		if !knownRootPhysicalSettlement(snapshot) || !receipt.Evidence.Completion.HostCompleted || receipt.Evidence.Completion.GrantID == "" {
			return ErrRootOutcomeUnknown
		}
		found, ackErr := acknowledgeExactOwnedRetirement(ctx, runner.runtime, receipt.Evidence.Grant, snapshot, &receipt.Evidence.Completion)
		if ackErr != nil {
			return errors.Join(ErrRootOutcomeUnknown, ackErr)
		}
		if !found {
			return errors.Join(ErrRootOutcomeUnknown, kernel.ErrExecutionConflict)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if grant.ExecutionLeaseID == "" || !knownRootPhysicalSettlement(snapshot) {
		return ErrRootOutcomeUnknown
	}
	completion, completionErr := runner.runtime.LookupOwnedPhysicalCallbackReturn(ctx, grant)
	if completionErr != nil && !errors.Is(completionErr, kernel.ErrNotFound) {
		return completionErr
	}
	var original *kernel.OwnedPhysicalCallbackCompletion
	if completionErr == nil {
		if !completion.HostCompleted {
			return ErrRootOutcomeUnknown
		}
		original = &completion
	}
	if err := settleOwnedRoot(ctx, runner.runtime, grant, snapshot, original, false); err != nil {
		return errors.Join(ErrRootOutcomeUnknown, err)
	}
	return nil
}

// settleOwnedRoot keeps the active-grant T4 path separate from historical ACK.
// A peer's validated retirement already proves T4 and must never be followed by
// another CompleteManagedRecovery call requiring the now-deleted grant.
func settleOwnedRoot(ctx context.Context, rt *kernel.Runtime, grant kernel.ChildExecutionGrant, snapshot kernel.Snapshot, completion *kernel.OwnedPhysicalCallbackCompletion, owner bool) error {
	if found, err := acknowledgeExactOwnedRetirement(ctx, rt, grant, snapshot, completion); found {
		return err
	}
	proof := featurecontrol.Seal(snapshot.Run.ID, snapshot.Run.Revision, "agent_owned_physical_settled", grant.ID, snapshot.State, snapshot.Checkpoint)
	var markErr error
	if owner {
		markErr = rt.MarkChildExecutionSettled(ctx, grant, proof)
	}
	if !owner || markErr != nil {
		if found, err := acknowledgeExactOwnedRetirement(ctx, rt, grant, snapshot, completion); found {
			if err != nil {
				return errors.Join(markErr, err)
			}
			return nil
		}
		if completion != nil {
			recovered := featurecontrol.Seal(snapshot.Run.ID, snapshot.Run.Revision, "agent_owned_physical_recovered", grant.ID, snapshot.State, snapshot.Checkpoint)
			markErr = errors.Join(markErr, rt.MarkOwnedPhysicalRecovered(ctx, grant, recovered))
		}
	}
	if markErr != nil {
		if found, err := acknowledgeExactOwnedRetirement(ctx, rt, grant, snapshot, completion); found {
			if err != nil {
				return errors.Join(markErr, err)
			}
			return nil
		}
		// Both Mark responses may be lost after their actual commits. Continue
		// with the existing proof-bound T4/Finish; those Store gates alone decide
		// whether a genuine marker exists. An error is not evidence of absence.
	}
	completed := featurecontrol.Seal(snapshot.Run.ID, snapshot.Run.Revision, "managed_successor_completed", grant.ID, snapshot.State, snapshot)
	if err := rt.CompleteManagedRecovery(ctx, grant, snapshot, completed); err != nil {
		if found, ackErr := acknowledgeExactOwnedRetirement(ctx, rt, grant, snapshot, completion); found {
			if ackErr != nil {
				return errors.Join(markErr, err, ackErr)
			}
			return nil
		}
		return errors.Join(markErr, err)
	}
	finishErr := rt.FinishChildExecution(ctx, grant, proof)
	if found, err := acknowledgeExactOwnedRetirement(ctx, rt, grant, snapshot, completion); found {
		if err != nil {
			return errors.Join(markErr, finishErr, err)
		}
		return nil
	}
	// Legacy genuine markers without a callback disposition remain retireable,
	// but never authorize a native lease release. New calls always have a fact.
	if finishErr == nil && completion == nil {
		return nil
	}
	if finishErr != nil {
		return errors.Join(markErr, finishErr)
	}
	return errors.Join(markErr, kernel.ErrExecutionConflict)
}

// The caller supplies the exact grant it admitted/loaded and settled source it
// actually observed. Only Store-validated retirement evidence can authorize ACK.
// The canonical payload is identical for original-owner and recovery entrances.
func acknowledgeExactOwnedRetirement(ctx context.Context, rt *kernel.Runtime, grant kernel.ChildExecutionGrant, snapshot kernel.Snapshot, completion *kernel.OwnedPhysicalCallbackCompletion) (bool, error) {
	if !rt.SupportsOwnedRetirement() {
		return false, nil
	}
	receipt, err := rt.LoadOwnedRetirement(ctx, grant.ID)
	if errors.Is(err, kernel.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if receipt.Evidence.Grant != grant || kernel.ManagedDigest(receipt.Evidence.Snapshot) != kernel.ManagedDigest(snapshot) ||
		(completion != nil && receipt.Evidence.Completion != *completion) {
		return true, kernel.ErrExecutionConflict
	}
	if receipt.Evidence.Completion.GrantID == "" {
		if completion != nil {
			return true, kernel.ErrExecutionConflict
		}
		return true, nil // validated LEGACY_UNKNOWN: no ACK or release claim
	}
	proof := featurecontrol.Seal(grant.ChildRunID, snapshot.Run.Revision, "agent_owned_retirement_ack", grant.ID, kernel.OwnedRetirementAcknowledgementPayload(receipt), nil)
	result, err := rt.AcknowledgeOwnedRetirement(ctx, grant, proof)
	if err == nil {
		return true, validateOwnedRetirementACK(result, receipt)
	}
	// An ACK may atomically release the original lease and lose its response.
	// Retry only the identical already-committed canonical ACK, never an absent
	// receipt or a first-ACK current-source/epoch conflict.
	stored, loadErr := rt.LoadOwnedRetirement(ctx, grant.ID)
	if loadErr != nil || stored.Phase != "ACKNOWLEDGED" || stored.Identity() != receipt.Identity() {
		return true, errors.Join(err, loadErr)
	}
	result, retryErr := rt.AcknowledgeOwnedRetirement(ctx, grant, proof)
	if retryErr != nil {
		return true, retryErr
	}
	return true, validateOwnedRetirementACK(result, receipt)
}

func validateOwnedRetirementACK(result kernel.OwnedRetirementAckResult, receipt kernel.OwnedRetirementReceipt) error {
	disposition := "preserved_borrowed"
	if receipt.Evidence.Completion.LeaseOwned {
		disposition = "released_owned"
	}
	if result.ReceiptID != receipt.Evidence.Grant.ID || result.SettledSourceDigest != kernel.ManagedDigest(receipt.Evidence.Snapshot) || result.Disposition != disposition {
		return kernel.ErrExecutionConflict
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

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
)

// ErrModelOutcomeUnknown retains the original dispatched invocation until an
// authoritative receipt exists. Claim/Run lease TTL never authorizes a resend.
var ErrModelOutcomeUnknown = errors.New("agent external model result is uncertain")
var ErrRootOutcomeUnknown = errors.New("agent original root callback result is uncertain")

type rootCallbackState struct {
	entered               atomic.Bool
	unknown               atomic.Bool
	approvalPolicyEntered atomic.Bool
	inputReadEntered      atomic.Bool
	summaryPhaseEntered   atomic.Bool
	toolApprovals         approvedToolCalls
	knownReturn           atomic.Pointer[committedAgentReturn]
}

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
	return err == nil && knownRootStatePhysicalSettlement(state)
}

// Callers that already validated the exact aggregate can use the same
// settlement predicate without decoding and restoring opaque evidence again.
func knownRootStatePhysicalSettlement(state runState) bool {
	if state.ToolDispatchID != "" || state.ToolWait != nil || len(state.DeferredChildren) != 0 || (state.ToolAdmission != nil && state.ToolAdmission.Phase == toolAdmissionEvaluating) {
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
	dispatchID := runner.originalPhysicalDispatch(ctx, snapshot.Run.ID, operation)
	var q kernel.ExecutionAcquireRequest
	if !external {
		q = kernel.ExecutionAcquireRequest{RunID: snapshot.Run.ID, OwnerID: runner.runExecution.OwnerID, LeaseDuration: runner.runExecution.LeaseDuration}
	}
	proof := featurecontrol.Seal(snapshot.Run.ID, snapshot.Run.Revision, "agent_owned_physical_admission", kernel.OwnedPhysicalAdmissionSubject(q, lease, operation, dispatchID), snapshot.State, snapshot)
	scopedRT, admitted, err := runner.runtime.AdmitOwnedPhysicalExecution(ctx, snapshot, q, proof, operation, dispatchID)
	if err != nil {
		if errors.Is(err, kernel.ErrPhysicalAdmissionCommitUnknown) {
			err = errors.Join(ErrRootOutcomeUnknown, err)
		}
		return snapshot, err
	}
	return runner.executeOwnedRoot(ctx, snapshot, scopedRT, admitted.Grant, admitted.Lease, external, execute)
}

func (runner *Runner) executeOwnedRoot(ctx context.Context, current kernel.Snapshot, scopedRT *kernel.Runtime, grant kernel.ChildExecutionGrant, lease kernel.ExecutionLease, external bool, execute func(context.Context, *Runner, kernel.Snapshot) (kernel.Snapshot, error)) (kernel.Snapshot, error) {
	scoped := *runner
	scoped.runtime = scopedRT
	scoped.activeRunLease = &lease
	scoped.activeRootGrant = &grant
	scoped.rootCallbacks = &rootCallbackState{}
	scoped.externalRunLease = external
	lifecycle := callbackscope.New(callbackscope.Binding{RunID: grant.ChildRunID, GrantID: grant.ID, OwnerID: grant.ExecutionOwnerID, LeaseID: grant.ExecutionLeaseID, Epoch: grant.ExecutionEpoch, SourceHash: grant.AdmissionSourceHash, Operation: grant.AdmissionOperation}, runner.boundedCallbacks)
	defer lifecycle.Close()
	physicalCtx := callbackscope.Bind(ctx, lifecycle)
	var toolFrame *originalToolFrame
	if grant.AdmissionOperation == "resume" || grant.AdmissionOperation == "start" || grant.AdmissionOperation == "approval" || grant.AdmissionOperation == "managed_resume" {
		toolFrame = &originalToolFrame{grantID: grant.ID, runID: grant.ChildRunID}
		physicalCtx = callbackscope.BindRootToolFrame(physicalCtx, toolFrame)
	}
	result, callErr := execute(physicalCtx, &scoped, current)
	if !scoped.rootCallbacks.entered.Load() {
		// All first-party entry closures reach invokeNativeRun before any
		// effect-capable callback. A prelude or entry denial is not a return.
		if callErr == nil {
			callErr = kernel.ErrExecutionConflict
		}
		return current, runner.retireUnenteredOwnedRoot(ctx, scopedRT, grant, current, external, callErr)
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	latest, loadErr := scopedRT.Load(cleanup, current.Run.ID)
	if loadErr != nil {
		return result, errors.Join(callErr, ErrRootOutcomeUnknown, loadErr)
	}
	state, stateErr := decodeState(latest.State)
	hostComplete := lifecycle.Complete() && !scoped.rootCallbacks.unknown.Load() && stateErr == nil &&
		(state.ToolAdmission == nil || state.ToolAdmission.Phase != toolAdmissionEvaluating)
	settled := hostComplete && knownRootStatePhysicalSettlement(state)
	outcome := kernel.OwnedPhysicalCallbackOutcome{HostCompleted: hostComplete, LeaseOwned: !external}
	pendingCall, pending, _ := FrozenPendingDispatch(latest)
	returnedCall := false
	allApproved := false
	if hostComplete {
		// Approval is captured at the actual Call return, not inferred
		// from a later wait representation or an observer's composition.
		bindings, approved, evidenceErr := toolFrame.outstanding(latest, &scoped.rootCallbacks.toolApprovals)
		if evidenceErr != nil {
			return latest, errors.Join(callErr, ErrRootOutcomeUnknown, evidenceErr)
		}
		outcome.ToolReturns = bindings
		allApproved = approved
		if b, found, _ := bindings.Find(pendingCall.ID); pending && found && (b.ReceiptDigest != "" || b.Unsent) {
			returnedCall = true
		}
	}
	// Control custody describes the actual whole executing callback. Original
	// outstanding approval evidence is sealed independently of the business CAS.
	// Qualification is about the actual returned execution, not whether
	// the Runner has an input source configured. A logical child with no
	// input binding has not entered that Host boundary. A Run that did
	// read the Host source still receives no deferred-control shortcut.
	scopedReturn := hostComplete && runner.scopedRootCallbacks &&
		!scoped.rootCallbacks.inputReadEntered.Load() && !scoped.rootCallbacks.summaryPhaseEntered.Load()
	if hostComplete && grant.AdmissionOperation == "prepare" && runner.trustedDeferredPreparedControl {
		outcome.ControlCustody = kernel.OwnedCustodyPrepared
	} else if scopedReturn && !scoped.rootCallbacks.approvalPolicyEntered.Load() {
		outcome.ControlCustody = kernel.OwnedCustodyScoped
	} else if scopedReturn && (settled || allApproved) && scoped.rootCallbacks.toolApprovals.allBound() {
		outcome.ControlCustody = kernel.OwnedToolResultReturn
		if runner.boundedApproval {
			outcome.ControlCustody = kernel.OwnedBoundedApprovalReturn
		}
	}
	payload := kernel.OwnedPhysicalCallbackReturnPayload(latest, outcome)
	returned := featurecontrol.Seal(latest.Run.ID, latest.Run.Revision, "agent_owned_callback_return", grant.ID, payload, nil)
	hash := sha256.Sum256(payload)
	completion := kernel.OwnedPhysicalCallbackCompletion{
		GrantID: grant.ID, ReturnRevision: latest.Run.Revision, ReturnSourceHash: hex.EncodeToString(hash[:]),
		HostCompleted: hostComplete, LeaseOwned: !external,
		ControlCustody: outcome.ControlCustody, ToolReturns: outcome.ToolReturns,
	}
	// The actual whole callback owns this fact, including the fast-known path.
	// Persist it before Finish so any competing recovery retains the same original
	// lease disposition. Public state or a clean receipt cannot manufacture it.
	if recordErr := scopedRT.RecordOwnedPhysicalCallbackReturn(cleanup, grant, latest.Run.Revision, outcome, returned); recordErr != nil {
		if settled {
			if found, ackErr := observeExactOwnedRetirement(cleanup, scopedRT, grant, latest, &completion); found {
				if ackErr != nil {
					return latest, errors.Join(callErr, ErrRootOutcomeUnknown, recordErr, ackErr)
				}
				recordPhysicalObservation(ctx, latest.Run.ID, PhysicalCallSettled)
				return latest, callErr
			}
		}
		// A lost Record ACK may leave the identical immutable fact on the active
		// grant. Only that exact fact allows this call to continue.
		stored, lookupErr := scopedRT.LookupOwnedPhysicalCallbackReturn(cleanup, grant)
		if lookupErr != nil || stored != completion {
			return latest, errors.Join(callErr, ErrRootOutcomeUnknown, recordErr, lookupErr)
		}
	}
	if !settled {
		// The enclosing native call has really returned. The Kernel grant still
		// fences the Tool until an independent receipt settles the original intent.
		if scopedReturn && pending && (runner.toolOutcomeVerifier != nil || returnedCall) {
			recordPhysicalObservation(ctx, latest.Run.ID, PhysicalCallDurableYield)
		}
		state, stateErr := decodeState(latest.State)
		if callErr == nil && hostComplete && stateErr == nil && (state.ToolDispatchID != "" || len(state.DeferredChildren) != 0) {
			recordPhysicalObservation(ctx, latest.Run.ID, PhysicalCallDurableYield)
			return latest, nil
		}
		return latest, errors.Join(callErr, ErrRootOutcomeUnknown)
	}
	if err := settleOwnedRoot(cleanup, scopedRT, grant, latest, &completion); err != nil {
		return latest, errors.Join(callErr, ErrRootOutcomeUnknown, err)
	}
	recordPhysicalObservation(ctx, latest.Run.ID, PhysicalCallSettled)
	return latest, callErr
}

// retireUnenteredOwnedRoot is reached ONLY by the acknowledged original
// invocation before execute is called. The unchanged frozen source is sealed
// as no-entry, not as a returned Host callback or Tool receipt. Managed
// successors must keep their separate selection/closure protocol; never
// manufacture a managed closure for an unentered managed continuation.
func (runner *Runner) retireUnenteredOwnedRoot(ctx context.Context, rt *kernel.Runtime, g kernel.ChildExecutionGrant, source kernel.Snapshot, external bool, cause error) error {
	if g.AdmissionOperation == "managed_resume" {
		return errors.Join(cause, ErrRootOutcomeUnknown)
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	outcome := kernel.OwnedPhysicalCallbackOutcome{LeaseOwned: !external, ControlCustody: kernel.OwnedCustodyNotEntered}
	payload := kernel.OwnedPhysicalCallbackReturnPayload(source, outcome)
	proof := featurecontrol.Seal(source.Run.ID, source.Run.Revision, "agent_owned_callback_not_entered", g.ID, payload, nil)
	hash := sha256.Sum256(payload)
	completion := kernel.OwnedPhysicalCallbackCompletion{GrantID: g.ID, ReturnRevision: source.Run.Revision, ReturnSourceHash: hex.EncodeToString(hash[:]), LeaseOwned: !external, ControlCustody: outcome.ControlCustody}
	if err := rt.RecordOwnedPhysicalCallbackReturn(cleanup, g, source.Run.Revision, outcome, proof); err != nil {
		if found, ackErr := observeExactOwnedRetirement(cleanup, rt, g, source, &completion); found {
			if ackErr == nil {
				recordPhysicalObservation(ctx, g.ChildRunID, PhysicalCallNotEntered)
				return cause
			}
			return errors.Join(cause, ErrRootOutcomeUnknown, err, ackErr)
		}
		stored, lookupErr := rt.LookupOwnedPhysicalCallbackReturn(cleanup, g)
		if lookupErr != nil || stored != completion {
			return errors.Join(cause, ErrRootOutcomeUnknown, err, lookupErr)
		}
	}
	if err := settleOwnedRoot(cleanup, rt, g, source, &completion); err != nil {
		return errors.Join(cause, ErrRootOutcomeUnknown, err)
	}
	recordPhysicalObservation(ctx, g.ChildRunID, PhysicalCallNotEntered)
	return cause
}

// ReconcileRootPhysicalExecution consumes the existing native retirement
// protocol after every original physical obligation has been settled. A
// clean feature state alone never manufactures a callback completion.
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
	grant, completion, err := runner.originalRootReturn(ctx, snapshot)
	if err != nil {
		return err
	}
	return runner.reconcileRootRetirement(ctx, snapshot, grant, completion)
}

// Select the actual active original return or the exact finished receipt at
// the current epoch and source. An absent Grant alone is not completion.
func (runner *Runner) originalRootReturn(ctx context.Context, snapshot kernel.Snapshot) (kernel.ChildExecutionGrant, *kernel.OwnedPhysicalCallbackCompletion, error) {
	grant, err := runner.runtime.LookupChildExecution(ctx, snapshot.Run.ID)
	if errors.Is(err, kernel.ErrNotFound) {
		receipt, lookupErr := runner.runtime.LookupOwnedRetirementForRun(ctx, snapshot.Run.ID)
		if lookupErr != nil {
			return grant, nil, lookupErr
		}
		if kernel.ManagedDigest(receipt.Evidence.Snapshot) != kernel.ManagedDigest(snapshot) ||
			(!receipt.Evidence.Completion.HostCompleted && !receipt.Evidence.Completion.MatchesNotEnteredSnapshot(receipt.Evidence.Grant, snapshot)) ||
			receipt.Evidence.Completion.GrantID == "" {
			return grant, nil, ErrRootOutcomeUnknown
		}
		return receipt.Evidence.Grant, &receipt.Evidence.Completion, nil
	}
	if err != nil {
		return grant, nil, err
	}
	if !grant.IsOwned() || grant.ExecutionLeaseID == "" || grant.ChildRunID != snapshot.Run.ID {
		return grant, nil, ErrRootOutcomeUnknown
	}
	completion, completionErr := runner.runtime.LookupOwnedPhysicalCallbackReturn(ctx, grant)
	if errors.Is(completionErr, kernel.ErrNotFound) {
		return grant, nil, nil
	}
	if completionErr != nil {
		return grant, nil, completionErr
	}
	if completion.MatchesNotEnteredSnapshot(grant, snapshot) {
		return grant, &completion, nil
	}
	if !completion.HostCompleted || completion.GrantID != grant.ID || completion.ReturnRevision < grant.ExpectedRevision || completion.ReturnRevision > snapshot.Run.Revision {
		return grant, nil, ErrRootOutcomeUnknown
	}
	return grant, &completion, nil
}

// Every receipt-only consumer uses the same original grant and exact
// settlement source for Finish. Re-resolving by RunID here
// could select a successor grant admitted after a concurrent retirement.
func (runner *Runner) reconcileRootRetirement(ctx context.Context, snapshot kernel.Snapshot, grant kernel.ChildExecutionGrant, completion *kernel.OwnedPhysicalCallbackCompletion) error {
	if completion != nil && completion.NotEntered() {
		if !completion.MatchesNotEnteredSnapshot(grant, snapshot) {
			return ErrRootOutcomeUnknown
		}
		return settleOwnedRoot(ctx, runner.runtime, grant, snapshot, completion)
	}
	if !grant.IsOwned() || grant.ChildRunID != snapshot.Run.ID || !knownRootPhysicalSettlement(snapshot) ||
		(completion != nil && (!completion.HostCompleted || completion.GrantID != grant.ID)) {
		return ErrRootOutcomeUnknown
	}
	if err := runner.requireRootModelQualification(ctx, snapshot); err != nil {
		return err
	}
	if err := settleOwnedRoot(ctx, runner.runtime, grant, snapshot, completion); err != nil {
		return errors.Join(ErrRootOutcomeUnknown, err)
	}
	return nil
}

func (runner *Runner) requireRootModelQualification(ctx context.Context, snapshot kernel.Snapshot) error {
	state, err := decodeState(snapshot.State)
	if err != nil {
		return err
	}
	if inv, ok := activeModelInvocation(state); ok {
		return runner.requireSettledModelQualification(ctx, *inv)
	}
	return nil
}

// settleOwnedRoot keeps active-grant retirement separate from historical receipt.
// A peer's validated retirement already proves closure and must never be followed by
// another settlement operation requiring the now-deleted grant.
func settleOwnedRoot(ctx context.Context, rt *kernel.Runtime, grant kernel.ChildExecutionGrant, snapshot kernel.Snapshot, completion *kernel.OwnedPhysicalCallbackCompletion) error {
	if completion == nil {
		return ErrRootOutcomeUnknown
	}
	if found, err := observeExactOwnedRetirement(ctx, rt, grant, snapshot, completion); found {
		return err
	}
	proof := featurecontrol.Seal(snapshot.Run.ID, snapshot.Run.Revision, "agent_owned_physical_settled", grant.ID, snapshot.State, snapshot)
	finishErr := rt.FinishChildExecution(ctx, grant, proof)
	if found, err := observeExactOwnedRetirement(ctx, rt, grant, snapshot, completion); found {
		if err != nil {
			return errors.Join(finishErr, err)
		}
		return nil
	}
	if finishErr != nil {
		return finishErr
	}
	return kernel.ErrExecutionConflict
}

// The original caller observes the one committed retirement receipt.
// No confirmation can write or release a lease; Finish already did so.
func observeExactOwnedRetirement(ctx context.Context, rt *kernel.Runtime, grant kernel.ChildExecutionGrant, snapshot kernel.Snapshot, completion *kernel.OwnedPhysicalCallbackCompletion) (bool, error) {
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
	return true, nil
}

func collectBoundedCallbacks(d Dependencies, nativeContext *BoundedContextAdapter, nativeBudget *BoundedBudgetAdapter) []any {
	var result []any
	if nativeContext != nil {
		result = append(result, nativeContext)
	}
	if nativeBudget != nil {
		result = append(result, nativeBudget)
	}
	if d.Inputs != nil {
		result = append(result, inputReadStage)
	}
	for _, m := range d.RunMiddleware {
		switch value := m.(type) {
		case *BoundedContextAdapter:
			result = append(result, value)
		case *BoundedBudgetAdapter:
			result = append(result, value)
		}
	}
	for _, m := range d.ModelMiddleware {
		switch value := m.(type) {
		case *BoundedBudgetAdapter:
			result = append(result, value)
		case *ModelAdmissionBarrier:
			result = append(result, value)
		}
	}
	for _, m := range d.ToolMiddleware {
		switch value := m.(type) {
		case *BoundedBudgetAdapter:
			result = append(result, value)
		case *BoundedToolTimelineAdapter:
			result = append(result, value)
		}
	}
	return result
}

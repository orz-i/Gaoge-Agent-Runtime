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
	return err == nil && knownPhysicalSettlement(state, true)
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
	return knownPhysicalSettlement(state, false)
}

// A borrowed Grant has no durable owned-return qualification for unconsumed
// Model receipts or external signals. Its settlement therefore stays strict.
func knownPhysicalSettlement(state runState, borrowed bool) bool {
	if state.ToolDispatchID != "" || state.ToolWait != nil || len(state.DeferredChildren) != 0 || (state.ToolAdmission != nil && state.ToolAdmission.Phase == toolAdmissionEvaluating) || (borrowed && len(state.ExternalSignals) != 0) {
		return false
	}
	for _, inv := range state.ModelInvocations {
		if borrowed {
			if inv.Status != ModelInvocationConsumed {
				return false
			}
			continue
		}
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

// withNativeSegment selects only the original native ownership constraint;
// all first-party execution enters the same callback lifecycle.
func (runner *Runner) withNativeSegment(ctx context.Context, snapshot kernel.Snapshot, operation string, execute func(context.Context, *Runner, kernel.Snapshot) (kernel.Snapshot, error)) (kernel.Snapshot, error) {
	rt := runner.runtime
	parented := snapshot.Run.ParentRunID != ""
	if parented {
		rt = rt.WithoutPhysicalGrant()
	}
	if !runner.runtime.SupportsPhysicalExecution() {
		return snapshot, kernel.ErrExecutionRequired
	}
	if !parented {
		if _, err := rt.LookupChildExecution(ctx, snapshot.Run.ID); err == nil {
			return snapshot, ErrRootOutcomeUnknown
		} else if !errors.Is(err, kernel.ErrNotFound) {
			return snapshot, err
		}
	}
	lease, external := rt.BoundExecutionLease()
	dispatchID := runner.originalPhysicalDispatch(ctx, snapshot.Run.ID, operation)
	var q kernel.ExecutionAcquireRequest
	if parented {
		lease = kernel.ExecutionLease{}
		external = false
		dispatchID = ""
	} else if !external {
		q = kernel.ExecutionAcquireRequest{RunID: snapshot.Run.ID, OwnerID: runner.runExecution.OwnerID, LeaseDuration: runner.runExecution.LeaseDuration}
	}
	subject := kernel.PhysicalAdmissionSubject(q, lease, operation, dispatchID)
	proof := featurecontrol.Seal(snapshot.Run.ID, snapshot.Run.Revision, "agent_owned_physical_admission", subject, snapshot.State, snapshot)
	scopedRT, admitted, err := rt.AdmitPhysicalExecution(ctx, snapshot, q, proof, operation, dispatchID)
	if err != nil {
		if errors.Is(err, kernel.ErrPhysicalAdmissionCommitUnknown) {
			err = errors.Join(ErrRootOutcomeUnknown, err)
		}
		return snapshot, err
	}
	if parented {
		sourceHash := kernel.ManagedDigest(struct {
			Snapshot  kernel.Snapshot
			Operation string
		}{snapshot, operation})
		return runner.executeNativeSegment(ctx, snapshot, scopedRT, admitted.Grant, nil, false, operation, sourceHash, execute)
	}
	return runner.executeNativeSegment(ctx, snapshot, scopedRT, admitted.Grant, &admitted.Lease, external, admitted.Grant.AdmissionOperation, admitted.Grant.AdmissionSourceHash, execute)
}

func (runner *Runner) executeNativeSegment(ctx context.Context, current kernel.Snapshot, scopedRT *kernel.Runtime, grant kernel.ChildExecutionGrant, lease *kernel.ExecutionLease, external bool, operation, sourceHash string, execute func(context.Context, *Runner, kernel.Snapshot) (kernel.Snapshot, error)) (kernel.Snapshot, error) {
	scoped := *runner
	scoped.runtime = scopedRT
	scoped.activeRunLease = lease
	scoped.activePhysicalGrant = &grant
	scoped.rootCallbacks = &rootCallbackState{}
	scoped.externalRunLease = external
	lifecycle := callbackscope.New(callbackscope.Binding{RunID: grant.ChildRunID, GrantID: grant.ID, OwnerID: grant.ExecutionOwnerID, LeaseID: grant.ExecutionLeaseID, Epoch: grant.ExecutionEpoch, SourceHash: sourceHash, Operation: operation}, runner.boundedCallbacks)
	defer lifecycle.Close()
	var toolFrame *originalToolFrame
	if grant.IsOwned() && (operation == "resume" || operation == "start" || operation == "approval" || operation == "managed_resume") {
		toolFrame = &originalToolFrame{runID: grant.ChildRunID}
	}
	result, callErr := execute(callbackscope.BindRootToolFrame(callbackscope.Bind(ctx, lifecycle), toolFrame), &scoped, current)
	if grant.IsOwned() && !scoped.rootCallbacks.entered.Load() {
		// All first-party entry closures reach invokeNativeRun before any
		// effect-capable callback. A prelude or entry denial is not a return.
		if callErr == nil {
			callErr = kernel.ErrExecutionConflict
		}
		return current, runner.retireUnenteredOwnedRoot(ctx, scopedRT, grant, current, external, callErr)
	}
	hostComplete := lifecycle.Complete() && !scoped.rootCallbacks.unknown.Load()
	unknown := ErrRootOutcomeUnknown
	if !grant.IsOwned() {
		unknown = kernel.ErrChildExecutionBusy
	}
	if !grant.IsOwned() && !hostComplete {
		return result, errors.Join(callErr, unknown)
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	latest, loadErr := scopedRT.Load(cleanup, current.Run.ID)
	if loadErr != nil {
		return result, errors.Join(callErr, unknown, loadErr)
	}
	state, stateErr := decodeState(latest.State)
	hostComplete = hostComplete && stateErr == nil && (state.ToolAdmission == nil || state.ToolAdmission.Phase != toolAdmissionEvaluating)
	settled := hostComplete && knownPhysicalSettlement(state, !grant.IsOwned())
	outcome := kernel.PhysicalCallbackOutcome{HostCompleted: hostComplete}
	var scopedReturn, pending, returnedCall bool
	if !grant.IsOwned() {
		if !settled || (callErr != nil && !scoped.rootCallbacks.knownReturn.Load().matches(result, latest, callErr)) {
			return result, errors.Join(callErr, unknown)
		}
	} else {
		outcome.LeaseOwned = !external
		pendingCall, hasPending, _ := FrozenPendingDispatch(latest)
		pending = hasPending
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
		scopedReturn = hostComplete && runner.scopedRootCallbacks &&
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
	}
	completion, retired, err := recordPhysicalReturn(cleanup, scopedRT, grant, latest, outcome, settled)
	observed := latest
	if !grant.IsOwned() {
		observed = result
	}
	if err != nil {
		return observed, errors.Join(callErr, unknown, err)
	}
	if !settled {
		if scopedReturn && pending && (runner.toolOutcomeVerifier != nil || returnedCall) {
			recordPhysicalObservation(ctx, latest.Run.ID, PhysicalCallDurableYield)
		}
		if callErr == nil && hostComplete && (state.ToolDispatchID != "" || len(state.DeferredChildren) != 0) {
			recordPhysicalObservation(ctx, latest.Run.ID, PhysicalCallDurableYield)
			return latest, nil
		}
		return latest, errors.Join(callErr, unknown)
	}
	if !retired {
		if err = finishPhysicalReturn(cleanup, scopedRT, grant, latest, &completion); err != nil {
			return observed, errors.Join(callErr, unknown, err)
		}
	}
	recordPhysicalObservation(ctx, latest.Run.ID, PhysicalCallSettled)
	return observed, callErr
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
	outcome := kernel.PhysicalCallbackOutcome{LeaseOwned: !external, ControlCustody: kernel.OwnedCustodyNotEntered}
	completion, retired, err := recordPhysicalReturn(cleanup, rt, g, source, outcome, true)
	if err != nil {
		return errors.Join(cause, ErrRootOutcomeUnknown, err)
	}
	if !retired {
		if err := finishPhysicalReturn(cleanup, rt, g, source, &completion); err != nil {
			return errors.Join(cause, ErrRootOutcomeUnknown, err)
		}
	}
	recordPhysicalObservation(ctx, g.ChildRunID, PhysicalCallNotEntered)
	return cause
}

// Record the exact original return before Finish. Both callback and no-entry
// retirement recover a lost write ACK only from the same immutable completion.
func recordPhysicalReturn(ctx context.Context, rt *kernel.Runtime, g kernel.ChildExecutionGrant, s kernel.Snapshot, o kernel.PhysicalCallbackOutcome, settled bool) (kernel.PhysicalCallbackCompletion, bool, error) {
	domain := "agent_physical_callback_return"
	if o.ControlCustody == kernel.OwnedCustodyNotEntered {
		domain = "agent_owned_callback_not_entered"
	}
	payload := kernel.PhysicalCallbackReturnPayload(s, o)
	proof := featurecontrol.Seal(s.Run.ID, s.Run.Revision, domain, g.ID, payload, nil)
	c := kernel.PhysicalCallbackCompletion{GrantID: g.ID, ReturnRevision: s.Run.Revision, ReturnSourceHash: kernel.ManagedDigest(payload), HostCompleted: o.HostCompleted, LeaseOwned: o.LeaseOwned, ControlCustody: o.ControlCustody, ToolReturns: o.ToolReturns}
	if err := rt.RecordPhysicalCallbackReturn(ctx, g, s.Run.Revision, o, proof); err != nil {
		// Borrowed returns do not retain a retirement payload. Preserve the
		// original ACK error and require explicit recovery of that return.
		if !g.IsOwned() {
			return c, false, err
		}
		if settled {
			if found, ackErr := observeExactOwnedRetirement(ctx, rt, g, s, &c); found {
				if ackErr != nil {
					return c, false, errors.Join(err, ackErr)
				}
				return c, true, nil
			}
		}
		stored, lookupErr := rt.LookupPhysicalCallbackReturn(ctx, g)
		if lookupErr != nil || stored != c {
			return c, false, errors.Join(err, lookupErr)
		}
	}
	return c, false, nil
}

// ReconcilePhysicalExecution consumes the original physical return after its
// obligations have settled. The persisted Run selects its ownership constraint;
// no caller-selected mode, clean snapshot, or expired lease proves a return.
// This receipt-only operation never dispatches Models, Tools or child Runs.
func (runner *Runner) ReconcilePhysicalExecution(ctx context.Context, runID string) error {
	if runner == nil || runner.runtime == nil {
		return ErrInvalidRequest
	}
	snapshot, err := runner.runtime.Load(ctx, runID)
	if err != nil {
		return err
	}
	if snapshot.Run.Kind != RunKind {
		return ErrInvalidRequest
	}
	if snapshot.Run.ParentRunID != "" {
		if !knownAgentPhysicalSettlement(snapshot) {
			return kernel.ErrChildExecutionBusy
		}
		grant, err := runner.runtime.LookupChildExecution(ctx, snapshot.Run.ID)
		if err != nil {
			return err
		}
		err = finishPhysicalReturn(ctx, runner.runtime, grant, snapshot, nil)
		if errors.Is(err, kernel.ErrExecutionConflict) {
			// A clean-looking Run cannot retire the Grant without its original
			// post-callback return fact. This remains quarantined, not a
			// generic no-effect conflict eligible for automatic retry.
			return errors.Join(kernel.ErrChildExecutionBusy, err)
		}
		return err
	}
	grant, completion, err := runner.originalRootReturn(ctx, snapshot)
	if err != nil {
		return err
	}
	return runner.reconcileRootRetirement(ctx, snapshot, grant, completion)
}

// Select the actual active original return or the exact finished receipt at
// the current epoch and source. An absent Grant alone is not completion.
func (runner *Runner) originalRootReturn(ctx context.Context, snapshot kernel.Snapshot) (kernel.ChildExecutionGrant, *kernel.PhysicalCallbackCompletion, error) {
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
	completion, completionErr := runner.runtime.LookupPhysicalCallbackReturn(ctx, grant)
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
func (runner *Runner) reconcileRootRetirement(ctx context.Context, snapshot kernel.Snapshot, grant kernel.ChildExecutionGrant, completion *kernel.PhysicalCallbackCompletion) error {
	if completion != nil && completion.NotEntered() {
		if !completion.MatchesNotEnteredSnapshot(grant, snapshot) {
			return ErrRootOutcomeUnknown
		}
		return finishPhysicalReturn(ctx, runner.runtime, grant, snapshot, completion)
	}
	if !grant.IsOwned() || grant.ChildRunID != snapshot.Run.ID || !knownRootPhysicalSettlement(snapshot) ||
		(completion != nil && (!completion.HostCompleted || completion.GrantID != grant.ID)) {
		return ErrRootOutcomeUnknown
	}
	if err := runner.requireRootModelQualification(ctx, snapshot); err != nil {
		return err
	}
	if err := finishPhysicalReturn(ctx, runner.runtime, grant, snapshot, completion); err != nil {
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

// finishPhysicalReturn keeps active-grant retirement separate from historical receipt.
// A peer's validated retirement already proves closure and must never be followed by
// another settlement operation requiring the now-deleted grant.
func finishPhysicalReturn(ctx context.Context, rt *kernel.Runtime, grant kernel.ChildExecutionGrant, snapshot kernel.Snapshot, completion *kernel.PhysicalCallbackCompletion) error {
	if !grant.IsOwned() {
		return rt.FinishChildExecution(ctx, grant, physicalSettlementProof(grant, snapshot))
	}
	if completion == nil {
		return ErrRootOutcomeUnknown
	}
	if found, err := observeExactOwnedRetirement(ctx, rt, grant, snapshot, completion); found {
		return err
	}
	proof := physicalSettlementProof(grant, snapshot)
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

func physicalSettlementProof(grant kernel.ChildExecutionGrant, s kernel.Snapshot) *featurecontrol.Proof {
	return featurecontrol.Seal(s.Run.ID, s.Run.Revision, "agent_physical_settled", grant.ID, s.State, s)
}

// The original caller observes the one committed retirement receipt.
// No confirmation can write or release a lease; Finish already did so.
func observeExactOwnedRetirement(ctx context.Context, rt *kernel.Runtime, grant kernel.ChildExecutionGrant, snapshot kernel.Snapshot, completion *kernel.PhysicalCallbackCompletion) (bool, error) {
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

func collectBoundedCallbacks(d Dependencies) []any {
	var result []any
	if d.BoundedContext != nil {
		result = append(result, d.BoundedContext)
	}
	if d.BoundedBudget != nil {
		result = append(result, d.BoundedBudget)
	}
	if d.Inputs != nil {
		result = append(result, inputReadStage)
	}
	if d.ModelAdmissionBarrier != nil {
		result = append(result, d.ModelAdmissionBarrier)
	}
	if d.ToolTimeline != nil {
		result = append(result, d.ToolTimeline)
	}
	return result
}

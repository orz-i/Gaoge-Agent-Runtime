package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type topLevelStartEnvelope struct {
	request StartRequest
	turn    Turn
	config  ConfigSnapshot
	created bool
	now     time.Time
}

func (runner *Runner) prepareTopLevelStartEnvelope(
	ctx context.Context,
	request StartRequest,
	root rootAdmission,
) (topLevelStartEnvelope, error) {
	// This is derived execution input, not a Host-selected configuration knob.
	request.Config.ContextBase = nil
	request, sessionID, turnID, err := normalizeStartRequest(request)
	if err != nil {
		return topLevelStartEnvelope{}, err
	}
	now := runner.clock.Now().UTC()
	resolvedConfig, err := runner.resolveConfigExecutionPolicy(request.Config)
	if err != nil {
		return topLevelStartEnvelope{}, err
	}
	request, err = sealStartAdmission(request, &resolvedConfig, root)
	if err != nil {
		return topLevelStartEnvelope{}, err
	}
	config, err := runner.sealContextAdmission(ctx, sessionID, turnID, resolvedConfig, request.Context, now)
	if err != nil {
		return topLevelStartEnvelope{}, err
	}
	turn, created, err := runner.persistStartEnvelope(ctx, request, sessionID, turnID, config, now)
	if err != nil {
		return topLevelStartEnvelope{}, err
	}
	return topLevelStartEnvelope{request: request, turn: turn, config: config, created: created, now: now}, nil
}

func (runner *Runner) finishTopLevelStart(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	runtimeSnapshot kernel.Snapshot,
	startErr error,
) (Snapshot, error) {
	if runtimeSnapshot.Run.ID == "" {
		// An empty-snapshot conflict from a composite Feature or a retry is
		// not proof that its remote work stopped. A same-ID cancelled
		// Kernel Run cannot settle another unresolved physical dispatch.
		// Only the original first-attempt Agent can reconcile a cancelled
		// Run after a pre-dispatch conflict, with no unknown-effect receipt
		// and no outstanding dispatch grant.
		if invocation.Attempt == 1 &&
			errors.Is(startErr, kernel.ErrConflict) && !errors.Is(startErr, ErrExecutionOutcomeUnknown) {
			pending, checkErr := runner.pendingExecutionDispatch(ctx, turn.ID, nil, nil)
			if checkErr != nil {
				return Snapshot{}, errors.Join(startErr, checkErr)
			}
			if !pending {
				recovered, recoverErr := runner.Refresh(ctx, turn.ID)
				if recoverErr == nil && initialAgentCancelledStartConflict(invocation, recovered.Turn.Status, startErr) {
					return recovered, nil
				}
			}
		}
		if startErr == nil {
			// Neither a physical Run nor an error is a completed effect.
			return Snapshot{}, ErrExecutionOutcomeUnknown
		}
		if !isDispatchProvenNoEffect(startErr) {
			return Snapshot{}, startErr
		}
		failed, failErr := runner.failTopLevelInvocationAndTurn(ctx, turn, invocation, startErr)
		return failed, errors.Join(startErr, failErr)
	}
	snapshot, syncErr := runner.syncRuntimeSnapshotWithRetry(ctx, turn, invocation, runtimeSnapshot)
	if initialAgentCancelledStartConflict(invocation, snapshot.Turn.Status, startErr) {
		startErr = nil
	}
	return snapshot, errors.Join(startErr, syncErr)
}

// A first-attempt Agent may observe a committed cancellation instead of a
// competing Kernel CAS error. Retried Attempts do not inherit this exception.
func initialAgentCancelledStartConflict(invocation Invocation, status TurnStatus, startErr error) bool {
	return invocation.Attempt == 1 &&
		status == TurnCancelled && errors.Is(startErr, kernel.ErrConflict)
}

// RefreshInvocation observes an original fixed-local Handoff child.
// Root Agents use Refresh; remote/dynamic Handoff observations stay with their ChildRunner.
func (runner *Runner) RefreshInvocation(ctx context.Context, invocationID string) (Snapshot, error) {
	invocation, err := runner.store.GetInvocation(ctx, strings.TrimSpace(invocationID))
	if err != nil {
		return Snapshot{}, err
	}
	if !isHandoffDelegationInvocation(invocation) {
		return Snapshot{}, ErrConflict
	}
	// A routed Handoff belongs to its original ChildRunner, not an arbitrary
	// Run with the same ID in the Harness Kernel. Only an explicitly fixed
	// same-Kernel route may be observed through the local Invocation reader.
	// A remote/dynamic route needs its own ChildRunner-owned observation.
	coordinator, ok := runner.handoffs.(*handoff.Coordinator)
	if !ok || !coordinator.SupportsLocalDelegationObservation(runner.runtime) ||
		(coordinator.ObservesLocalKernelRunner(runner.runtime) && !coordinator.OwnsLocalKernelRunner(runner.runtime) &&
			!coordinator.SupportsDeferredAgent(runner.runtime)) {
		return Snapshot{}, ErrHandoffCancellationUnverified
	}
	invocation, turn, runtimeSnapshot, err := runner.loadInvocationRuntime(ctx, invocationID)
	if err != nil {
		return Snapshot{}, err
	}
	frozen, _, proofErr := runner.localHandoffCancellationAuthority(ctx, invocation, runtimeSnapshot)
	if proofErr != nil {
		return Snapshot{}, proofErr
	}
	if !runner.observesLocalChild(ctx, coordinator, frozen, runtimeSnapshot) {
		return Snapshot{}, ErrHandoffCancellationUnverified
	}
	pending, err := runner.resolvedInteractionClaimPending(ctx, turn.ID, "")
	if err != nil {
		return Snapshot{}, err
	}
	if pending {
		return runner.loadSnapshot(ctx, turn, &runtimeSnapshot)
	}
	return runner.syncChildInvocationSnapshot(ctx, turn, invocation, runtimeSnapshot)
}

// RetryInvocation starts a new attempt of the original root Agent only.
// Retired Native Child Capability and routed Handoff children cannot rotate
// their original physical execution identity.
func (runner *Runner) RetryInvocation(ctx context.Context, invocationID string) (Snapshot, error) {
	invocation, err := runner.store.GetInvocation(ctx, strings.TrimSpace(invocationID))
	if err != nil {
		return Snapshot{}, err
	}
	if invocation.ParentItemID != "" || len(invocation.Input) == 0 {
		return Snapshot{}, ErrConflict
	}
	turn, err := loadVerifiedTurn(ctx, runner.store, invocation.TurnID)
	if err != nil {
		return Snapshot{}, errors.Join(ErrConflict, err)
	}
	if err = runner.requireFrozenAdmission(ctx, turn); err != nil {
		return Snapshot{}, err
	}
	if invocation.Attempt > 1 && !retryableInvocationStatus(invocation.Status) {
		return runner.replayRetriedInvocation(ctx, turn, invocation)
	}
	if !retryableInvocationStatus(invocation.Status) {
		return Snapshot{}, ErrConflict
	}
	// Retry rotates a physical root Run identity only from its originally
	// frozen Actor and Kernel Thread, never mutable Session values.
	if _, _, err := frozenTopLevelInvocationIdentity(invocation); err != nil {
		return Snapshot{}, err
	}
	retryCtx, authorityErr := runner.withOriginalRetryAuthority(ctx, invocation)
	if authorityErr != nil {
		return Snapshot{}, authorityErr
	}
	return runner.startNextInvocationAttempt(retryCtx, turn, invocation)
}

func (runner *Runner) startNextInvocationAttempt(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
) (Snapshot, error) {
	nextAttempt := invocation.Attempt + 1
	nextRef := invocationExecutionRefID(invocation.ID, nextAttempt)
	// The original Store CAS MUST atomically admit the replacement shared
	// budget generation. A separate Prepare/Commit saga could survive a crash
	// as an Accepted Invocation without any cancellable Kernel Run.
	ctx, err := runner.withAtomicRetryBudget(ctx, turn, invocation, nextRef)
	if err != nil {
		return Snapshot{}, err
	}
	retried, err := runner.store.RetryInvocation(ctx, invocation.ID, invocation.Revision, nextRef, runner.clock.Now().UTC())
	if err != nil {
		return Snapshot{}, err
	}
	if err = runner.recordInvocationItem(ctx, retried); err != nil {
		return Snapshot{}, err
	}
	turn, err = loadVerifiedTurn(ctx, runner.store, turn.ID)
	if err != nil {
		return Snapshot{}, err
	}
	return runner.startRetriedTopLevelInvocation(ctx, turn, retried)
}

func (runner *Runner) replayRetriedInvocation(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
) (Snapshot, error) {
	// The Store's original Retry CAS has already atomically committed the
	// replacement budget generation. Replay only the same frozen Invocation
	// and exact physical dispatch identity; never mint another budget grant.
	loaded, err := runner.runtime.Load(ctx, invocation.ExecutionRefID)
	if err == nil {
		return runner.syncRuntimeSnapshotWithRetry(ctx, turn, invocation, loaded)
	}
	if !errors.Is(err, kernel.ErrNotFound) || invocation.Status != InvocationAccepted {
		return Snapshot{}, err
	}
	if err = runner.recordInvocationItem(ctx, invocation); err != nil {
		return Snapshot{}, err
	}
	return runner.startRetriedTopLevelInvocation(ctx, turn, invocation)
}

func (runner *Runner) startRetriedTopLevelInvocation(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
) (Snapshot, error) {
	if invocation.ParentItemID != "" {
		return Snapshot{}, ErrConflict
	}
	actor, thread, err := frozenTopLevelInvocationIdentity(invocation)
	if err != nil {
		return Snapshot{}, err
	}
	runCtx, err := runner.topLevelRetryContext(ctx, turn)
	if err != nil {
		return Snapshot{}, err
	}
	runner.publishTurnStatus(ctx, turn, EventTurnStarted, false)
	runtimeSnapshot, startErr := runner.startInvocationAttempt(runCtx, invocation, turn, actor, thread)
	return runner.finishTopLevelStart(ctx, turn, invocation, runtimeSnapshot, startErr)
}

// frozenTopLevelInvocationIdentity reads the exact Actor/Thread committed in
// the Invocation. The Session is NOT a fallback execution authority: its
// HostThread can differ from the Kernel Thread, and reconstructing missing
// fields during retry silently changes a previously frozen physical Run.
func frozenTopLevelInvocationIdentity(invocation Invocation) (kernel.ActorRef, kernel.ThreadRef, error) {
	if len(invocation.Input) == 0 || !json.Valid(invocation.Input) ||
		hashInvocationBytes(invocation.Input) != invocation.InputHash {
		return kernel.ActorRef{}, kernel.ThreadRef{}, ErrConflict
	}
	var input struct {
		Actor  kernel.ActorRef  `json:"actor"`
		Thread kernel.ThreadRef `json:"thread"`
	}
	if err := json.Unmarshal(invocation.Input, &input); err != nil ||
		!validActor(input.Actor) || strings.TrimSpace(input.Thread.Kind) == "" ||
		strings.TrimSpace(input.Thread.ID) == "" {
		return kernel.ActorRef{}, kernel.ThreadRef{}, ErrConflict
	}
	return input.Actor, input.Thread, nil
}

func (runner *Runner) topLevelRetryContext(ctx context.Context, turn Turn) (context.Context, error) {
	checkpointID := strings.TrimSpace(turn.ContextCheckpointID)
	if checkpointID == "" {
		return ctx, nil
	}
	checkpoint, err := runner.store.GetContextCheckpoint(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	if checkpoint.ScopeID != turn.SessionID || !sameContextCheckpointRef(checkpoint, turn.ContextRef) || checkpoint.ID != checkpointID {
		return nil, ErrConflict
	}
	return withContextCheckpoint(ctx, checkpoint), nil
}

func (runner *Runner) startInvocationAttempt(
	ctx context.Context,
	invocation Invocation,
	turn Turn,
	actor kernel.ActorRef,
	thread kernel.ThreadRef,
) (kernel.Snapshot, error) {
	if invocation.ParentItemID != "" {
		return kernel.Snapshot{}, ErrConflict
	}
	requestID := stableID("hretry", invocation.ID, attemptString(invocation.Attempt))
	return runner.withPhysicalInvocationDispatch(ctx, invocation, "start-retry", func(physicalCtx context.Context) (kernel.Snapshot, error) {
		var input agentInvocationInput
		if err := json.Unmarshal(invocation.Input, &input); err != nil {
			return kernel.Snapshot{}, ErrConflict
		}
		config, err := loadSealedConfig(physicalCtx, runner.store, turn.ConfigSnapshotID)
		if err != nil {
			return kernel.Snapshot{}, err
		}
		start := rootAgentStartRequest(config, invocation.ExecutionRefID, actor, thread,
			requestID, input.Goal, input.InitialToolKeys, input.RequiredToolKeys)
		// The original invocation's admission hash already bound these
		// per-run spending grants; without freezing them in Invocation Input,
		// retry silently changes bounded execution into unbounded execution.
		start.CallAllowance, start.AutoAllowance = input.CallAllowance, input.AutoAllowance
		return runner.agent.StartRun(physicalCtx, start)
	})
}

// CancelInvocation cancels one original Handoff child with its frozen route
// and durable Invocation identity; ordinary Native Child is retired.
func (runner *Runner) CancelInvocation(ctx context.Context, invocationID, reason string) (snapshot Snapshot, err error) {
	invocationID = strings.TrimSpace(invocationID)
	invocation, err := runner.store.GetInvocation(ctx, invocationID)
	if err != nil {
		return Snapshot{}, err
	}
	if !validInvocationExecutionAuthority(invocation) || invocation.ID != invocationID ||
		!isHandoffDelegationInvocation(invocation) || strings.TrimSpace(invocation.ExecutionRefID) == "" {
		return Snapshot{}, ErrConflict
	}
	if err = verifyProtectiveInvocation(ctx, runner.store, invocation); err != nil {
		return Snapshot{}, err
	}
	// A protective child stop uses only the original Turn identity here.
	// The existing related-Run and ancestry fences must still prove ownership
	// before any physical cancellation; ancillary Config reads can fail.
	turn, err := loadVerifiedTurnIdentity(ctx, runner.store, invocation.TurnID)
	if err != nil {
		return Snapshot{}, err
	}
	intent, err := runner.cancelPreflightWithIntent(ctx, turn, invocation.ExecutionRefID,
		func() error {
			_, checkErr := runner.inspectRelatedCancellation(ctx, turn, invocation.ExecutionRefID)
			return checkErr
		},
		func(writeAttempted *bool) error {
			return runner.fenceRelatedCancellation(ctx, turn, invocation.ExecutionRefID, writeAttempted)
		},
		invocation,
	)
	if err != nil {
		return Snapshot{}, err
	}
	settled := false
	defer func() {
		if settled && err == nil {
			err = runner.finishCancellationFenceAfterRequest(ctx, intent)
		}
	}()
	ctx = withCancellationElection(ctx, intent)
	runtimeSnapshot, found, err := runner.cancelRuntimeRun(ctx, invocation.ExecutionRefID, reason, nil)
	if err != nil {
		return Snapshot{}, runner.releaseNoEffectCancellation(ctx, turn, intent, invocation.ExecutionRefID, err)
	}
	if !found {
		runtimeSnapshot = cancelledRuntimeSnapshot(invocation.ExecutionRefID, reason)
	}
	runs := map[string]kernel.Snapshot{invocation.ExecutionRefID: runtimeSnapshot}
	// Failed/Completed parent Agents may still own active physical
	// Handoff descendants in RunRelation. Only a
	// proven terminal subtree may settle this child's cancellation intent.
	if terminalRuntimeStatus(runtimeSnapshot.Run.Status) {
		descendants, cancelErr := runner.cancelOwnedDescendants(ctx, turn, invocation.ExecutionRefID, reason)
		if cancelErr != nil {
			return Snapshot{}, cancelErr
		}
		for runID, snapshot := range descendants {
			runs[runID] = snapshot
		}
	}
	if err = runner.syncInvocationRuns(ctx, turn, runs); err != nil {
		return Snapshot{}, err
	}
	if err = runner.syncTerminalDelegations(ctx, turn, runs); err != nil {
		return Snapshot{}, err
	}
	snapshot, err = runner.loadSnapshot(ctx, turn, nil)
	settled = err == nil
	return snapshot, err
}

func (runner *Runner) validateParentItem(ctx context.Context, turnID, parentItemID string) error {
	parentItemID = strings.TrimSpace(parentItemID)
	if parentItemID == "" {
		return nil // An Interaction can target its Invocation directly.
	}
	if runner == nil || runner.store == nil || strings.TrimSpace(turnID) == "" {
		return ErrInvalidRequest
	}
	// Admission and dispatch must use the same globally indexed Item
	// identity, not independently interpret a full Turn Timeline.
	item, err := runner.store.GetItem(ctx, parentItemID)
	if errors.Is(err, ErrNotFound) {
		return ErrInvalidRequest
	}
	if err != nil {
		return err
	}
	if item.ID != parentItemID || item.Seq == 0 {
		return ErrConflict
	}
	if item.TurnID != strings.TrimSpace(turnID) {
		return ErrInvalidRequest
	}
	return nil
}

func (runner *Runner) loadInvocationRuntime(
	ctx context.Context,
	invocationID string,
) (Invocation, Turn, kernel.Snapshot, error) {
	invocation, err := runner.store.GetInvocation(ctx, strings.TrimSpace(invocationID))
	if err != nil {
		return Invocation{}, Turn{}, kernel.Snapshot{}, err
	}
	turn, err := loadVerifiedTurn(ctx, runner.store, invocation.TurnID)
	if err != nil {
		return Invocation{}, Turn{}, kernel.Snapshot{}, err
	}
	runtimeSnapshot, err := runner.runtime.Load(ctx, invocation.ExecutionRefID)
	return invocation, turn, runtimeSnapshot, err
}

func (runner *Runner) resumeInvocationExecution(
	ctx context.Context,
	invocation Invocation,
	expectedRevision uint64,
) (kernel.Snapshot, error) {
	if invocation.ParentItemID != "" {
		// Only Root Agent continuation is local. Handoff children retain
		// the original ChildRunner-owned physical continuation.
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	turn, err := loadVerifiedTurn(ctx, runner.store, invocation.TurnID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if err = runner.requireFrozenAdmission(ctx, turn); err != nil {
		return kernel.Snapshot{}, err
	}
	if err = runner.requireInvocationRootModelOutputCeiling(ctx, invocation); err != nil {
		return kernel.Snapshot{}, err
	}
	// Root continuation also respects pending child continuations.
	if err = runner.requireUnclaimedInteractionContinuation(ctx, turn.ID, ""); err != nil {
		return kernel.Snapshot{}, err
	}
	resumer, ok := runner.agent.(agentResumer)
	if !ok {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	if snapshot, managed, managedErr := runner.recoverManagedInvocation(ctx, invocation); managed {
		return snapshot, managedErr
	}
	resumed, resumeErr := runner.withPhysicalInvocationDispatch(ctx, invocation, "resume", func(physicalCtx context.Context) (kernel.Snapshot, error) {
		return resumer.Resume(physicalCtx, invocation.ExecutionRefID, expectedRevision)
	})
	if errors.Is(resumeErr, ErrConflict) && resumed.Run.ID == "" {
		// A crashed native Agent Start may retain a Harness grant even
		// after its Kernel Run acquired a sticky execution Epoch. Let
		// only the first-party Kernel lease arbitrate that recovery.
		return runner.resumeNativeFencedAgentAfterInterruptedStart(
			ctx, invocation, expectedRevision, resumer, resumeErr)
	}
	return resumed, resumeErr
}

func (runner *Runner) syncChildInvocationSnapshot(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	runtimeSnapshot kernel.Snapshot,
	verifiedNoRunCancel ...bool,
) (Snapshot, error) {
	// Handoff observations can also synchronize an independently advancing
	// Kernel Run. Do not overwrite a claimed, unverified business response
	// solely because its parent Turn was already projected as running.
	pending, err := runner.resolvedInteractionClaimPending(ctx, turn.ID, invocation.ID)
	if err != nil {
		return Snapshot{}, err
	}
	if pending {
		return runner.loadSnapshot(ctx, turn, &runtimeSnapshot)
	}
	// Handoff Refresh must not commit a terminal child
	// projection while its original Host callback continuation is unknown.
	// A Cancel path has its own verified no-Run cancellation proof.
	verifiedCancel := len(verifiedNoRunCancel) == 1 && verifiedNoRunCancel[0]
	if !verifiedCancel {
		continuationPending, continuationErr := runner.pendingInteractionContinuation(ctx, turn.ID, invocation.ID)
		if continuationErr != nil {
			return Snapshot{}, continuationErr
		}
		if continuationPending {
			return runner.loadSnapshot(ctx, turn, &runtimeSnapshot)
		}
	}
	if runtimeSnapshotConflicts(invocation, runtimeSnapshot) {
		return Snapshot{}, ErrConflict
	}
	// Reject a foreign child observation before status CAS and Artifact or
	// diagnostic output. Delegated Handoff children retain their separate
	// explicit physical-owner and receipt validation.
	// Only a verified Cancel/CancelInvocation descendant cleanup may
	// synthesize a cancelled child whose Kernel Run never existed. Public
	// Refresh and all real physical snapshots must prove the original
	// frozen owner even when the Run has no Result.
	noRunProven := len(verifiedNoRunCancel) == 1 && verifiedNoRunCancel[0] &&
		runtimeSnapshot.Run.ID == invocation.ExecutionRefID &&
		runtimeSnapshot.Run.Kind == "" && runtimeSnapshot.Result == nil &&
		runtimeSnapshot.Run.Status == kernel.RunStatusCancelled &&
		runtimeSnapshot.Run.ErrorCode == "run.cancelled"
	if !isHandoffDelegationInvocation(invocation) {
		return Snapshot{}, ErrConflict
	}
	status, err := runner.invocationRuntimeStatus(ctx, invocation, runtimeSnapshot)
	if err != nil {
		return Snapshot{}, err
	}
	if terminalTurnStatus(status) {
		if noRunProven {
			ctx = withOriginalWaitingCancellationWitness(ctx, invocation)
		} else {
			ctx, err = withOriginalKernelTerminalWitness(ctx, invocation, runtimeSnapshot)
			if err != nil {
				return Snapshot{}, err
			}
		}
	}
	invocation, err = runner.syncInvocationProjection(ctx, invocation, runtimeSnapshot, status)
	if err != nil {
		return Snapshot{}, err
	}
	if err = runner.projectChildInvocationOutcome(ctx, invocation, runtimeSnapshot); err != nil {
		return Snapshot{}, err
	}
	if err = runner.recordApprovalRequestItem(ctx, turn, invocation, runtimeSnapshot); err != nil {
		return Snapshot{}, err
	}
	if err = runner.syncSubtaskItems(ctx, turn); err != nil {
		return Snapshot{}, err
	}
	turn, err = loadVerifiedTurn(ctx, runner.store, turn.ID)
	if err != nil {
		return Snapshot{}, err
	}
	return runner.loadSnapshot(ctx, turn, nil)
}

func (runner *Runner) projectChildInvocationOutcome(
	ctx context.Context,
	invocation Invocation,
	runtimeSnapshot kernel.Snapshot,
) error {
	if invocation.Status == InvocationCompleted && runtimeSnapshot.Result != nil {
		payload, err := json.Marshal(struct {
			ContentType string          `json:"contentType"`
			Content     json.RawMessage `json:"content"`
		}{runtimeSnapshot.Result.ContentType, runtimeSnapshot.Result.Content})
		if err != nil {
			return err
		}
		now := runner.clock.Now().UTC()
		_, err = appendItemFact(ctx, runner.store, runner.turnFeed, Item{
			ID: stableID("hico", invocation.ID, "result", attemptString(invocation.Attempt)), TurnID: invocation.TurnID,
			Kind: ItemArtifact, Status: ItemCompleted, RunID: invocation.ExecutionRefID,
			InvocationID: invocation.ID, ParentItemID: invocationLifecycleItemID(invocation, InvocationAccepted, 1),
			Payload: payload, CreatedAt: now, UpdatedAt: now,
		})
		return err
	}
	if invocation.Status != InvocationFailed && invocation.Status != InvocationCancelled {
		return nil
	}
	payload, err := json.Marshal(struct {
		ErrorCode   string `json:"errorCode,omitempty"`
		ErrorDetail string `json:"errorDetail,omitempty"`
	}{invocation.ErrorCode, invocation.ErrorDetail})
	if err != nil {
		return err
	}
	now := runner.clock.Now().UTC()
	_, err = appendItemFact(ctx, runner.store, runner.turnFeed, Item{
		ID: stableID("hicd", invocation.ID, string(invocation.Status), attemptString(invocation.Attempt)), TurnID: invocation.TurnID,
		Kind: ItemDiagnostic, Status: invocationItemStatus(invocation.Status), RunID: invocation.ExecutionRefID,
		InvocationID: invocation.ID, ParentItemID: invocationLifecycleItemID(invocation, InvocationAccepted, 1),
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return err
}

// invocationRuntimeStatus reflects persisted generic Host interactions while
// keeping native Agent and delegated child execution ownership unchanged.
func (runner *Runner) invocationRuntimeStatus(ctx context.Context, invocation Invocation, snapshot kernel.Snapshot) (TurnStatus, error) {
	if snapshot.Run.Status == kernel.RunStatusRunning && invocation.ParentItemID == "" {
		waiting, err := runner.hasOtherWaitingInteraction(ctx, Interaction{TurnID: invocation.TurnID})
		if err != nil {
			return "", err
		}
		if waiting {
			return TurnWaitingInput, nil
		}
	}
	return turnStatusFromRuntime(snapshot.Run.Status)
}

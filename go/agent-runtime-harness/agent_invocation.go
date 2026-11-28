package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
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
		if native, e := runner.nativeRootCommands(ctx, turn.ID, true); e != nil {
			return Snapshot{}, errors.Join(startErr, e)
		} else if native != nil {
			return runner.finishNativeRootStart(ctx, turn, invocation, startErr)
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
	coordinator, ok := runner.handoffs.(*handoff.Coordinator)
	if !ok {
		return Snapshot{}, ErrHandoffCancellationUnverified
	}
	runtimeSnapshot, frozen, err := runner.localDelegationObservation(ctx, invocation.ExecutionRefID)
	if err != nil {
		return Snapshot{}, err
	}
	if original := coordinator.LocalAgent(runner.runtime); original != nil && !frozen.NativeLocal &&
		!coordinator.PreparesLocal() && !original.OwnsKernelRuntime(runner.runtime) {
		return Snapshot{}, ErrHandoffCancellationUnverified
	}
	turn, err := loadVerifiedTurn(ctx, runner.store, invocation.TurnID)
	if err != nil {
		return Snapshot{}, err
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
	retryCtx, authorityErr := runner.withOriginalRetryAuthority(ctx, turn, invocation)
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
	// The Host CAS chooses one immutable Attempt. Native execution admission
	// follows that choice; legacy external ledgers retain their original CAS.
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
	// Replay the immutable Host selection. For native accounting an accepted
	// Attempt may not yet have been admitted to the original ledger.
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
	var input agentInvocationInput
	if !decodeTypedAuthorityJSON(invocation.Input, &input) {
		return kernel.Snapshot{}, ErrConflict
	}
	config, err := loadSealedConfig(ctx, runner.store, turn.ConfigSnapshotID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	start := rootAgentStartRequest(config, invocation.ExecutionRefID, actor, thread,
		requestID, input.Goal, input.InitialToolKeys, input.RequiredToolKeys)
	start.CallAllowance = input.CallAllowance
	return runner.startOriginalRoot(ctx, invocation, "start-retry", start)
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
	if _, err = verifyProtectiveInvocation(ctx, runner.store, invocation); err != nil {
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
	if found && !terminalRuntimeStatus(runtimeSnapshot.Run.Status) {
		return Snapshot{}, ErrRuntimeCancellationUnverified
	}
	runs, err := runner.cancelOwnedDescendants(ctx, turn, invocation.ExecutionRefID, reason)
	if err != nil {
		return Snapshot{}, err
	}
	if found {
		runs[invocation.ExecutionRefID] = runtimeSnapshot
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

// Native eligibility is operation-specific. An opaque callback or actual
// independent accounting/cancellation domain needs Host arbitration; invalid
// original evidence is a rejection, never permission to fall back.
func (runner *Runner) nativeRootCommands(ctx context.Context, turnID string, scoped bool) (*agent.Runner, error) {
	native, ok := runner.agent.(*agent.Runner)
	if !ok || !native.ObservesKernelRuntime(runner.runtime) {
		return nil, nil
	}
	turn, err := loadVerifiedTurnIdentity(ctx, runner.store, turnID)
	if err != nil {
		return nil, err
	}
	ledger, err := runner.frozenTurnBudget(ctx, turn)
	cancel, local := runner.cancellation.(*kernel.Runtime)
	_, bound := runner.runtime.BoundExecutionLease()
	if err != nil || ledger != nil && !runner.nativeBudget() ||
		!local || cancel != runner.runtime || bound || scoped && !native.NativeRootCallbacksScoped() {
		return nil, err
	}
	return native, nil
}

// Host authorization is read-only for an existing native Run. Do not bypass
// independent outstanding Host operations from older or mixed compositions.
// The native Run revision/Grant is the only new execution election.
func (runner *Runner) originalNativeCommand(ctx context.Context, inv Invocation) error {
	if terminalInvocationStatus(inv.Status) {
		return ErrConflict
	}
	turn, e := loadVerifiedTurn(ctx, runner.store, inv.TurnID)
	if e != nil {
		return e
	}
	if terminalTurnStatus(turn.Status) {
		return ErrConflict
	}
	if e = runner.requireUnclaimedInteractionContinuation(ctx, inv.TurnID, ""); e != nil {
		return e
	}
	current, e := verifyProtectiveInvocation(ctx, runner.store, inv)
	if e != nil || terminalInvocationStatus(current.Status) {
		return errors.Join(ErrConflict, e)
	}
	if inv.ParentItemID == "" {
		root, e := loadTopLevelInvocation(ctx, runner.store, inv.TurnID)
		if e != nil || !sameFrozenInvocationAuthority(inv, root) {
			return errors.Join(ErrConflict, e)
		}
	} else if e = runner.requireLiveInvocationParents(ctx, inv); e != nil {
		return e
	}
	// Re-read after at most one exact original receipt repair, including
	// the cancellation fence; repair itself is not new admission.
	for attempt := range 2 {
		items, e := runner.loadGlobalPhysicalEvidence(ctx, inv.TurnID)
		if e != nil {
			return e
		}
		if ActiveCancellationFence(items) {
			return ErrConflict
		}
		if !PendingExecutionDispatch(items, inv.ID) {
			return nil
		}
		if inv.ParentItemID != "" {
			return ErrConflict
		}
		if attempt == 0 {
			if repaired, e := runner.recoverOriginalPhysicalDispatch(ctx, inv); e != nil || !repaired {
				return errors.Join(ErrExecutionOutcomeUnknown, e)
			}
		}
	}
	return ErrExecutionOutcomeUnknown
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
	if native, e := runner.nativeRootCommands(ctx, turn.ID, false); e != nil {
		return kernel.Snapshot{}, e
	} else if native != nil {
		if e := runner.originalNativeCommand(ctx, invocation); e != nil {
			return kernel.Snapshot{}, e
		}
		if !native.SupportsManagedRecovery() {
			return native.Resume(ctx, invocation.ExecutionRefID, expectedRevision)
		}
		snap, _, e := runner.observeNativeCommand(ctx, native, invocation, "resume", nil, func(c context.Context) (kernel.Snapshot, error) {
			return native.Resume(c, invocation.ExecutionRefID, expectedRevision)
		})
		return snap, e
	}
	return runner.withPhysicalInvocationDispatch(ctx, invocation, "resume", func(physicalCtx context.Context) (kernel.Snapshot, error) {
		return resumer.Resume(physicalCtx, invocation.ExecutionRefID, expectedRevision)
	})
}

func (runner *Runner) syncChildInvocationSnapshot(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	runtimeSnapshot kernel.Snapshot,
	verifiedCancellation ...bool,
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
	// A Cancel path has its original elected cancellation proof.
	verifiedCancel := len(verifiedCancellation) == 1 && verifiedCancellation[0]
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
	// Only actual Run snapshots reach this path. Never-dispatched children
	// use their original native creation decision or opaque no-effect proof.
	if runtimeSnapshot.Run.Kind == "" {
		return Snapshot{}, ErrConflict
	}
	if !isHandoffDelegationInvocation(invocation) {
		return Snapshot{}, ErrConflict
	}
	status, err := runner.invocationRuntimeStatus(ctx, invocation, runtimeSnapshot)
	if err != nil {
		return Snapshot{}, err
	}
	if terminalTurnStatus(status) {
		ctx, err = withOriginalKernelTerminalWitness(ctx, invocation, runtimeSnapshot)
		if err != nil {
			return Snapshot{}, err
		}
	}
	if err = runner.recordNativeApprovalDecisions(ctx, turn, invocation, runtimeSnapshot); err != nil {
		return Snapshot{}, err
	}
	invocation, err = runner.syncInvocationProjection(ctx, invocation, runtimeSnapshot, status)
	if err != nil {
		return Snapshot{}, err
	}
	if err = runner.projectChildInvocationOutcome(ctx, invocation, runtimeSnapshot.Result); err != nil {
		return Snapshot{}, err
	}
	if err = runner.recordApprovalRequestItem(ctx, turn, invocation, runtimeSnapshot); err != nil {
		return Snapshot{}, err
	}
	if terminalTurnStatus(status) {
		frozen, e := decodeFrozenHandoffDelegation(invocation.Input)
		if e != nil {
			return Snapshot{}, e
		}
		if frozen.NativeLocal {
			if err = runner.syncTerminalDelegations(ctx, turn, map[string]kernel.Snapshot{runtimeSnapshot.Run.ID: runtimeSnapshot}); err != nil {
				return Snapshot{}, err
			}
		}
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
	result *kernel.Result,
) error {
	now := runner.clock.Now().UTC()
	item := Item{TurnID: invocation.TurnID, RunID: invocation.ExecutionRefID, InvocationID: invocation.ID, ParentItemID: invocationLifecycleItemID(invocation, InvocationAccepted, 1), CreatedAt: now, UpdatedAt: now}
	var err error
	switch {
	case invocation.Status == InvocationCompleted && result != nil:
		item.ID = stableID("hico", invocation.ID, "result", attemptString(invocation.Attempt))
		item.Kind, item.Status = ItemArtifact, ItemCompleted
		item.Payload, err = json.Marshal(struct {
			ContentType string          `json:"contentType"`
			Content     json.RawMessage `json:"content"`
		}{result.ContentType, result.Content})
	case invocation.Status == InvocationFailed || invocation.Status == InvocationCancelled:
		item.ID = stableID("hicd", invocation.ID, string(invocation.Status), attemptString(invocation.Attempt))
		item.Kind, item.Status = ItemDiagnostic, invocationItemStatus(invocation.Status)
		item.Payload, err = json.Marshal(struct {
			ErrorCode   string `json:"errorCode,omitempty"`
			ErrorDetail string `json:"errorDetail,omitempty"`
		}{invocation.ErrorCode, invocation.ErrorDetail})
	default:
		return nil
	}
	if err != nil {
		return err
	}
	_, err = appendItemFact(ctx, runner.store, runner.turnFeed, item)
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

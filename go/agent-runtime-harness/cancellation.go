package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// RuntimeCanceller lets the host perform feature-owned cleanup, including
// cancelling remote tasks, before projecting a Runtime Run as terminal.
// IMPORTANT: kernel.ErrConflict from Cancel certifies that the rejected
// attempt performed NO external cleanup. A canceller which might already
// have changed remote state must return an outcome-unknown error instead.
type RuntimeCanceller interface {
	Cancel(context.Context, string, uint64, string) (kernel.Snapshot, error)
}

// LocalKernelCanceller is the Host's explicit claim that its custom
// Canceller acts on this exact Kernel. Handoff never infers that authority
// from identical Run IDs or matching public task fields. An arbitrary
// RuntimeCanceller without this proof cannot cancel a delegated local Run.
type LocalKernelCanceller interface {
	RuntimeCanceller
	OwnsKernelRuntime(*kernel.Runtime) bool
}

// LocalKernelRunKindCanceller is a narrower alternative for mixed local
// Kernel and remote cancellers. It proves that Cancel for exactly this RunKind
// delegates to the same Kernel, without asserting that the entire canceller
// owns remote A2A/Handoff effects. Harness still verifies the original
// Feature/Agent route and the committed cancellation result independently.
// Forwarding a DIRECT native child-busy no-effect result also certifies that
// this callback performed no Host cleanup. If Host effects could have begun,
// return an outcome-unknown error instead, never pass through that proof.
type LocalKernelRunKindCanceller interface {
	RuntimeCanceller
	OwnsKernelRunKind(*kernel.Runtime, kernel.RunKind) bool
}

// RemoteDelegationCanceller is an explicit Host-owned A2A cancellation proof.
// The original frozen target and persisted remote shadow must agree; generic
// Agent Kernel control can never grant authority over remote effects.
type RemoteDelegationCanceller interface {
	RuntimeCanceller
	OwnsRemoteDelegation(context.Context, *kernel.Runtime, handoff.Delegation, kernel.Snapshot) bool
}

// ErrHandoffCancellationUnverified means a delegated ChildRunner owns a Run
// that the Harness-local Kernel cannot observe or cancel. No terminal status
// may be projected without a verified, route-owned cancellation outcome.
var ErrHandoffCancellationUnverified = errors.New("harness handoff child cancellation is unverified")

// ErrRuntimeCancellationUnverified means a Host RuntimeCanceller returned a
// result that cannot be reconciled with the owning local Kernel Run. The
// callback may already have performed effects: retain the cancellation
// intent and never interpret this error as a definitive no-effect conflict.
var ErrRuntimeCancellationUnverified = errors.New("harness runtime cancellation outcome is unverified")

func (runner *Runner) cancelRuntimeRun(
	ctx context.Context,
	runID string,
	reason string,
	root *Invocation,
) (kernel.Snapshot, bool, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return kernel.Snapshot{}, false, ErrInvalidRequest
	}
	// A native Kernel control can distinguish a read-only/pre-Store rejection
	// from a write-capable Store outcome. Custom cancellers retain their
	// explicit contract that kernel.ErrConflict certifies zero external
	// cleanup; never infer that guarantee from a native Store conflict.
	_, nativeKernel := runner.cancellation.(*kernel.Runtime)
	var delegatedChild *Invocation
	if runner.store != nil {
		owner, ownerErr := runner.store.GetInvocationByExecutionRefID(ctx, runID)
		if ownerErr != nil && !errors.Is(ownerErr, ErrNotFound) {
			return kernel.Snapshot{}, false, ownerErr
		}
		if ownerErr == nil && isHandoffDelegationInvocation(owner) {
			delegatedChild = &owner
		}
	}
	nativeSelected := false
	if delegatedChild != nil {
		intent, bound := ctx.Value(cancellationElectionKey{}).(Item)
		if !bound || intent.TurnID != delegatedChild.TurnID || !ValidCancellationFenceIntentAdmission(intent) || intent.Seq == 0 {
			return kernel.Snapshot{}, false, ErrHandoffCancellationUnverified
		}
		d, e := decodeFrozenHandoffDelegation(delegatedChild.Input)
		nativeSelected = e == nil && d.NativeLocal
	}
	if delegatedChild != nil && !nativeSelected {
		// Run IDs, Actor/Thread and even full frozen Goal/RequestID prove
		// task identity but not which Kernel/ChildRunner owns its effect.
		// A remote Run may have an exact metadata twin in this Kernel.
		// Reuse the existing immutable grant ledger first: an undisbursed
		// Handoff is safe to mark cancelled without touching any local Run.
		possible, proofErr := runner.handoffPhysicalEffectPossible(ctx, *delegatedChild)
		if proofErr != nil {
			return kernel.Snapshot{}, false, proofErr
		}
		if !possible {
			return kernel.Snapshot{}, false, runner.projectUndispatchedHandoffCancellation(ctx, *delegatedChild, reason)
		}
		// A delegated child must use a verified original, statically
		// composed local Agent or authenticated remote A2A Shadow route.
		// No generic dynamic ChildRunner can inherit physical control.
		if _, ok := runner.handoffs.(*handoff.Coordinator); !ok {
			return kernel.Snapshot{}, false, fmt.Errorf("unproven Handoff route %q: %w", runID, ErrHandoffCancellationUnverified)
		}
	}
	// For ordinary Root/Feature Runs as well, a native Canceller must be
	// exactly the Runtime whose Run was loaded. Keep Handoff's earlier
	// route-specific check and safe never-dispatched case authoritative.
	// A wrong native Runtime did no physical work; its rejection is a
	// Harness-minted pre-callback proof for cancellation-intent recovery.
	if native, ok := runner.cancellation.(*kernel.Runtime); ok && native != runner.runtime {
		if delegatedChild != nil {
			return kernel.Snapshot{}, false, fmt.Errorf("foreign Handoff Canceller: %w", ErrHandoffCancellationUnverified)
		}
		return kernel.Snapshot{}, false, cancellationReadNoEffect{cause: fmt.Errorf("native Canceller is not the Harness Kernel: %w", ErrConflict)}
	}
	var lastNativeNoEffect error
	for range maxRuntimeSyncRetryAttempts {
		current, err := runner.runtime.Load(ctx, runID)
		if errors.Is(err, kernel.ErrNotFound) {
			if nativeSelected {
				turn, e := loadVerifiedTurnIdentity(ctx, runner.store, delegatedChild.TurnID)
				if e != nil {
					return kernel.Snapshot{}, false, e
				}
				_, _, q, _, e := runner.nativeChildSelection(ctx, turn, *delegatedChild, kernel.RunStatusCancelled, reason)
				if e != nil {
					return kernel.Snapshot{}, false, e
				}
				d, e := runner.decideNativeCreation(ctx, q)
				if errors.Is(e, kernel.ErrAlreadyExists) {
					continue
				}
				if e != nil {
					return kernel.Snapshot{}, false, errors.Join(ErrExecutionOutcomeUnknown, e)
				}
				_, e = runner.projectUncreatedNativeChild(ctx, turn, *delegatedChild, d)
				return kernel.Snapshot{}, false, e
			}
			if delegatedChild != nil && !terminalInvocationStatus(delegatedChild.Status) {
				return kernel.Snapshot{}, false, ErrHandoffCancellationUnverified // original opaque dispatch remains unresolved
			}
			return kernel.Snapshot{}, false, runner.verifyMissingRelatedCancellation(ctx, runID, reason)
		}
		if err != nil {
			// This is a Harness-owned read-only check before calling ANY
			// RuntimeCanceller, native or custom. The private direct proof
			// certifies only that no cancellation callback ran in this
			// attempt; the underlying Store error proves nothing itself.
			return kernel.Snapshot{}, false, cancellationReadNoEffect{cause: err}
		}
		// Root cancellation is also physical execution authority. Verify the
		// original frozen root before any native/custom Cancel callback or
		// acceptance of a coincidentally terminal same-ID Run. This direct
		// pre-effect proof permits safe cancellation-intent retirement.
		if root != nil {
			// Only the original root Turn's caller can supply root authority.
			// A foreign same-ID Invocation from a DIFFERENT Turn cannot lend
			// its identity to a remotely owned RunRelation descendant.
			if root.ParentItemID != "" || root.ExecutionRefID != runID {
				return kernel.Snapshot{}, true, cancellationReadNoEffect{cause: ErrConflict}
			}
			if err := validateRootRuntimeOwnership(*root, current); err != nil {
				return kernel.Snapshot{}, true, cancellationReadNoEffect{cause: err}
			}
		}
		// The same route and immutable parent proof gates observation and
		// control; a matching same-ID Run cannot substitute an owner.
		delegation, parentRun, err := runner.verifiedLocalRelation(ctx, current, true)
		if err != nil {
			if delegatedChild != nil {
				err = errors.Join(ErrHandoffCancellationUnverified, err)
			}
			if runner.nativeBudget() && nativeKernel {
				return current, true, cancellationReadNoEffect{cause: err}
			}
			return kernel.Snapshot{}, true, err
		}
		if terminalRuntimeStatus(current.Run.Status) {
			if root != nil || nativeSelected {
				if err := runner.reconcileTerminalRootRetirement(ctx, current); err != nil {
					return current, true, err
				}
			}
			// The original route-specific ownership proof above owns
			// physical settlement semantics. Native Agent checks
			// unresolved Tool/Model intents before reaching here;
			// independently routed Handoffs retain their own external
			// cleanup and reconciliation contract instead of inheriting it.
			return current, true, nil
		}
		// An A2A shadow Run is local evidence of a remote task, not the
		// task itself. The default native Kernel Cancel has no A2A RPC;
		// only an explicitly composed RuntimeCanceller can own remote
		// cleanup and its verified terminal outcome.
		remoteRoute := nativeKernel && current.Run.Kind == handoff.RemoteAgentRunKind &&
			delegatedChild != nil && runner.standardRemoteCoordinator() != nil
		if nativeKernel && current.Run.Kind == handoff.RemoteAgentRunKind && !remoteRoute {
			return kernel.Snapshot{}, true, fmt.Errorf("remote Agent Run %q: %w", runID, ErrHandoffCancellationUnverified)
		}
		nativeCall := nativeKernel && !remoteRoute
		callCtx := ctx
		var effect Item
		if !nativeCall {
			var claimErr error
			callCtx, effect, claimErr = runner.beginHostCancellationEffect(ctx, current)
			if claimErr != nil {
				return kernel.Snapshot{}, true, claimErr
			}
		}
		var cancelled kernel.Snapshot
		if remoteRoute {
			cancelled, err = runner.standardRemoteCoordinator().CancelRemoteDelegation(callCtx, runner.runtime, delegation, parentRun, current)
			if err != nil {
				// A protocol error or a post-effect Store conflict is NOT
				// the custom callback's pre-dispatch no-effect proof.
				return cancelled, true, ErrCancellationOutcomeUnknown
			}
		} else {
			cancelled, err = runner.cancellation.Cancel(callCtx, runID, current.Run.Revision, strings.TrimSpace(reason))
		}
		if delegatedChild != nil && err == nil &&
			(!handoff.VerifiedChildRun(delegation, parentRun, cancelled) ||
				!terminalRuntimeStatus(cancelled.Run.Status)) {
			// A custom canceller returning the correct identity but still
			// Running has not confirmed cancellation. Do not close the intent.
			return kernel.Snapshot{}, true, ErrHandoffCancellationUnverified
		}
		if !nativeCall && err == nil {
			// A Host's returned Snapshot is not a Kernel control receipt merely
			// because Cancel returned nil. Require the exact original Run and
			// a matching, locally committed terminal fact before any Turn or
			// child projection. In particular a fabricated foreign Cancelled
			// snapshot or a still-Running reply cannot settle the intent.
			verified, verifyErr := runner.verifiedCustomCancellationOutcome(ctx, current, cancelled)
			if verifyErr != nil {
				if delegatedChild != nil {
					return kernel.Snapshot{}, true, ErrHandoffCancellationUnverified
				}
				return kernel.Snapshot{}, true, ErrRuntimeCancellationUnverified
			}
			cancelled = verified
			if remoteRoute && !runner.standardRemoteCoordinator().ObservesRemoteShadow(ctx, runner.runtime, delegation, cancelled) {
				return kernel.Snapshot{}, true, ErrCancellationOutcomeUnknown
			}
		}
		if delegatedChild != nil && err == nil && cancelled.Run.Kind == agent.RunKind {
			// A callback may enter and return without changing Run.Revision
			// between the read-side witness and Cancel CAS. Recheck the actual
			// complete callback history on the now-terminal Run before closing
			// the durable intent; a borrowed/opaque retirement stays unknown.
			coordinator := runner.handoffs.(*handoff.Coordinator)
			if !coordinator.VerifiedLocalDelegationControl(ctx, runner.runtime, delegation, cancelled) {
				return kernel.Snapshot{}, true, ErrHandoffCancellationUnverified
			}
		}
		if nativeCall {
			if kernel.IsControlNoEffect(err) {
				if errors.Is(err, kernel.ErrConflict) {
					// A pre-Store CAS revision mismatch is safe to retry,
					// but preserve its native proof if all retries exhaust.
					lastNativeNoEffect = err
					continue
				}
				return cancelled, true, err
			}
			// Even a native Store ErrConflict can follow an uncertain
			// write-capable CAS. Never retry or release its intent blindly.
			return cancelled, true, err
		}
		outcome := CancellationEffectUnknown
		if err == nil {
			outcome = CancellationEffectSettled
		}
		if errors.Is(err, kernel.ErrConflict) {
			outcome = CancellationEffectNoEffect
		}
		// The mixed Host's explicit RunKind delegation contract attests that
		// this exact callback did only native control. This root-only proof
		// settles ONLY its own claim, never a later child cleanup claim.
		if kernel.IsControlNoEffect(err) && errors.Is(err, kernel.ErrChildExecutionBusy) {
			if owner, ok := runner.cancellation.(LocalKernelRunKindCanceller); ok && owner.OwnsKernelRunKind(runner.runtime, current.Run.Kind) {
				outcome = CancellationEffectNoEffect
			}
		}
		if outcome != CancellationEffectUnknown {
			if receiptErr := runner.finishHostCancellationEffect(ctx, effect, outcome); receiptErr != nil {
				return cancelled, true, errors.Join(err, receiptErr)
			}
		}
		if errors.Is(err, kernel.ErrConflict) {
			// The custom RuntimeCanceller contract reserves this error
			// for attempts with absolutely no external cleanup.
			continue
		}
		return cancelled, true, err
	}
	if nativeKernel && lastNativeNoEffect != nil {
		return kernel.Snapshot{}, false, lastNativeNoEffect
	}
	// Every custom canceller conflict was contractually no-effect.
	return kernel.Snapshot{}, false, kernel.ErrConflict
}

// A custom Canceller's claimed terminal state must be observed in the same
// Kernel that Harness owns. Caller-supplied metadata is insufficient: a
// foreign Kernel can have an identical Run ID and even identical task fields.
// Do not wrap arbitrary Load errors (especially kernel.ErrConflict) in this
// unknown-outcome proof; they must not become a custom no-effect receipt.
func (runner *Runner) verifiedCustomCancellationOutcome(
	ctx context.Context, before, returned kernel.Snapshot,
) (kernel.Snapshot, error) {
	if !sameCancellationRunOrigin(before.Run, returned.Run) ||
		!terminalRuntimeStatus(returned.Run.Status) {
		return kernel.Snapshot{}, ErrRuntimeCancellationUnverified
	}
	observed, err := runner.runtime.Load(ctx, before.Run.ID)
	if err != nil || !sameCancellationRunOrigin(before.Run, observed.Run) ||
		observed.Run.Status != returned.Run.Status ||
		!terminalRuntimeStatus(observed.Run.Status) {
		return kernel.Snapshot{}, ErrRuntimeCancellationUnverified
	}
	return observed, nil
}

func sameCancellationRunOrigin(expected, candidate kernel.Run) bool {
	return expected.ID != "" && candidate.ID == expected.ID &&
		candidate.Kind == expected.Kind && candidate.Actor == expected.Actor &&
		candidate.Thread == expected.Thread && candidate.Goal == expected.Goal &&
		candidate.RequestID == expected.RequestID && candidate.ParentRunID == expected.ParentRunID
}

// A correct ChildRunner alone cannot authorize cancellation when the Host
// composed a different Kernel (or an unproven custom route) as Canceller.
// Only exact native Kernel identity or an explicit Host-owned local Kernel
// contract may close the second half of Handoff cancellation ownership.
func (runner *Runner) localHandoffCancellerOwnsKernel() bool {
	if runner == nil || runner.runtime == nil {
		return false
	}
	if native, ok := runner.cancellation.(*kernel.Runtime); ok {
		return native == runner.runtime
	}
	owner, ok := runner.cancellation.(LocalKernelCanceller)
	return ok && owner.OwnsKernelRuntime(runner.runtime)
}

// Local Agent and remote A2A control require independent, exact-Kernel
// authority declarations. Mixed cancellers may claim only Agent RunKind.
func (runner *Runner) localRelationCancellerOwnsKernel(kind kernel.RunKind) bool {
	if runner.localHandoffCancellerOwnsKernel() {
		return true
	}
	owner, ok := runner.cancellation.(LocalKernelRunKindCanceller)
	return ok && runner.runtime != nil && owner.OwnsKernelRunKind(runner.runtime, kind)
}

func (runner *Runner) remoteHandoffCancellerOwnsDelegation(
	ctx context.Context, frozen handoff.Delegation, child kernel.Snapshot,
) bool {
	if runner == nil || runner.runtime == nil {
		return false
	}
	if coordinator := runner.standardRemoteCoordinator(); coordinator != nil {
		return coordinator.ObservesRemoteShadow(ctx, runner.runtime, frozen, child)
	}
	owner, ok := runner.cancellation.(RemoteDelegationCanceller)
	return ok && owner.OwnsRemoteDelegation(ctx, runner.runtime, frozen, child)
}

// localHandoffCancellationAuthority proves that a coincidentally present
// Kernel Run belongs to the exact routed child before any local Cancel or
// terminal projection. The immutable Delegation Item names its original
// parent Run, and the child's hashed input supplies its frozen task envelope.
// Missing/corrupt evidence is unknown remote outcome, never local authority.
func (runner *Runner) originalHandoffParent(ctx context.Context, child Invocation) (handoff.Delegation, Invocation, error) {
	c, ok := runner.handoffs.(*handoff.Coordinator)
	return readOriginalHandoffParent(ctx, runner.store, runner.relationReader, child, !ok || !c.ObservesLocalKernelRunner(runner.runtime))
}

// The native budget binding, negative decision and ordinary child control
// share this exact original Host selection. Only a legacy fixed opaque
// route may lack the complete execution envelope.
func readOriginalHandoffParent(ctx context.Context, store Store, relations ContextRunRelationSource, child Invocation, requireEnvelope bool) (handoff.Delegation, Invocation, error) {
	unverified := func(e error) (handoff.Delegation, Invocation, error) {
		return handoff.Delegation{}, Invocation{}, errors.Join(ErrHandoffCancellationUnverified, e)
	}
	binding, err := frozenChildRoot(child)
	if err != nil {
		return unverified(err)
	}
	d, err := decodeFrozenHandoffDelegation(child.Input)
	if err != nil {
		return unverified(err)
	}
	item, err := store.GetItem(ctx, child.ParentItemID)
	if err != nil {
		return unverified(err)
	}
	if item.ID != child.ParentItemID || item.TurnID != child.TurnID || item.Seq == 0 ||
		item.Kind != ItemDelegation || item.Status != ItemStarted || item.RunID != child.ExecutionRefID || item.InvocationID == "" {
		return unverified(ErrConflict)
	}
	var payload delegationItemPayload
	if !decodeTypedAuthorityJSON(item.Payload, &payload) || !matchesDelegationPayload(payload, d, payload.ParentRunID) ||
		d.ChildRunID != child.ExecutionRefID || strings.TrimSpace(payload.ParentRunID) == "" {
		return unverified(ErrConflict)
	}
	parent, err := store.GetInvocation(ctx, item.InvocationID)
	if err != nil || parent.ID != item.InvocationID || parent.ID == child.ID || parent.TurnID != child.TurnID || parent.ExecutionRefID != payload.ParentRunID {
		return unverified(errors.Join(ErrConflict, err))
	}
	if requireEnvelope && payload.Execution == nil {
		return unverified(ErrConflict)
	}
	if d.NativeLocal || requireEnvelope && relations != nil {
		if _, valid := frozenDelegationAdmissionPayload(child, item); !valid || invocationAttempt(parent) != binding.immediateParent() {
			return unverified(ErrConflict)
		}
		if _, err = verifyProtectiveInvocation(ctx, store, child); err != nil {
			return unverified(err)
		}
		root, err := loadTopLevelInvocation(ctx, store, child.TurnID)
		if err != nil {
			return unverified(err)
		}
		if _, err = verifyFrozenChildAttempts(ctx, store, child, root); err != nil {
			return unverified(err)
		}
		if relations == nil {
			return unverified(ErrConflict)
		}
		edge, err := relations.GetByChild(ctx, child.ExecutionRefID)
		if err != nil || edge.Kind != runrelation.KindDelegation || edge.ParentRunID != parent.ExecutionRefID || edge.ChildRunID != child.ExecutionRefID || edge.OwnerNodeID != d.ID {
			return unverified(errors.Join(ErrConflict, err))
		}
	}
	return d, parent, nil
}

func (runner *Runner) localHandoffCancellationAuthority(ctx context.Context, child Invocation, candidate kernel.Snapshot) (handoff.Delegation, kernel.Run, error) {
	d, parent, err := runner.originalHandoffParent(ctx, child)
	if err != nil {
		return handoff.Delegation{}, kernel.Run{}, err
	}
	source, err := runner.runtime.Load(ctx, parent.ExecutionRefID)
	if err != nil || !handoff.VerifiedChildRun(d, source.Run, candidate) ||
		(d.NativeLocal && !agent.MatchesFrozenOrigin(candidate, child.Input)) {
		return handoff.Delegation{}, kernel.Run{}, errors.Join(ErrHandoffCancellationUnverified, err)
	}
	return d, source.Run, nil
}

func (runner *Runner) syncTerminalDelegations(ctx context.Context, turn Turn, runs map[string]kernel.Snapshot) error {
	items, err := listAllItems(ctx, runner.store, turn.ID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Kind != ItemDelegation || item.Status != ItemStarted {
			continue
		}
		snapshot, found := runs[item.RunID]
		if !found || !terminalRuntimeStatus(snapshot.Run.Status) {
			continue
		}
		var payload delegationItemPayload
		if err = json.Unmarshal(item.Payload, &payload); err != nil {
			return err
		}
		invocation, loadErr := runner.store.GetInvocation(ctx, item.InvocationID)
		if loadErr != nil {
			return loadErr
		}
		delegation := handoff.Delegation{
			ID: payload.DelegationID, MemberID: payload.MemberID, MemberRevision: payload.MemberRevision, ChildRunID: payload.ChildRunID,
			Goal: payload.Goal, Status: handoff.Status(snapshot.Run.Status),
		}
		if payload.Execution != nil {
			delegation = *payload.Execution
		}
		delegation = handoff.ProjectChild(delegation, snapshot)
		_, err = runner.recordDelegationItem(ctx, turn, invocation, delegation, delegationItemStatus(delegation.Status), item.ID)
		if err != nil {
			return err
		}
	}
	return nil
}

// cancelOwnedDescendants uses the same mixed ownership closure as preflight.
// A completed Application intermediary is not a boundary that drops native
// grandchildren, and absent RunRelation capability never selects siblings.
func (runner *Runner) cancelOwnedDescendants(ctx context.Context, turn Turn, rootRunID, reason string) (map[string]kernel.Snapshot, error) {
	root, err := runner.store.GetInvocationByExecutionRefID(ctx, rootRunID)
	if err != nil && !errors.Is(err, ErrNotFound) || err == nil && root.TurnID != turn.ID {
		return nil, errors.Join(ErrConflict, err)
	}
	var invocations []Invocation
	var targets map[string]struct{}
	rootID := ""
	if err == nil && root.ParentItemID == "" {
		rootID = root.ID
		invocations, err = runner.store.ListInvocations(ctx, turn.ID)
	} else {
		invocations, targets, _, err = runner.collectCancellationScope(ctx, turn, rootRunID)
	}
	if err != nil {
		return nil, err
	}
	var selected []Invocation
	var seeds []string
	for _, inv := range invocations {
		if _, owned := targets[inv.ID]; targets != nil && !owned {
			continue
		}
		selected = append(selected, inv)
		if inv.ExecutionRefID != rootRunID && inv.ExecutionRefID != "" {
			seeds = append(seeds, inv.ExecutionRefID)
		}
	}
	_, historical, err := runner.currentCancellationChildren(ctx, turn, rootID, selected)
	if err != nil {
		return nil, err
	}
	delegated, err := runner.delegatedRunIDs(ctx, turn.ID, targets)
	if err != nil {
		return nil, err
	}
	return runner.cancelRelatedRuns(ctx, rootRunID, withoutHistoricalRuns(append(seeds, delegated...), historical), reason)
}

// The original cancellation election and the verified never-dispatched
// Handoff history authorize a product update, not a Kernel Run or receipt.
func (runner *Runner) projectUndispatchedHandoffCancellation(ctx context.Context, child Invocation, reason string) error {
	intent := ctx.Value(cancellationElectionKey{}).(Item) // checked by cancelRuntimeRun
	turn, err := loadVerifiedTurnIdentity(ctx, runner.store, intent.TurnID)
	if err != nil {
		return err
	}
	d, parent, err := runner.originalHandoffParent(ctx, child)
	if err != nil || d.NativeLocal {
		return errors.Join(ErrHandoffCancellationUnverified, err)
	}
	root, err := loadTopLevelInvocation(ctx, runner.store, turn.ID)
	if err != nil {
		return err
	}
	if _, err = verifyFrozenChildAttempts(ctx, runner.store, child, root); err != nil {
		return err
	}
	if !terminalInvocationStatus(child.Status) {
		if child.Status != InvocationAccepted && child.Status != InvocationWaitingInput {
			return ErrExecutionOutcomeUnknown
		}
		child, err = runner.syncInvocationStatus(withOriginalWaitingCancellationWitness(ctx, child), child, InvocationCancelled, "run.cancelled", reason)
		if err != nil {
			return err
		}
	}
	d.Status, d.ErrorCode, d.Error = handoff.Status(child.Status), child.ErrorCode, child.ErrorDetail
	if err = runner.projectChildInvocationOutcome(ctx, child, nil); err != nil {
		return err
	}
	_, err = runner.recordDelegationItem(ctx, turn, parent, d, delegationItemStatus(d.Status), child.ParentItemID)
	return err
}

// An Accepted delegated child that never acquired a physical grant, or whose
// only grants are durably marked rejected BEFORE HandoffStarter, provably did
// not reach its external ChildRunner. Every other grant (including a settled
// Handoff-start receipt) may already represent a running remote effect.
// Cancellation intent is already persisted before this read, so no late
// physical grant can race the no-effect proof.
func (runner *Runner) handoffPhysicalEffectPossible(ctx context.Context, child Invocation) (bool, error) {
	// An unrelated Invocation's empty grant ledger is not no-effect proof
	// for a raw Feature edge occupying the same Run ID. Even this early
	// shortcut must establish that the relation belongs to this Handoff.
	if runner.relationReader != nil {
		relation, err := runner.relationReader.GetByChild(ctx, child.ExecutionRefID)
		if err != nil && !errors.Is(err, runrelation.ErrNotFound) {
			return true, err
		}
		if err == nil {
			item, itemErr := runner.store.GetItem(ctx, child.ParentItemID)
			var payload delegationItemPayload
			if itemErr != nil || json.Unmarshal(item.Payload, &payload) != nil ||
				item.Kind != ItemDelegation || item.TurnID != child.TurnID ||
				relation.Kind != runrelation.KindDelegation ||
				relation.ChildRunID != child.ExecutionRefID ||
				relation.ParentRunID != payload.ParentRunID || relation.OwnerNodeID != payload.DelegationID ||
				payload.ChildRunID != child.ExecutionRefID {
				return true, errors.Join(ErrRuntimeCancellationUnverified, itemErr)
			}
		}
	}
	if _, err := frozenChildRoot(child); err != nil {
		// Pre-hard-cut records lack an authoritative root/Attempt proof.
		// Never infer a safe no-effect outcome from their missing grant.
		return true, errors.Join(ErrHandoffCancellationUnverified, err)
	}
	if d, e := decodeFrozenHandoffDelegation(child.Input); e == nil && d.NativeLocal {
		turn, e := loadVerifiedTurnIdentity(ctx, runner.store, child.TurnID)
		if e != nil {
			return true, e
		}
		_, e = runner.nativeChildDecision(ctx, turn, child)
		if e == nil {
			return false, nil
		}
		if errors.Is(e, kernel.ErrNotFound) || errors.Is(e, kernel.ErrAlreadyExists) {
			return true, nil
		}
		return true, e
	}
	items, err := runner.loadGlobalPhysicalEvidence(ctx, child.TurnID)
	if err != nil {
		return true, err
	}
	return acceptedChildPhysicalEffectPossible(items, child), nil
}

func (runner *Runner) delegatedRunIDs(ctx context.Context, turnID string, targets map[string]struct{}) ([]string, error) {
	items, err := listAllItems(ctx, runner.store, turnID)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0)
	for _, item := range items {
		if _, owned := targets[item.InvocationID]; item.Kind != ItemDelegation || targets != nil && !owned {
			continue
		}
		var payload delegationItemPayload
		if err = json.Unmarshal(item.Payload, &payload); err != nil {
			return nil, err
		}
		if runID := strings.TrimSpace(payload.ChildRunID); runID != "" {
			result = append(result, runID)
		}
	}
	return result, nil
}

func (runner *Runner) cancelRelatedRuns(
	ctx context.Context,
	rootRunID string,
	seedRunIDs []string,
	reason string,
) (map[string]kernel.Snapshot, error) {
	rootRunID = strings.TrimSpace(rootRunID)
	if rootRunID == "" {
		return nil, ErrInvalidRequest
	}
	queue := append([]string{rootRunID}, seedRunIDs...)
	visited := make(map[string]struct{}, len(queue))
	result := make(map[string]kernel.Snapshot)
	for len(queue) > 0 {
		runID := strings.TrimSpace(queue[0])
		queue = queue[1:]
		if _, ok := visited[runID]; ok {
			continue
		}
		visited[runID] = struct{}{}
		if runID != rootRunID {
			snapshot, found, err := runner.cancelRuntimeRun(ctx, runID, reason, nil)
			if err != nil {
				return nil, err
			}
			if found {
				result[runID] = snapshot
			}
		}
		children, err := runner.relatedChildRunIDs(ctx, runID)
		if err != nil {
			return nil, err
		}
		queue = append(queue, children...)
	}
	return result, nil
}

// Apply the same missing-Run proof to direct targets and traversed children.
func (runner *Runner) verifyMissingRelatedCancellation(ctx context.Context, runID, reason string) error {
	intent, bound := ctx.Value(cancellationElectionKey{}).(Item)
	if !bound || !ValidCancellationFenceIntentAdmission(intent) || intent.Seq == 0 {
		return ErrRuntimeCancellationUnverified
	}
	turn, err := loadVerifiedTurnIdentity(ctx, runner.store, intent.TurnID)
	if err != nil {
		return err
	}
	proven, err := runner.missingRunCancellation(ctx, turn, runID, &reason)
	if !proven || err != nil {
		return errors.Join(ErrRuntimeCancellationUnverified, err)
	}
	return nil
}

func (runner *Runner) relatedChildRunIDs(ctx context.Context, runID string) ([]string, error) {
	if runner.relationReader == nil {
		return nil, nil
	}
	relations, err := runner.relationReader.ListChildren(ctx, runID)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(relations))
	for _, relation := range relations {
		result = append(result, relation.ChildRunID)
	}
	return result, nil
}

func (runner *Runner) syncInvocationRuns(
	ctx context.Context,
	turn Turn,
	runs map[string]kernel.Snapshot,
) error {
	invocations, err := runner.store.ListInvocations(ctx, turn.ID)
	if err != nil {
		return err
	}
	for _, invocation := range invocations {
		runtimeSnapshot, ok := runs[strings.TrimSpace(invocation.ExecutionRefID)]
		if !ok || strings.TrimSpace(invocation.ParentItemID) == "" {
			continue
		}
		if err = runner.syncChildInvocationWithRetry(ctx, turn, invocation.ID, runtimeSnapshot); err != nil {
			return err
		}
	}
	return nil
}

func (runner *Runner) syncChildInvocationWithRetry(
	ctx context.Context,
	turn Turn,
	invocationID string,
	runtimeSnapshot kernel.Snapshot,
) error {
	for range maxRuntimeSyncRetryAttempts {
		invocation, err := runner.store.GetInvocation(ctx, invocationID)
		if err != nil {
			return err
		}
		if terminalInvocationStatus(invocation.Status) {
			return nil
		}
		_, err = runner.syncChildInvocationSnapshot(ctx, turn, invocation, runtimeSnapshot, true)
		if errors.Is(err, ErrCancellationOutcomeUnknown) {
			return err
		}
		if !errors.Is(err, ErrConflict) {
			return err
		}
	}
	return ErrConflict
}

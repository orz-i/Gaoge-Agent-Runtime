package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
	if delegatedChild != nil {
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
			return kernel.Snapshot{}, false, nil
		}
		// Only the fixed original ChildRunner's explicit same-Kernel
		// composition proof can authorize a local physical Cancel. No
		// heuristic fallback for custom/dynamic Handoff routes.
		coordinator, ok := runner.handoffs.(*handoff.Coordinator)
		if !ok || !coordinator.OwnsLocalKernelRunner(runner.runtime) ||
			!runner.localHandoffCancellerOwnsKernel() {
			return kernel.Snapshot{}, false, fmt.Errorf("routed Handoff Run %q has no local execution-owner/canceller proof: %w", runID, ErrHandoffCancellationUnverified)
		}
	}
	// For ordinary Root/Feature Runs as well, a native Canceller must be
	// exactly the Runtime whose Run was loaded. Keep Handoff's earlier
	// route-specific check and safe never-dispatched case authoritative.
	// A wrong native Runtime did no physical work; its rejection is a
	// Harness-minted pre-callback proof for cancellation-intent recovery.
	if native, ok := runner.cancellation.(*kernel.Runtime); ok && native != runner.runtime {
		return kernel.Snapshot{}, false, cancellationReadNoEffect{cause: fmt.Errorf("native Canceller is not the Harness Kernel: %w", ErrConflict)}
	}
	var lastNativeNoEffect error
	for range maxRuntimeSyncRetryAttempts {
		current, err := runner.runtime.Load(ctx, runID)
		if errors.Is(err, kernel.ErrNotFound) {
			// A routed Handoff can be executing in another Kernel. Its
			// absence from this Kernel is NOT proof that external cleanup
			// succeeded, even when the Handoff Start grant is settled.
			if delegatedChild != nil && !terminalInvocationStatus(delegatedChild.Status) {
				possible, proofErr := runner.handoffPhysicalEffectPossible(ctx, *delegatedChild)
				if proofErr != nil {
					return kernel.Snapshot{}, false, proofErr
				}
				if possible {
					return kernel.Snapshot{}, false, fmt.Errorf("remote Handoff Run %q: %w", runID, ErrHandoffCancellationUnverified)
				}
			}
			return kernel.Snapshot{}, false, runner.verifyMissingRelatedCancellation(ctx, runID)
		}
		if err != nil {
			// This is a Harness-owned read-only check before calling ANY
			// RuntimeCanceller, native or custom. The private direct proof
			// certifies only that no cancellation callback ran in this
			// attempt; the underlying Store error proves nothing itself.
			return kernel.Snapshot{}, false, cancellationReadNoEffect{cause: err}
		}
		// A RunID collision in the local Kernel is not authority to cancel
		// a routed Handoff. Verify the full frozen child task and parent
		// origin before either accepting a terminal state or issuing Cancel.
		var parentRun kernel.Run
		var delegation handoff.Delegation
		if delegatedChild != nil {
			delegation, parentRun, err = runner.localHandoffCancellationAuthority(ctx, *delegatedChild, current)
			if err != nil {
				return kernel.Snapshot{}, true, err
			}
		}
		if terminalRuntimeStatus(current.Run.Status) {
			return current, true, nil
		}
		// An A2A shadow Run is local evidence of a remote task, not the
		// task itself. The default native Kernel Cancel has no A2A RPC;
		// only an explicitly composed RuntimeCanceller can own remote
		// cleanup and its verified terminal outcome.
		if nativeKernel && current.Run.Kind == handoff.RemoteAgentRunKind {
			return kernel.Snapshot{}, true, fmt.Errorf("A2A shadow Run %q: %w", runID, ErrHandoffCancellationUnverified)
		}
		cancelled, err := runner.cancellation.Cancel(ctx, runID, current.Run.Revision, strings.TrimSpace(reason))
		if delegatedChild != nil && err == nil &&
			(!handoff.VerifiedChildRun(delegation, parentRun, cancelled) ||
				!terminalRuntimeStatus(cancelled.Run.Status)) {
			// A custom canceller returning the correct identity but still
			// Running has not confirmed cancellation. Do not close the intent.
			return kernel.Snapshot{}, true, ErrHandoffCancellationUnverified
		}
		if !nativeKernel && err == nil {
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
		}
		if nativeKernel {
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
		candidate.RequestID == expected.RequestID
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

// localHandoffCancellationAuthority proves that a coincidentally present
// Kernel Run belongs to the exact routed child before any local Cancel or
// terminal projection. The immutable Delegation Item names its original
// parent Run, and the child's hashed input supplies its frozen task envelope.
// Missing/corrupt evidence is unknown remote outcome, never local authority.
func (runner *Runner) localHandoffCancellationAuthority(
	ctx context.Context, child Invocation, candidate kernel.Snapshot,
) (handoff.Delegation, kernel.Run, error) {
	unverified := func(err error) (handoff.Delegation, kernel.Run, error) {
		return handoff.Delegation{}, kernel.Run{}, errors.Join(ErrHandoffCancellationUnverified, err)
	}
	if _, err := frozenChildRoot(child); err != nil {
		return unverified(err)
	}
	var delegation handoff.Delegation
	if err := json.Unmarshal(child.Input, &delegation); err != nil {
		return unverified(err)
	}
	item, err := runner.store.GetItem(ctx, child.ParentItemID)
	if err != nil {
		return unverified(err)
	}
	if item.ID != child.ParentItemID || item.TurnID != child.TurnID || item.Seq == 0 ||
		item.Kind != ItemDelegation || item.Status != ItemStarted ||
		item.RunID != child.ExecutionRefID || item.InvocationID == "" {
		return unverified(ErrConflict)
	}
	var payload delegationItemPayload
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		return unverified(err)
	}
	if payload.DelegationID != delegation.ID || payload.ChildRunID != delegation.ChildRunID ||
		payload.ChildRunID != child.ExecutionRefID ||
		payload.MemberID != delegation.MemberID || payload.MemberRevision != delegation.MemberRevision ||
		payload.RoleID != delegation.RoleID || payload.RoleRevision != delegation.RoleRevision ||
		payload.RoleName != delegation.RoleName || payload.Goal != delegation.Goal ||
		strings.TrimSpace(payload.ParentRunID) == "" ||
		(payload.Execution != nil && !sameHandoffFrozenExecution(delegation, *payload.Execution)) {
		return unverified(ErrConflict)
	}
	parent, err := runner.store.GetInvocation(ctx, item.InvocationID)
	if err != nil {
		return unverified(err)
	}
	if parent.ID != item.InvocationID || parent.ID == child.ID ||
		parent.TurnID != child.TurnID || parent.ExecutionClass != ExecutionAgent ||
		parent.ExecutionRefID != payload.ParentRunID {
		return unverified(ErrConflict)
	}
	parentSnapshot, err := runner.runtime.Load(ctx, payload.ParentRunID)
	if err != nil {
		return unverified(err)
	}
	if parentSnapshot.Run.ID != payload.ParentRunID ||
		!handoff.VerifiedChildRun(delegation, parentSnapshot.Run, candidate) {
		return unverified(ErrConflict)
	}
	return delegation, parentSnapshot.Run, nil
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
			delegation.Status = handoff.Status(snapshot.Run.Status)
		}
		if snapshot.Run.Status == kernel.RunStatusCompleted && snapshot.Result != nil {
			delegation.Result = append(json.RawMessage(nil), snapshot.Result.Content...)
		}
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
	invocations, targets, _, err := runner.collectCancellationScope(ctx, turn, rootRunID)
	if err != nil {
		return nil, err
	}
	var selected []Invocation
	var seeds []string
	rootInvocationID := ""
	for _, invocation := range invocations {
		if _, owned := targets[invocation.ID]; !owned {
			continue
		}
		selected = append(selected, invocation)
		if invocation.ExecutionRefID == rootRunID {
			rootInvocationID = invocation.ID
			continue
		}
		if invocation.ExecutionClass == ExecutionApplication {
			if _, err = runner.cancelApplicationInvocation(ctx, turn, invocation, reason); err != nil {
				return nil, err
			}
		} else {
			seeds = append(seeds, invocation.ExecutionRefID)
		}
	}
	runs, err := runner.cancelRelatedRuns(ctx, rootRunID, seeds, reason)
	if err != nil {
		return nil, err
	}
	if err = runner.addMissingInvocationCancellations(ctx, runs, selected, rootInvocationID, reason); err != nil {
		return nil, err
	}
	return runs, nil
}

func (runner *Runner) cancelTurnDescendants(
	ctx context.Context,
	turnID string,
	rootInvocationID string,
	rootRunID string,
	reason string,
) (map[string]kernel.Snapshot, error) {
	invocations, err := runner.store.ListInvocations(ctx, turnID)
	if err != nil {
		return nil, err
	}
	seeds := childInvocationRunIDs(invocations, rootInvocationID)
	delegatedRunIDs, err := runner.delegatedRunIDs(ctx, turnID)
	if err != nil {
		return nil, err
	}
	seeds = append(seeds, delegatedRunIDs...)
	runs, err := runner.cancelRelatedRuns(ctx, rootRunID, seeds, reason)
	if err != nil {
		return nil, err
	}
	if err := runner.addMissingInvocationCancellations(ctx, runs, invocations, rootInvocationID, reason); err != nil {
		return nil, err
	}
	return runs, nil
}

func childInvocationRunIDs(invocations []Invocation, rootInvocationID string) []string {
	result := make([]string, 0, len(invocations))
	for _, invocation := range invocations {
		if invocation.ID != rootInvocationID && strings.TrimSpace(invocation.ExecutionRefID) != "" {
			result = append(result, invocation.ExecutionRefID)
		}
	}
	return result
}

func (runner *Runner) addMissingInvocationCancellations(
	ctx context.Context, runs map[string]kernel.Snapshot,
	invocations []Invocation,
	rootInvocationID string,
	reason string,
) error {
	for _, invocation := range invocations {
		if invocation.ID == rootInvocationID || terminalInvocationStatus(invocation.Status) {
			continue
		}
		runID := strings.TrimSpace(invocation.ExecutionRefID)
		if _, ok := runs[runID]; runID != "" && !ok {
			if isHandoffDelegationInvocation(invocation) {
				// A caller could omit a Run ID during traversal. Apply
				// the same persisted physical-effect proof before emitting
				// any synthetic no-Run cancellation.
				possible, proofErr := runner.handoffPhysicalEffectPossible(ctx, invocation)
				if proofErr != nil {
					return proofErr
				}
				if possible {
					return fmt.Errorf("remote Handoff Run %q: %w", runID, ErrHandoffCancellationUnverified)
				}
			}
			runs[runID] = cancelledRuntimeSnapshot(runID, reason)
		}
	}
	return nil
}

// An Accepted delegated child that never acquired a physical grant, or whose
// only grants are durably marked rejected BEFORE HandoffStarter, provably did
// not reach its external ChildRunner. Every other grant (including a settled
// Handoff-start receipt) may already represent a running remote effect.
// Cancellation intent is already persisted before this read, so no late
// physical grant can race the no-effect proof.
func (runner *Runner) handoffPhysicalEffectPossible(ctx context.Context, child Invocation) (bool, error) {
	if _, err := frozenChildRoot(child); err != nil {
		// Pre-hard-cut records lack an authoritative root/Attempt proof.
		// Never infer a safe no-effect outcome from their missing grant.
		return true, errors.Join(ErrHandoffCancellationUnverified, err)
	}
	items, err := listAllItems(ctx, runner.store, child.TurnID)
	if err != nil {
		return false, err
	}
	if acceptedChildPhysicalEffectPossible(items, child) {
		return true, nil
	}
	return false, nil
}

func (runner *Runner) delegatedRunIDs(ctx context.Context, turnID string) ([]string, error) {
	items, err := listAllItems(ctx, runner.store, turnID)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0)
	for _, item := range items {
		if item.Kind != ItemDelegation {
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
		snapshot, found, err := runner.cancelDescendantRun(ctx, rootRunID, runID, reason)
		if err != nil {
			return nil, err
		}
		if found {
			result[runID] = snapshot
		}
		children, err := runner.relatedChildRunIDs(ctx, runID)
		if err != nil {
			return nil, err
		}
		queue = append(queue, children...)
	}
	return result, nil
}

func (runner *Runner) cancelDescendantRun(
	ctx context.Context,
	rootRunID string,
	runID string,
	reason string,
) (kernel.Snapshot, bool, error) {
	if runID == rootRunID {
		return kernel.Snapshot{}, false, nil
	}
	return runner.cancelRuntimeRun(ctx, runID, reason)
}

// Apply the same missing-Run proof to direct targets and traversed children.
func (runner *Runner) verifyMissingRelatedCancellation(ctx context.Context, runID string) error {
	if runner.relationReader == nil {
		return nil
	}
	// An indexed RunRelation records execution ownership, not local Kernel
	// residence. Team Members, Workflow Effects and GroupChat speakers can
	// run on an independent ChildRunner without a Harness Invocation. A
	// missing local Run is therefore NOT an authoritative no-effect or
	// terminal receipt for a related physical child. Preserve the intent
	// for the original execution owner to reconcile or quarantine.
	relation, relationErr := runner.relationReader.GetByChild(ctx, runID)
	if errors.Is(relationErr, runrelation.ErrNotFound) {
		return nil // A non-relation seed uses its own admission proof.
	}
	if relationErr != nil {
		return relationErr
	}
	if relation.ChildRunID != runID || strings.TrimSpace(relation.ParentRunID) == "" {
		return ErrConflict
	}
	// A frozen Handoff child whose grant proves physical dispatch never
	// entered its ChildRunner retains the existing synthetic no-effect case.
	// RunRelation alone does not override a stronger child-owned grant proof.
	if owner, ownerErr := runner.store.GetInvocationByExecutionRefID(ctx, runID); ownerErr == nil {
		if isHandoffDelegationInvocation(owner) {
			possible, proofErr := runner.handoffPhysicalEffectPossible(ctx, owner)
			if proofErr != nil {
				return proofErr
			}
			if !possible {
				return nil
			}
		}
	} else if !errors.Is(ownerErr, ErrNotFound) {
		return ownerErr
	}
	return fmt.Errorf("RunRelation child %q has no local physical cancellation receipt: %w", runID, ErrRuntimeCancellationUnverified)
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
		_, err = runner.syncChildInvocationSnapshot(ctx, turn, invocation, runtimeSnapshot)
		if !errors.Is(err, ErrConflict) {
			return err
		}
	}
	return ErrConflict
}

func cancelledRuntimeSnapshot(runID string, reason string) kernel.Snapshot {
	return kernel.Snapshot{Run: kernel.Run{
		ID: runID, Status: kernel.RunStatusCancelled,
		ErrorCode: "run.cancelled", ErrorDetail: strings.TrimSpace(reason),
	}}
}

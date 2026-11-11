package harness

import (
	"context"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// terminalTransitionWitness is a private, ephemeral admission capability.
// Only the original Runner call path that observed a Kernel terminal result,
// a conclusively no-effect Start, or an Application's committed result can
// mint one. The Store checks this together with original durable facts in
// the same Turn CAS; neither a terminal Status nor a settled dispatch receipt
// can manufacture this capability.
type terminalTransitionKind uint8

const (
	terminalFromKernel terminalTransitionKind = iota + 1
	terminalFromNoEffect
	terminalFromApplication
	terminalFromHostCancellation
)

type terminalTransitionWitness struct {
	turnID, invocationID, runID string
	target                      TurnStatus
	source                      terminalTransitionKind
}

type terminalTransitionWitnessKey struct{}

func withTerminalWitness(ctx context.Context, owner Invocation, target TurnStatus, source terminalTransitionKind) context.Context {
	return context.WithValue(ctx, terminalTransitionWitnessKey{}, terminalTransitionWitness{
		turnID: owner.TurnID, invocationID: owner.ID,
		runID: owner.ExecutionRefID, target: target, source: source,
	})
}

// withOriginalKernelTerminalWitness can only be issued from the
// original ownership-verified Kernel Snapshot path. The full frozen
// ownership checks are performed by the caller before this method.
func withOriginalKernelTerminalWitness(
	ctx context.Context, owner Invocation, snapshot kernel.Snapshot,
) (context.Context, error) {
	target, err := turnStatusFromRuntime(snapshot.Run.Status)
	if err != nil || !terminalTurnStatus(target) ||
		snapshot.Run.ID == "" || snapshot.Run.ID != owner.ExecutionRefID {
		return ctx, ErrConflict
	}
	return withTerminalWitness(ctx, owner, target, terminalFromKernel), nil
}

// withOriginalNoEffectTerminalWitness is for the physical Start error
// boundary after the invoked adapter positively proves no effect. It is
// never a wrapper for an ordinary/ambiguous external error.
func withOriginalNoEffectTerminalWitness(
	ctx context.Context, owner Invocation, proofErr error,
) (context.Context, error) {
	if !isDispatchProvenNoEffect(proofErr) && !errors.Is(proofErr, ErrApplicationNoEffect) {
		return ctx, ErrConflict
	}
	return withTerminalWitness(ctx, owner, TurnFailed, terminalFromNoEffect), nil
}

// withOriginalApplicationTerminalWitness is only called after the owning
// Host Application result/no-effect cancellation has been accepted by its
// original Runner path. Its identity is still checked by the Store.
func withOriginalApplicationTerminalWitness(ctx context.Context, owner Invocation, target TurnStatus) context.Context {
	return withTerminalWitness(ctx, owner, target, terminalFromApplication)
}

// withOriginalWaitingCancellationWitness is issued solely after the
// selected waiting Invocation wins cancellation admission. It is not
// transferable to another sibling Invocation, even in the same Turn.
func withOriginalWaitingCancellationWitness(ctx context.Context, owner Invocation) context.Context {
	return withTerminalWitness(ctx, owner, TurnCancelled, terminalFromHostCancellation)
}

// VerifiedInvocationTerminalWitness reads an otherwise unforgeable private
// admission token. SQL adapters may inspect it but cannot construct it.
// It binds the *exact* owner Run, Invocation, Turn and terminal outcome.
func VerifiedInvocationTerminalWitness(ctx context.Context, owner Invocation, target InvocationStatus) bool {
	proof, ok := ctx.Value(terminalTransitionWitnessKey{}).(terminalTransitionWitness)
	if !ok || proof.turnID == "" || proof.invocationID == "" || proof.runID == "" ||
		proof.turnID != owner.TurnID || proof.invocationID != owner.ID ||
		proof.runID != owner.ExecutionRefID {
		return false
	}
	return proof.target == turnStatusFromInvocation(target)
}

// VerifiedTurnTerminalWitness uses the original single root Invocation to
// prevent a child callback/Run from authorizing a terminal parent Turn.
func VerifiedTurnTerminalWitness(ctx context.Context, root Invocation, target TurnStatus) bool {
	if root.ParentItemID != "" || root.ID == "" || root.TurnID == "" {
		return false
	}
	proof, ok := ctx.Value(terminalTransitionWitnessKey{}).(terminalTransitionWitness)
	return ok && proof.target == target && proof.turnID == root.TurnID &&
		proof.invocationID == root.ID && proof.runID == root.ExecutionRefID &&
		terminalTurnStatus(target)
}

func turnStatusFromInvocation(status InvocationStatus) TurnStatus {
	switch status {
	case InvocationCompleted:
		return TurnCompleted
	case InvocationFailed:
		return TurnFailed
	case InvocationCancelled:
		return TurnCancelled
	case InvocationAccepted, InvocationRunning, InvocationWaitingInput, InvocationPausedBudget:
		return ""
	default:
		return ""
	}
}

// ValidTurnTerminalDescendants checks the original owning Turn's durable
// child Invocations under the same Turn-serializing Store admission lock.
// A completed/failed/cancelled root cannot silently declare its Turn final
// while a separately claimed child is still Accepted, Running, Waiting or
// Paused. Root Kernel snapshots do not prove descendant completion.
func ValidTurnTerminalDescendants(root Invocation, owners []Invocation, items []Item) bool {
	if root.ID == "" || root.TurnID == "" || root.ParentItemID != "" {
		return false
	}
	// The original Handoff child binds the entire ancestor Attempt chain.
	// All owners are already loaded under the same Turn-serializing Store
	// lock as terminal CAS; comparing them here prevents an old child Run
	// from settling its parent after an intermediate Attempt rotated.
	byID := make(map[string]Invocation, len(owners))
	for _, owner := range owners {
		if owner.TurnID != root.TurnID || owner.ID == "" {
			return false
		}
		if _, duplicate := byID[owner.ID]; duplicate {
			return false
		}
		byID[owner.ID] = owner
	}
	if stored, ok := byID[root.ID]; !ok ||
		invocationAttempt(stored) != invocationAttempt(root) {
		return false
	}
	for _, owner := range owners {
		if owner.ID == root.ID {
			continue
		}
		// Every Handoff child, including one already marked Completed,
		// must retain the exact original sealed ChildRun and ancestor
		// Attempt provenance. A corrupt input can make the permissive
		// Handoff classification fail; its reserved hid_ parent identity
		// must still force strict source verification, not fall through
		// as an ordinary terminal Agent child.
		handoffCandidate := isHandoffDelegationInvocation(owner) ||
			(owner.ExecutionClass == ExecutionAgent &&
				owner.CapabilityKey == CapabilityAgent &&
				strings.HasPrefix(owner.ParentItemID, "hid_"))
		if handoffCandidate {
			_, binding, valid := verifiedTerminalHandoffFrozenSource(root, owner)
			if !valid {
				return false
			}
			for _, ancestor := range binding.Parents {
				parent, found := byID[ancestor.InvocationID]
				if !found || parent.ParentItemID == "" ||
					parent.ID == owner.ID || invocationAttempt(parent) != ancestor {
					return false
				}
			}
		}
		if terminalInvocationStatus(owner.Status) {
			continue
		}
		if !VerifiedTerminalHandoffChild(root, owner, items) {
			return false
		}
	}
	return true
}

// ValidTurnTerminalTransition checks original immutable state, intended
// outcome, the exact root source and private one-operation witness. This is
// the *single* transition rule shared by Memory and SQL terminal CAS. It
// does not itself assert that outstanding Host/Dispatch/Cancel effects are
// settled: those independent durable predicates are checked under the
// same Store lock by the caller.
func ValidTurnTerminalTransition(ctx context.Context, current, requested Turn, root Invocation) bool {
	if current.ID != requested.ID || !sameTurnIdentity(current, requested) ||
		current.Status == "" || !terminalTurnStatus(requested.Status) {
		return false
	}
	if terminalTurnStatus(current.Status) {
		return current.Status == requested.Status
	}
	if root.TurnID != requested.ID {
		return false
	}
	// An Application root can complete its original Host result while a
	// descendant remains WaitingInput. The subsequently elected Cancel
	// operation may close the still-nonterminal Turn after cleaning up
	// that subtree. It cannot rewrite the already-committed root status.
	// This exception requires both an exact Application-root witness AND
	// the original verified cancellation-control context. Store admission
	// separately checks that the durable same-Turn cancellation intent
	// remains active under its original Turn lock.
	if root.Status != invocationStatusFromTurn(requested.Status) {
		if requested.Status != TurnCancelled ||
			root.ExecutionClass != ExecutionApplication ||
			(root.Status != InvocationCompleted && root.Status != InvocationFailed) ||
			!VerifiedCancellationProjection(ctx, requested.ID) {
			return false
		}
	}
	return VerifiedTurnTerminalWitness(ctx, root, requested.Status)
}

// VerifiedOriginalStartNoEffect is the one durable substitute for the
// ORIGINAL physical Start attempt's no-effect witness. A successfully
// rejected Control, Resume, Approval or Handoff is only evidence about
// that *other* operation, never proof that the owning Kernel Run failed.
//
// Both Store implementations use this exact predicate under their original
// Turn admission lock. Require a UNIQUE first-party Start/Start-Retry grant
// for the current Invocation/Run and its unique pre-effect receipt. A second
// grant for this original Run is ambiguous, even if it also reports no effect.
func VerifiedOriginalStartNoEffect(items []Item, owner Invocation) bool {
	receipts := executionDispatchReceiptIndex(items)
	originals := 0
	for _, grant := range items {
		if grant.ParentItemID != ExecutionDispatchParentItemID ||
			grant.InvocationID != owner.ID || grant.RunID != owner.ExecutionRefID ||
			grant.TurnID != owner.TurnID {
			continue
		}
		phase, valid := VerifiedExecutionDispatchGrant(grant)
		if !valid || (phase != "start" && phase != "start-retry") {
			return false
		}
		// A retry Start belongs to the newly allocated Attempt, not the
		// original one. Distinguish these from control/middleware retries.
		if (phase == "start-retry" && owner.Attempt <= 1) ||
			(phase == "start" && owner.Attempt > 1) {
			return false
		}
		boundary, settled := VerifiedExecutionDispatchReceipt(grant, receipts[grant.ID])
		if !settled {
			return false
		}
		switch boundary {
		case "physical_dispatch_rejected", "feature_start_no_effect", "native_agent_start_no_effect":
			originals++
		default:
			return false
		}
	}
	return originals == 1
}

// retryKernelTerminalWitness is a private, one-operation capability minted
// only after the original Runner read the terminal Kernel Run and verified
// its complete frozen execution ownership. A persisted Failed status or
// effectful Start-return alone cannot mint another physical Attempt.
type retryKernelTerminalWitness struct {
	turnID, invocationID, runID string
	attempt                     int
	target                      InvocationStatus
}

type retryKernelTerminalWitnessKey struct{}

func withOriginalKernelRetryWitness(
	ctx context.Context, owner Invocation, snapshot kernel.Snapshot,
) (context.Context, error) {
	target, err := turnStatusFromRuntime(snapshot.Run.Status)
	if err != nil || !terminalTurnStatus(target) ||
		!retryableInvocationStatus(owner.Status) ||
		target != turnStatusFromInvocation(owner.Status) ||
		snapshot.Run.ID != owner.ExecutionRefID || owner.Attempt < 1 {
		return ctx, ErrConflict
	}
	return context.WithValue(ctx, retryKernelTerminalWitnessKey{}, retryKernelTerminalWitness{
		turnID: owner.TurnID, invocationID: owner.ID,
		runID: owner.ExecutionRefID, attempt: owner.Attempt, target: owner.Status,
	}), nil
}

// withOriginalRetryAuthority performs the read-only Kernel side of a
// subsequent Retry before Store attempts atomic Attempt rotation. The
// original physical Run may be missing ONLY when the Store's durable exact
// Start no-effect or no-physical-effect path independently permits Retry.
// Any existing Running/Waiting/Paused Kernel Run categorically forbids a
// second physical execution, even if historical Harness rows say Failed.
func (runner *Runner) withOriginalRetryAuthority(
	ctx context.Context, owner Invocation,
) (context.Context, error) {
	if owner.ExecutionClass == ExecutionApplication {
		// Host-owned Application has no Kernel Run of its own; its distinct
		// Host effect/cancellation fences remain the Retry authority.
		return ctx, nil
	}
	if runner == nil || runner.runtime == nil || owner.ExecutionRefID == "" {
		return ctx, ErrConflict
	}
	snapshot, err := runner.runtime.Load(ctx, owner.ExecutionRefID)
	if errors.Is(err, kernel.ErrNotFound) {
		// The Store still requires a valid original Start no-effect proof
		// when physical grants exist. Absence alone is not a success vote.
		return ctx, nil
	}
	if err != nil {
		return ctx, err
	}
	if !terminalRuntimeStatus(snapshot.Run.Status) {
		return ctx, ErrConflict
	}
	if owner.ParentItemID == "" {
		if err := validateRootRuntimeOwnership(owner, snapshot); err != nil {
			return ctx, err
		}
	} else {
		if err := runner.validateChildRuntimeOwnership(ctx, owner, snapshot); err != nil {
			return ctx, err
		}
	}
	return withOriginalKernelRetryWitness(ctx, owner, snapshot)
}

func verifiedKernelRetryWitness(ctx context.Context, owner Invocation) bool {
	proof, ok := ctx.Value(retryKernelTerminalWitnessKey{}).(retryKernelTerminalWitness)
	return ok && proof.turnID != "" && proof.invocationID != "" &&
		proof.runID != "" && proof.attempt > 0 &&
		proof.turnID == owner.TurnID && proof.invocationID == owner.ID &&
		proof.runID == owner.ExecutionRefID && proof.attempt == owner.Attempt &&
		proof.target == owner.Status && retryableInvocationStatus(owner.Status)
}

// RetryOriginalOperationEvidenceValid is the read-side original physical
// effect authority for a new Attempt. The same immutable grant/receipt
// lineage is checked by Memory and SQL under the original Turn lock:
//
//   - No observed physical grant: the existing trusted Host/Kernel terminal
//     status and separate Cancellation/child fences remain authoritative.
//   - One original Start conclusively rejected before any effect: an exact
//     immutable Start no-effect receipt is sufficient after a lost ACK.
//   - An effectful original Start: only the original Runner, after verifying
//     its Kernel Run has the *matching* terminal outcome, can mint the
//     private per-Run, per-Attempt witness carried into the Store CAS.
//
// A harmless Control/Resume failure, an effectful Dispatch return or an
// already-written historical Failed status cannot independently authorize a
// second physical Run. No second persisted execution state machine is added.
func RetryOriginalOperationEvidenceValid(ctx context.Context, items []Item, owner Invocation) bool {
	receipts := executionDispatchReceiptIndex(items)
	hasGrant := false
	originalStarts := 0
	for _, grant := range items {
		if grant.ParentItemID != ExecutionDispatchParentItemID ||
			grant.TurnID != owner.TurnID || grant.InvocationID != owner.ID ||
			grant.RunID != owner.ExecutionRefID {
			continue
		}
		hasGrant = true
		phase, valid := VerifiedExecutionDispatchGrant(grant)
		if !valid {
			return false
		}
		if _, settled := VerifiedExecutionDispatchReceipt(grant, receipts[grant.ID]); !settled {
			return false
		}
		switch phase {
		case "start":
			if owner.Attempt != 1 {
				return false
			}
			originalStarts++
		case "start-retry":
			if owner.Attempt <= 1 {
				return false
			}
			originalStarts++
		}
	}
	if !hasGrant {
		return true
	}
	if VerifiedOriginalStartNoEffect(items, owner) {
		return true
	}
	return originalStarts == 1 && verifiedKernelRetryWitness(ctx, owner)
}

// ValidInvocationTerminalTransitionWithEvidence is the single original
// terminal authority rule shared by both Store implementations. The
// no-effect input may only be computed from the complete immutable grant
// and receipt chain under the original Turn transaction lock.
func ValidInvocationTerminalTransitionWithEvidence(
	ctx context.Context, current, requested Invocation, originalNoEffect bool,
) bool {
	if current.ID != requested.ID || !sameInvocationIdentity(current, requested) ||
		current.ExecutionRefID != requested.ExecutionRefID ||
		!terminalInvocationStatus(requested.Status) {
		return false
	}
	if terminalInvocationStatus(current.Status) {
		return current.Status == requested.Status
	}
	if VerifiedInvocationTerminalWitness(ctx, current, requested.Status) {
		return true
	}
	return requested.Status == InvocationFailed && originalNoEffect
}

func ValidInvocationTerminalTransition(ctx context.Context, current, requested Invocation) bool {
	return ValidInvocationTerminalTransitionWithEvidence(ctx, current, requested, false)
}

// A persisted no-effect original root may be projected Failed after a
// crash even when its first-party ephemeral witness was lost. The same
// proof does NOT authorize Completed or Cancelled.
func ValidTurnTerminalTransitionWithEvidence(
	ctx context.Context, current, requested Turn, root Invocation, originalNoEffect bool,
) bool {
	if ValidTurnTerminalTransition(ctx, current, requested, root) {
		return true
	}
	return originalNoEffect && requested.Status == TurnFailed &&
		!terminalTurnStatus(current.Status) && current.ID == requested.ID &&
		sameTurnIdentity(current, requested) && root.ParentItemID == "" &&
		root.TurnID == requested.ID && root.Status == InvocationFailed
}

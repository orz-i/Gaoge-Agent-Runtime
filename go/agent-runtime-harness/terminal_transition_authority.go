package harness

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// terminalTransitionWitness is a private, ephemeral admission capability.
// Only the original Runner call path that observed a Kernel terminal result
// or a conclusively no-effect Start can mint one. The Store checks this together with original durable facts in
// the same Turn CAS; neither a terminal Status nor a settled dispatch receipt
// can manufacture this capability.
type terminalTransitionWitness struct {
	turnID, invocationID, runID string
	target                      TurnStatus
}

type terminalTransitionWitnessKey struct{}

func withTerminalWitness(ctx context.Context, owner Invocation, target TurnStatus) context.Context {
	return context.WithValue(ctx, terminalTransitionWitnessKey{}, terminalTransitionWitness{
		turnID: owner.TurnID, invocationID: owner.ID,
		runID: owner.ExecutionRefID, target: target,
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
	return withTerminalWitness(ctx, owner, target), nil
}

// withOriginalNoEffectTerminalWitness is for the physical Start error
// boundary after the invoked adapter positively proves no effect. It is
// never a wrapper for an ordinary/ambiguous external error.
func withOriginalNoEffectTerminalWitness(
	ctx context.Context, owner Invocation, proofErr error,
) (context.Context, error) {
	if !isDispatchProvenNoEffect(proofErr) {
		return ctx, ErrConflict
	}
	return withTerminalWitness(ctx, owner, TurnFailed), nil
}

// withOriginalWaitingCancellationWitness is issued solely after the
// selected waiting Invocation wins cancellation admission. It is not
// transferable to another sibling Invocation, even in the same Turn.
func withOriginalWaitingCancellationWitness(ctx context.Context, owner Invocation) context.Context {
	return withTerminalWitness(ctx, owner, TurnCancelled)
}

// VerifiedInvocationTerminalWitness reads an otherwise unforgeable private
// admission token. SQL adapters may inspect it but cannot construct it.
// It binds the *exact* owner Run, Invocation, Turn and terminal outcome.
func VerifiedInvocationTerminalWitness(ctx context.Context, owner Invocation, target InvocationStatus) bool {
	return terminalWitnessMatches(ctx, owner, turnStatusFromInvocation(target))
}

func VerifiedTurnTerminalWitness(ctx context.Context, root Invocation, target TurnStatus) bool {
	return root.ParentItemID == "" && terminalWitnessMatches(ctx, root, target)
}

func terminalWitnessMatches(ctx context.Context, owner Invocation, target TurnStatus) bool {
	proof, ok := ctx.Value(terminalTransitionWitnessKey{}).(terminalTransitionWitness)
	return ok && terminalTurnStatus(target) && proof.target == target &&
		proof.turnID != "" && proof.invocationID != "" && proof.runID != "" &&
		proof.turnID == owner.TurnID && proof.invocationID == owner.ID && proof.runID == owner.ExecutionRefID
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
	return ValidTurnTerminalDescendantsWithBudget(root, owners, items, nil)
}

// ValidTurnTerminalDescendantsWithBudget consumes the same immutable Attempt
// ancestry as cancellation. The optional original ledger is read under the
// owning Store Turn lock; historical children never bind to the new Root.
func ValidTurnTerminalDescendantsWithBudget(root Invocation, owners []Invocation, items []Item, ledger *budget.Ledger) bool {
	if root.ID == "" || root.TurnID == "" || root.ParentItemID != "" {
		return false
	}
	return validateChildEffects(owners, items, root, ledger, true, false) == nil
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
	// A single Agent root cannot terminalize a different Turn outcome.
	if root.Status != invocationStatusFromTurn(requested.Status) {
		return false
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
	total, starts, rejected, valid := originalDispatchEvidence(items, owner)
	return valid && total == 1 && starts == 1 && rejected
}

// Read the original Host effect history once. Retry may accept a settled
// control in addition to Start only with a verified original Kernel result.
func originalDispatchEvidence(items []Item, owner Invocation) (total, starts int, rejected, valid bool) {
	receipts := executionDispatchReceiptIndex(items)
	for _, grant := range items {
		if grant.ParentItemID != ExecutionDispatchParentItemID ||
			grant.InvocationID != owner.ID || grant.RunID != owner.ExecutionRefID ||
			grant.TurnID != owner.TurnID {
			continue
		}
		total++
		phase, ok := VerifiedExecutionDispatchGrant(grant)
		boundary, settled := VerifiedExecutionDispatchReceipt(grant, receipts[grant.ID])
		if !ok || !settled {
			return total, starts, false, false
		}
		if phase == "start" || phase == "start-retry" {
			if (phase == "start" && owner.Attempt != 1) || (phase == "start-retry" && owner.Attempt <= 1) {
				return total, starts, false, false
			}
			starts++
			rejected = boundary == "physical_dispatch_rejected" || boundary == "native_agent_start_no_effect"
		}
	}
	return total, starts, rejected, true
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
	ctx context.Context, turn Turn, owner Invocation,
) (context.Context, error) {
	if runner == nil || runner.runtime == nil || owner.ExecutionRefID == "" || owner.ParentItemID != "" {
		return ctx, ErrConflict
	}
	snapshot, err := runner.runtime.Load(ctx, owner.ExecutionRefID)
	if errors.Is(err, kernel.ErrNotFound) {
		if d, native, e := runner.readUncreatedRootDecision(ctx, turn, owner); native || e != nil {
			if e != nil || invocationStatusFromTurn(TurnStatus(d.Status)) != owner.Status {
				return ctx, errors.Join(ErrConflict, e)
			}
		}
		// Opaque dispatches retain their independent original effect proof.
		return ctx, nil
	}
	if err != nil {
		return ctx, err
	}
	if !terminalRuntimeStatus(snapshot.Run.Status) {
		return ctx, ErrConflict
	}
	if err := validateRootRuntimeOwnership(owner, snapshot); err != nil {
		return ctx, err
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
	total, starts, rejected, valid := originalDispatchEvidence(items, owner)
	return valid && (total == 0 || starts == 1 && (total == 1 && rejected || verifiedKernelRetryWitness(ctx, owner)))
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

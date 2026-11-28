package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// SupportsManagedRecovery identifies the original first-party recovery selection.
func (r *Runner) SupportsManagedRecovery() bool {
	return r != nil && r.managedRecovery
}

func (r *Runner) managedOriginalDispatch(ctx context.Context, id string) (kernel.ManagedHostBinding, bool) {
	b, ok := r.runtime.ManagedDispatch(ctx, id)
	if !r.SupportsManagedRecovery() || !ok || r.activePhysicalGrant == nil || r.activeRunLease == nil || !b.MatchesOriginalGrant(*r.activePhysicalGrant) {
		return kernel.ManagedHostBinding{}, false
	}
	return b, true
}

func (r *Runner) qualifyManagedModelBoundary(ctx context.Context, s kernel.Snapshot, state runState, inv ModelInvocation, mutation *budget.LedgerMutation) (*kernel.ManagedSafePoint, error) {
	if inv.Purpose == ModelInvocationContextSummary {
		if !validSummaryNativePhase(state) {
			return nil, ErrInvalidRequest
		}
	}
	binding, ok := r.managedOriginalDispatch(ctx, s.Run.ID)
	if !ok {
		if inv.Purpose == ModelInvocationContextSummary {
			return nil, kernel.ErrExecutionRequired
		}
		return nil, nil
	}
	raw, eligible, err := r.freezeManagedObligations(ctx, s, state, inv, binding, mutation)
	if err != nil {
		return nil, err
	}
	if !eligible || !knownRootStatePhysicalSettlement(state) {
		if inv.Purpose == ModelInvocationContextSummary && inv.Status == ModelInvocationPending {
			return nil, ErrRootOutcomeUnknown
		}
		return nil, nil
	}
	id := "managed-safe-" + kernel.ManagedDigest(struct {
		Grant, Invocation string
		Revision          uint64
	}{r.activePhysicalGrant.ID, inv.ID, s.Run.Revision + 1})
	return r.managedTransitionPoint(inv, binding, id, raw), nil
}

// The concrete phase validators share construction of the original point.
// Only commitNativeTransition seals and commits it together with the mutation.
func (r *Runner) managedTransitionPoint(inv ModelInvocation, binding kernel.ManagedHostBinding, id string, lifecycle json.RawMessage) *kernel.ManagedSafePoint {
	return &kernel.ManagedSafePoint{ID: id, RecoveryID: kernel.ManagedRecoveryID(id, binding), Host: binding,
		OriginalGrant: *r.activePhysicalGrant, OriginalLease: *r.activeRunLease, ModelInvocationID: inv.ID, ModelRequestHash: inv.RequestHash,
		LifecycleVersion: kernel.ManagedLifecycleAgentObligations, LifecycleSource: lifecycle}
}

// RecoverManagedContinuation never claims an interrupted callback returned.
// It resumes only the exact persisted concrete continuation, after Kernel-first
// read-only authorization and native mode admission. The common native callback-entry
// transaction consumes the successor's recovery qualification before entry.
func (r *Runner) RecoverManagedContinuation(ctx context.Context, original kernel.ManagedTransfer) (kernel.Snapshot, kernel.ManagedClosureProof, error) {
	return r.recoverManagedContinuation(ctx, original, "", "")
}

// CancelManagedContinuation is the explicitly authorized native cancellation
// command. The Store arbitrates it against successor admission.
func (r *Runner) CancelManagedContinuation(ctx context.Context, original kernel.ManagedTransfer, reason string) (kernel.Snapshot, kernel.ManagedClosureProof, error) {
	return r.recoverManagedContinuation(ctx, original, kernel.ManagedCancel, reason)
}

func (r *Runner) recoverManagedContinuation(ctx context.Context, original kernel.ManagedTransfer, requiredMode kernel.ManagedRecoveryMode, reason string) (kernel.Snapshot, kernel.ManagedClosureProof, error) {
	if !r.SupportsManagedRecovery() {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, ErrRootOutcomeUnknown
	}
	id := original.SafePoint.Host.RunID
	current, err := r.runtime.LoadManagedRecovery(ctx, id)
	if err != nil {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, err
	}
	if !current.Valid() || kernel.ManagedDigest(current) != kernel.ManagedDigest(original) {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, kernel.ErrExecutionConflict
	}
	if expected, ok := r.runtime.ManagedDispatch(ctx, id); ok && kernel.ManagedDigest(expected) != kernel.ManagedDigest(current.SafePoint.Host) {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, kernel.ErrExecutionConflict
	}
	if err = r.runtime.VerifyManagedApproval(ctx, current.SafePoint.Host); err != nil {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, errors.Join(ErrRootOutcomeUnknown, err)
	}
	if _, err = r.validateManagedObligations(current.SafePoint); err != nil {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, err
	}
	// Native successor admission freezes the original selection in the
	// current transfer. Successors cannot publish a new safe point for that
	// dispatch, so a Host reservation lookup is not a recovery authority.
	if requiredMode != "" && current.Selection.Mode != "" && current.Selection.Mode != requiredMode {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, kernel.ErrExecutionConflict
	}
	if current.Selection.Mode == kernel.ManagedResume && current.SuccessorGrant.ID != "" &&
		!current.Resolved() {
		// A real returned successor is ordinary Agent work awaiting its
		// original receipt. Never re-admit it or mint another callback.
		if source, loadErr := r.runtime.Load(ctx, id); loadErr == nil {
			if scope, scopeErr := r.RootDeferredCleanupEligibility(ctx, id, source.Run.Revision); scopeErr == nil && scope.grant == current.SuccessorGrant {
				next, receiptErr := r.ReconcileRootDeferredChildren(ctx, id, source.Run.Revision, scope)
				if errors.Is(receiptErr, ErrToolOutcomeUnknown) && !scope.RetirementOnly() {
					return next, kernel.ManagedClosureProof{}, nil
				}
				if receiptErr != nil {
					return next, kernel.ManagedClosureProof{}, receiptErr
				}
				p, closeErr := r.runtime.ManagedRecoveryClosure(ctx, current.SafePoint.ID)
				return next, p, closeErr
			}
		}
	}
	if current.Resolved() {
		p, e := r.runtime.ManagedRecoveryClosure(ctx, current.SafePoint.ID)
		return p.Snapshot(), p, e
	}
	var cancelReason []string
	if requiredMode == kernel.ManagedCancel {
		cancelReason = []string{reason}
	}
	selected, obligations, err := r.authorizeManagedFrontier(ctx, current, cancelReason...)
	if err != nil {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, err
	}
	if requiredMode != "" && selected.Authorization().Mode != requiredMode {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, kernel.ErrExecutionConflict
	}
	if selected.Authorization().Mode == kernel.ManagedCancel {
		point := selected.Point()
		_, _, e := r.runtime.ApplyControl(ctx, point.Host.RunID, point.Source.Run.Revision, selected.CancellationGuard(), kernel.Mutation{}, nil)
		if e != nil {
			return kernel.Snapshot{}, kernel.ManagedClosureProof{}, e
		}
		p, e := r.runtime.ManagedRecoveryClosure(ctx, point.ID)
		return p.Snapshot(), p, e
	}
	point := selected.Point()
	scoped, v, err := r.runtime.AdmitManagedContinuation(ctx, selected, r.runExecution.OwnerID, r.runExecution.LeaseDuration)
	if err != nil {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, err
	}
	snapshot, err := scoped.Load(ctx, id)
	if err != nil {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, err
	}
	// Admission reserves the exact original obligation. Recheck the
	// same point before entry, including any read-only Context rollover.
	if obligations, err = r.preflightManagedPoint(ctx, point, snapshot); err != nil {
		return snapshot, kernel.ManagedClosureProof{}, err
	}
	if _, err := decodeState(snapshot.State); err != nil {
		return snapshot, kernel.ManagedClosureProof{}, err
	}
	// Rebind only the original stored dispatch. This is a storage read before
	// native callback entry; it does not create a second physical dispatch.
	ctx, err = r.runtime.BindManagedDispatch(ctx, point.Host)
	if err != nil {
		return snapshot, kernel.ManagedClosureProof{}, err
	}
	// Carry the exact original ledger into native Run admission; do not
	// Ensure/Register or rebind the original managed source during recovery.
	recovering := *r
	recovering.managedObligationSource = &obligations
	result, runErr := recovering.executeNativeSegment(ctx, snapshot, scoped, v.Grant, &v.Lease, false, v.Grant.AdmissionOperation, v.Grant.AdmissionSourceHash, func(next context.Context, owner *Runner, source kernel.Snapshot) (kernel.Snapshot, error) {
		return owner.invokeNativeRun(next, source, runInvocation(plugin.RunResume, source), func(drive context.Context) (kernel.Snapshot, error) {
			frontier, err := owner.recoverManagedModelFrontier(drive, source, obligations)
			if err != nil {
				return frontier, err
			}
			return owner.drive(drive, frontier)
		})
	})
	closure, closureErr := r.runtime.ManagedRecoveryClosure(context.WithoutCancel(ctx), point.ID)
	if closureErr != nil {
		if runErr == nil {
			if scope, err := r.RootDeferredCleanupEligibility(ctx, id, result.Run.Revision); err == nil && scope.grant == v.Grant && !scope.RetirementOnly() {
				return result, kernel.ManagedClosureProof{}, nil
			}
		}
		return result, kernel.ManagedClosureProof{}, errors.Join(runErr, ErrRootOutcomeUnknown, closureErr)
	}
	return result, closure, runErr
}

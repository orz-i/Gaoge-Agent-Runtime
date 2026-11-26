package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// SupportsManagedContextRecovery selects an explicitly configured concrete
// lifecycle, not a general claim that arbitrary middleware is safe to resume.
func (r *Runner) SupportsManagedContextRecovery() bool {
	return r != nil && r.managedContextRecovery != nil
}

func (r *Runner) commitManagedModelIntent(ctx context.Context, s kernel.Snapshot, state runState, inv ModelInvocation, m kernel.Mutation) (kernel.Snapshot, error) {
	if state.SummaryNativeRecovery && inv.Purpose == ModelInvocationContextSummary {
		return r.commitSummarySafePoint(ctx, s, state, m)
	}
	if r.managedContextRecovery == nil || r.activeRootGrant == nil || r.activeRunLease == nil || s.Run.ParentRunID != "" {
		return r.runtime.Apply(ctx, s.Run.ID, s.Run.Revision, m)
	}
	binding, ok := r.runtime.ManagedDispatch(ctx, s.Run.ID)
	if !ok {
		return r.runtime.Apply(ctx, s.Run.ID, s.Run.Revision, m)
	}
	if r.combinedManaged(state) {
		// A Host reservation selects one original transfer, not a chain
		// of new transferable successor grants.
		if r.activeRootGrant.AdmissionOperation == "managed_resume" {
			return r.runtime.Apply(ctx, s.Run.ID, s.Run.Revision, m)
		}
		raw, eligible, err := r.freezeManagedObligations(ctx, s, state, inv, binding)
		if err != nil {
			return s, err
		}
		if !eligible || !knownRootPhysicalSettlement(s) {
			return r.runtime.Apply(ctx, s.Run.ID, s.Run.Revision, m)
		}
		id := "managed-safe-" + kernel.ManagedDigest(struct {
			Grant, Invocation string
			Revision          uint64
		}{r.activeRootGrant.ID, inv.ID, s.Run.Revision + 1})
		return r.applyManagedSafePoint(ctx, s, inv, m, binding, id, kernel.ManagedLifecycleAgentObligations, raw)
	}
	// Only actual returned operations can describe a suspendable frame. This
	// leaves opaque/incomplete configurations on existing fail-closed semantics.
	lifecycle, ok := callbackscope.ManagedSuspension(ctx, r.managedContextRecovery, inv.Request)
	if !ok || !knownRootPhysicalSettlement(s) || state.ToolDispatchID != "" || state.ToolWait != nil || len(state.DeferredChildren) != 0 || inv.DispatchAttempt != 0 {
		return r.runtime.Apply(ctx, s.Run.ID, s.Run.Revision, m)
	}
	id := "managed-safe-" + kernel.ManagedDigest(struct {
		Grant, Invocation string
		Revision          uint64
	}{r.activeRootGrant.ID, inv.ID, s.Run.Revision + 1})
	return r.applyManagedSafePoint(ctx, s, inv, m, binding, id, "managed-context-v1", lifecycle)
}

// Only the two concrete phase validators above/below may select a point.
// This is the shared original-grant/source commit, not a new transfer
// authority or a re-interpretation of either persisted lifecycle version.
func (r *Runner) applyManagedSafePoint(ctx context.Context, source kernel.Snapshot, inv ModelInvocation, m kernel.Mutation,
	binding kernel.ManagedHostBinding, id, version string, lifecycle json.RawMessage) (kernel.Snapshot, error) {
	point := kernel.ManagedSafePoint{ID: id, RecoveryID: kernel.ManagedRecoveryID(id, binding), Host: binding,
		OriginalGrant: *r.activeRootGrant, OriginalLease: *r.activeRunLease,
		ModelInvocationID: inv.ID, ModelRequestHash: inv.RequestHash, LifecycleVersion: version, LifecycleSource: lifecycle}
	proof := featurecontrol.Seal(source.Run.ID, source.Run.Revision, "managed_intent_source", r.activeRootGrant.ID, source.State, point)
	return r.runtime.ApplyManagedModelIntent(ctx, source.Run.ID, source.Run.Revision, m, point, proof)
}

// Business and Summary both leave the original safe point before claim and
// provider admission. The ordinary managed Context may have no elected
// point; a pending Summary in its native profile requires one.
func (r *Runner) leaveModelSafePoint(ctx context.Context, s kernel.Snapshot, state runState, invocation ModelInvocation) error {
	required := state.SummaryNativeRecovery && invocation.Purpose == ModelInvocationContextSummary && invocation.Status == ModelInvocationPending
	if !required && r.managedContextRecovery == nil {
		return nil
	}
	_, err := r.runtime.LeaveManagedSafePoint(ctx, s.Run.ID)
	if !required && kernel.ManagedRecoveryAbsent(err) {
		return nil
	}
	return err
}

// RecoverManagedContinuation never claims an interrupted callback returned.
// It resumes only the exact persisted concrete continuation, after Kernel-first
// PARK and committed Host mode selection. No configurable callback runs before
// the new owner's explicit LeaveManagedSafePoint ACK.
func (r *Runner) RecoverManagedContinuation(ctx context.Context, id string) (kernel.Snapshot, kernel.ManagedClosureProof, error) {
	if !r.SupportsManagedContextRecovery() && !r.SupportsSummaryNativeRecovery() {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, ErrRootOutcomeUnknown
	}
	current, err := r.runtime.LoadManagedRecovery(ctx, id)
	if err != nil {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, err
	}
	// Check the current profile before consulting Host reservation. Older
	// unbudgeted proofs never acquire new ledger authority from a new handle.
	switch current.SafePoint.LifecycleVersion {
	case kernel.ManagedLifecycleAgentObligations:
		if _, err = r.validateManagedObligations(current.SafePoint); err != nil {
			return kernel.Snapshot{}, kernel.ManagedClosureProof{}, err
		}
	case kernel.ManagedLifecycleWorkingStateSummary:
		if r.nativeBudget != nil {
			return kernel.Snapshot{}, kernel.ManagedClosureProof{}, ErrInvalidRequest
		}
		if err = r.preflightSummaryTransfer(ctx, current); err != nil {
			return kernel.Snapshot{}, kernel.ManagedClosureProof{}, err
		}
	case "managed-context-v1":
		if !r.SupportsManagedContextRecovery() || r.nativeBudget != nil {
			return kernel.Snapshot{}, kernel.ManagedClosureProof{}, ErrRootOutcomeUnknown
		}
	default:
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, ErrRootOutcomeUnknown
	}
	// Once a Host selection exists, later safe points may have moved the Run's
	// current pointer. Reconstruct the ORIGINAL selected transfer from its ID.
	reservation, readErr := r.runtime.ReadManagedReservation(ctx, current.SafePoint.Host, "")
	if readErr == nil {
		if !reservation.Valid() {
			return kernel.Snapshot{}, kernel.ManagedClosureProof{}, kernel.ErrExecutionConflict
		}
		original, e := r.runtime.LoadManagedRecoveryByID(ctx, reservation.SuspensionID)
		if e != nil {
			return kernel.Snapshot{}, kernel.ManagedClosureProof{}, e
		}
		current = original
	} else if !errors.Is(readErr, kernel.ErrNotFound) {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, readErr
	}
	var obligations managedObligations
	if current.SafePoint.LifecycleVersion == kernel.ManagedLifecycleAgentObligations {
		obligations, err = r.preflightCombinedManaged(ctx, current)
		if err != nil {
			return kernel.Snapshot{}, kernel.ManagedClosureProof{}, err
		}
	}
	if current.State == kernel.ManagedClosed || current.State == kernel.ManagedCancelled {
		p, e := r.runtime.ManagedRecoveryClosure(ctx, current.SafePoint.ID)
		if e == nil {
			e = r.ReconcileManagedClosure(ctx, p)
		}
		return p.Snapshot(), p, e
	}
	parked, err := r.runtime.ParkManagedContinuation(ctx, id)
	if err != nil {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, err
	}
	selected, err := r.runtime.SelectManagedContinuation(ctx, parked)
	if err != nil {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, err
	}
	if selected.Reservation().Mode == kernel.ManagedCancel {
		if err = r.cancelManagedBudget(ctx, obligations, id, false); err != nil {
			return kernel.Snapshot{}, kernel.ManagedClosureProof{}, err
		}
		t, e := r.runtime.CancelParkedManagedContinuation(ctx, selected)
		if e != nil {
			return kernel.Snapshot{}, kernel.ManagedClosureProof{}, e
		}
		p, e := r.runtime.ManagedRecoveryClosure(ctx, t.SafePoint.ID)
		if e == nil {
			e = r.ReconcileManagedClosure(ctx, p)
		}
		return p.Snapshot(), p, e
	}
	// The selected RESUME must still see the exact original, noncancelled
	// ledger; a cancelled or missing quota source cannot elect an owner.
	if _, err = r.checkManagedLedger(ctx, obligations, id, false, false); err != nil {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, err
	}
	claimant, err := newManagedClaimantID()
	if err != nil {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, err
	}
	t, err := r.runtime.EnableManagedContinuation(ctx, selected, claimant, r.runExecution.OwnerID, r.runExecution.LeaseDuration)
	if err != nil {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, err
	}
	bound, err := r.runtime.WithExecution(t.SuccessorLease)
	if err != nil {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, err
	}
	// This is BEFORE Hydrate, provider identity methods, clocks, telemetry,
	// observers, catalog discovery, or the ordinary Resume prelude.
	if _, err = bound.LeaveManagedSafePoint(ctx, id); err != nil {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, err
	}
	snapshot, err := bound.Load(ctx, id)
	if err != nil {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, err
	}
	state, err := decodeState(snapshot.State)
	if err != nil {
		return snapshot, kernel.ManagedClosureProof{}, err
	}
	if t.SafePoint.LifecycleVersion == kernel.ManagedLifecycleAgentObligations {
		if _, err = r.validateManagedObligations(t.SafePoint); err != nil || !kernel.ManagedSourceMatches(t.SafePoint, snapshot) {
			return snapshot, kernel.ManagedClosureProof{}, errors.Join(ErrRootOutcomeUnknown, err)
		}
		if _, err = r.checkManagedLedger(ctx, obligations, id, false, false); err != nil {
			return snapshot, kernel.ManagedClosureProof{}, err
		}
	} else if t.SafePoint.LifecycleVersion == kernel.ManagedLifecycleWorkingStateSummary {
		if err = r.validateSummarySafePoint(t.SafePoint); err != nil || !kernel.ManagedSourceMatches(t.SafePoint, snapshot) {
			return snapshot, kernel.ManagedClosureProof{}, errors.Join(ErrRootOutcomeUnknown, err)
		}
	} else {
		inv, ok := activeModelInvocation(state)
		if !ok || inv.ID != t.SafePoint.ModelInvocationID || inv.RequestHash != t.SafePoint.ModelRequestHash || inv.Status != ModelInvocationPending || inv.DispatchAttempt != 0 || !knownRootPhysicalSettlement(snapshot) {
			return snapshot, kernel.ManagedClosureProof{}, ErrRootOutcomeUnknown
		}
	}
	ownerProof := featurecontrol.Seal(id, snapshot.Run.Revision, "agent_physical_owner", t.SuccessorGrant.ID, snapshot.State, snapshot.Checkpoint)
	scoped, err := bound.WithPhysicalGrant(ctx, t.SuccessorGrant, ownerProof)
	if err != nil {
		return snapshot, kernel.ManagedClosureProof{}, err
	}
	// Rebind only the original stored dispatch. This is an actual storage read,
	// after Leave; it does not create a second physical Host dispatch.
	ctx, err = r.runtime.BindManagedDispatch(ctx, t.SafePoint.Host)
	if err != nil {
		return snapshot, kernel.ManagedClosureProof{}, err
	}
	// Carry the exact original ledger into native Run admission; do not
	// Ensure/Register or rebind the original managed source during recovery.
	recovering := *r
	if t.SafePoint.LifecycleVersion == kernel.ManagedLifecycleAgentObligations {
		recovering.managedBudgetSource = &obligations
	}
	result, runErr := recovering.executeOwnedRoot(ctx, snapshot, bound, scoped, t.SuccessorGrant, t.SuccessorLease, false, func(next context.Context, owner *Runner, source kernel.Snapshot) (kernel.Snapshot, error) {
		return owner.invokeNativeRun(next, source, runInvocation(plugin.RunResume, source), func(drive context.Context) (kernel.Snapshot, error) { return owner.resumeRunning(drive, source, state) })
	})
	closure, closureErr := r.runtime.ManagedRecoveryClosure(context.WithoutCancel(ctx), t.SafePoint.ID)
	if closureErr != nil {
		return result, kernel.ManagedClosureProof{}, errors.Join(runErr, ErrRootOutcomeUnknown, closureErr)
	}
	return result, closure, runErr
}

// ReconcileManagedClosure only retires the exact already-closed transfer.
// Harness can repair its original dispatch projection after this operation,
// even when the Host closure ACK was lost. It cannot PARK, ENABLE or drive.
func (r *Runner) ReconcileManagedClosure(ctx context.Context, proof kernel.ManagedClosureProof) error {
	if r == nil || r.runtime == nil || ctx == nil {
		return ErrInvalidRequest
	}
	t := proof.Transfer()
	if !proof.Matches(t.Selection) {
		return kernel.ErrExecutionConflict
	}
	switch t.SafePoint.LifecycleVersion {
	case kernel.ManagedLifecycleAgentObligations:
		if _, err := r.validateManagedObligations(t.SafePoint); err != nil {
			return err
		}
	case kernel.ManagedLifecycleWorkingStateSummary:
		if !r.SupportsSummaryNativeRecovery() {
			return kernel.ErrExecutionConflict
		}
	case "managed-context-v1":
		if !r.SupportsManagedContextRecovery() {
			return kernel.ErrExecutionConflict
		}
	default:
		return kernel.ErrExecutionConflict
	}
	current, err := r.runtime.ManagedRecoveryClosure(ctx, t.SafePoint.ID)
	if err != nil || !current.Matches(t.Selection) ||
		kernel.ManagedDigest(current.Transfer()) != kernel.ManagedDigest(t) ||
		kernel.ManagedDigest(current.Snapshot()) != kernel.ManagedDigest(proof.Snapshot()) {
		return errors.Join(kernel.ErrExecutionConflict, err)
	}
	if t.SafePoint.LifecycleVersion == kernel.ManagedLifecycleAgentObligations && t.State == kernel.ManagedCancelled {
		p, err := r.validateManagedObligations(t.SafePoint)
		if err != nil {
			return err
		}
		if err = r.cancelManagedBudget(ctx, p, t.SafePoint.Host.RunID, true); err != nil {
			return err
		}
	}
	return r.retireManagedClosure(ctx, current)
}

// A closed T4 is not an ACK. Reuse the exact original successor's ordinary
// Mark/Finish/ACK protocol; absence of the active Grant alone never releases
// its lease or certifies completion. Managed cancellation has no successor.
func (r *Runner) retireManagedClosure(ctx context.Context, p kernel.ManagedClosureProof) error {
	t := p.Transfer()
	if t.State != kernel.ManagedClosed {
		return nil
	}
	g, source := t.SuccessorGrant, p.Snapshot()
	receipt, err := r.runtime.LoadOwnedRetirement(ctx, g.ID)
	if err == nil {
		if receipt.Evidence.Grant != g || !receipt.Evidence.Completion.HostCompleted || !receipt.Evidence.Completion.LeaseOwned {
			return kernel.ErrExecutionConflict
		}
		// The exact closed transfer must be part of the immutable retirement.
		matched := false
		for _, closed := range receipt.Closures {
			if kernel.ManagedDigest(closed) == kernel.ManagedDigest(t) {
				matched = true
				break
			}
		}
		if !matched {
			return kernel.ErrExecutionConflict
		}
		found, ackErr := acknowledgeExactOwnedRetirement(ctx, r.runtime, g, source, &receipt.Evidence.Completion)
		if !found {
			return errors.Join(kernel.ErrExecutionConflict, ackErr)
		}
		return ackErr
	}
	if !errors.Is(err, kernel.ErrNotFound) {
		return err
	}
	active, err := r.runtime.LookupChildExecution(ctx, source.Run.ID)
	if err != nil || active != g {
		return errors.Join(kernel.ErrExecutionConflict, err)
	}
	completion, err := r.runtime.LookupOwnedPhysicalCallbackReturn(ctx, g)
	if err != nil || !completion.HostCompleted || !completion.LeaseOwned {
		return errors.Join(kernel.ErrExecutionConflict, err)
	}
	return r.reconcileRootRetirement(ctx, source, g, &completion)
}

// Internal entropy is not an IDSource callback and cannot run user code before
// the newly enabled owner has acknowledged LeaveManagedSafePoint.
func newManagedClaimantID() (string, error) {
	var id [32]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return "managedclaimant-" + hex.EncodeToString(id[:]), nil
}

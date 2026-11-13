package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	if r.managedContextRecovery == nil || r.activeRootGrant == nil || r.activeRunLease == nil || s.Run.ParentRunID != "" {
		return r.runtime.Apply(ctx, s.Run.ID, s.Run.Revision, m)
	}
	binding, ok := r.runtime.ManagedDispatch(ctx, s.Run.ID)
	if !ok {
		return r.runtime.Apply(ctx, s.Run.ID, s.Run.Revision, m)
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
	p := kernel.ManagedSafePoint{ID: id, RecoveryID: kernel.ManagedRecoveryID(id, binding), Host: binding, OriginalGrant: *r.activeRootGrant, OriginalLease: *r.activeRunLease, ModelInvocationID: inv.ID, ModelRequestHash: inv.RequestHash, LifecycleVersion: "managed-context-v1", LifecycleSource: lifecycle}
	proof := featurecontrol.Seal(s.Run.ID, s.Run.Revision, "managed_intent_source", r.activeRootGrant.ID, s.State, p)
	return r.runtime.ApplyManagedModelIntent(ctx, s.Run.ID, s.Run.Revision, m, p, proof)
}

// RecoverManagedContinuation never claims an interrupted callback returned.
// It resumes only the exact persisted concrete continuation, after Kernel-first
// PARK and committed Host mode selection. No configurable callback runs before
// the new owner's explicit LeaveManagedSafePoint ACK.
func (r *Runner) RecoverManagedContinuation(ctx context.Context, id string) (kernel.Snapshot, kernel.ManagedClosureProof, error) {
	if !r.SupportsManagedContextRecovery() {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, ErrRootOutcomeUnknown
	}
	current, err := r.runtime.LoadManagedRecovery(ctx, id)
	if err != nil {
		return kernel.Snapshot{}, kernel.ManagedClosureProof{}, err
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
	if current.State == kernel.ManagedClosed || current.State == kernel.ManagedCancelled {
		p, e := r.runtime.ManagedRecoveryClosure(ctx, current.SafePoint.ID)
		if e == nil {
			e = r.retireManagedClosure(ctx, p)
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
		t, e := r.runtime.CancelParkedManagedContinuation(ctx, selected)
		if e != nil {
			return kernel.Snapshot{}, kernel.ManagedClosureProof{}, e
		}
		p, e := r.runtime.ManagedRecoveryClosure(ctx, t.SafePoint.ID)
		return p.Snapshot(), p, e
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
	inv, ok := activeModelInvocation(state)
	if !ok || inv.ID != t.SafePoint.ModelInvocationID || inv.RequestHash != t.SafePoint.ModelRequestHash || inv.Status != ModelInvocationPending || inv.DispatchAttempt != 0 || !knownRootPhysicalSettlement(snapshot) {
		return snapshot, kernel.ManagedClosureProof{}, ErrRootOutcomeUnknown
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
	result, runErr := r.executeOwnedRoot(ctx, snapshot, bound, scoped, t.SuccessorGrant, t.SuccessorLease, false, func(next context.Context, owner *Runner, source kernel.Snapshot) (kernel.Snapshot, error) {
		return owner.runChain.Invoke(next, runInvocation(plugin.RunResume, source), func(drive context.Context) (kernel.Snapshot, error) { return owner.resumeRunning(drive, source, state) })
	})
	closure, closureErr := r.runtime.ManagedRecoveryClosure(context.WithoutCancel(ctx), t.SafePoint.ID)
	if closureErr != nil {
		return result, kernel.ManagedClosureProof{}, errors.Join(runErr, ErrRootOutcomeUnknown, closureErr)
	}
	return result, closure, runErr
}

// A crash after real Mark+completion but before Finish may repair ONLY the
// already marked original successor. No callback or execution owner is adopted.
func (r *Runner) retireManagedClosure(ctx context.Context, p kernel.ManagedClosureProof) error {
	t := p.Transfer()
	if t.State != kernel.ManagedClosed {
		return nil
	}
	g, e := r.runtime.LookupChildExecution(ctx, t.SafePoint.Host.RunID)
	if errors.Is(e, kernel.ErrNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	if g != t.SuccessorGrant {
		return kernel.ErrExecutionConflict
	}
	s := p.Snapshot()
	proof := featurecontrol.Seal(s.Run.ID, s.Run.Revision, "agent_owned_physical_settled", g.ID, s.State, s.Checkpoint)
	if e = r.runtime.FinishChildExecution(ctx, g, proof); e != nil {
		return e
	}
	if e = r.runtime.ReleaseExecution(ctx, t.SuccessorLease); e != nil && !errors.Is(e, kernel.ErrExecutionConflict) {
		return e
	}
	return nil
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

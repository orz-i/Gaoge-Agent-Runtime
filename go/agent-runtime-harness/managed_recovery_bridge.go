package harness

import (
	"context"
	"errors"
	"math"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Managed recovery uses the native Store for election and retirement.
// Host storage only validates the original immutable selection.
func (r *Runner) bindManagedRoot(ctx context.Context, inv Invocation, phase, id string, approval *kernel.ManagedApprovalDecision) (context.Context, error) {
	host, ok := r.store.(kernel.ManagedRecoveryHostStore)
	if !ok || !r.runtime.UsesManagedRecoveryHost(host) {
		return ctx, kernel.ErrExecutionRequired
	}
	if inv.Attempt <= 0 || uint64(inv.Attempt) > math.MaxUint32 {
		return ctx, ErrConflict
	}
	turn, err := r.store.GetTurn(ctx, inv.TurnID)
	if err != nil {
		return ctx, err
	}
	session, err := r.store.GetSession(ctx, turn.SessionID)
	if err != nil {
		return ctx, err
	}
	config, err := r.store.GetConfigSnapshot(ctx, turn.ConfigSnapshotID)
	if err != nil {
		return ctx, err
	}
	ka, err := r.runtime.ManagedRecoveryAuthority(ctx)
	if err != nil {
		return ctx, err
	}
	ha, err := host.ManagedRecoveryAuthority(ctx)
	if err != nil {
		return ctx, err
	}
	b := kernel.ManagedHostBinding{KernelAuthority: ka, HostAuthority: ha, TurnID: turn.ID, InvocationID: inv.ID, Attempt: uint32(inv.Attempt), RunID: inv.ExecutionRefID, DispatchID: id, Phase: phase, Approval: approval}
	b.Source, err = nativeHostSource(session, turn, inv, config, b)
	if err != nil {
		return ctx, err
	}
	return r.runtime.BindManagedDispatch(ctx, b)
}

// Native root commands use the Host's already frozen selection, with no new
// Host dispatch grant. The correlation is consumed only by the native Grant
// and its approval journal. Managed recovery seals these same Host facts.
func (r *Runner) withNativeRootCommand(ctx context.Context, native *agent.Runner, inv Invocation, phase string, approval *kernel.ManagedApprovalDecision, physical func(context.Context) (kernel.Snapshot, error)) (kernel.Snapshot, error) {
	snap, _, err := r.observeNativeCommand(ctx, native, inv, phase, approval, physical)
	return snap, err
}

func (r *Runner) observeNativeCommand(ctx context.Context, native *agent.Runner, inv Invocation, phase string, approval *kernel.ManagedApprovalDecision, physical func(context.Context) (kernel.Snapshot, error)) (kernel.Snapshot, agent.PhysicalCallOutcome, error) {
	id, err := r.runtime.NewID("nativecmd")
	if err != nil {
		return kernel.Snapshot{}, agent.PhysicalCallOutcome{}, err
	}
	id = "nativecmd_" + kernel.ManagedDigest(struct{ Invocation, Run, Attempt string }{inv.ID, inv.ExecutionRefID, id})
	if native.SupportsManagedRecovery() && inv.Attempt == 1 && inv.ParentItemID == "" {
		ctx, err = r.bindManagedRoot(ctx, inv, phase, id, approval)
		if err != nil {
			return kernel.Snapshot{}, agent.PhysicalCallOutcome{}, err
		}
	}
	op := nativeDispatchOperation(phase)
	return native.ObservePhysicalDispatch(ctx, inv.ExecutionRefID, op, id, physical)
}

// Direct recovery is qualified by the native original Grant/retirement. A
// normally returned source is historical, not a second Host pending dispatch.
func (r *Runner) directManagedPending(ctx context.Context, native *agent.Runner, t kernel.ManagedTransfer) (bool, error) {
	if t.Resolved() {
		return t.Selection.Mode == kernel.ManagedCancel, nil
	}
	if t.Selection.Mode != "" {
		return true, nil
	}
	g := t.SafePoint.OriginalGrant
	// A retired original is historical even if the current Run has
	// advanced since its receipt or has another active grant.
	receipt, err := r.runtime.LoadOwnedRetirement(ctx, g.ID)
	if err == nil {
		if !receipt.Valid() || receipt.Evidence.Grant != g {
			return false, ErrConflict
		}
		return false, nil
	}
	if !errors.Is(err, kernel.ErrNotFound) {
		return false, err
	}
	active, err := r.runtime.LookupChildExecution(ctx, g.ChildRunID)
	if err != nil || active != g {
		return false, errors.Join(ErrConflict, err)
	}
	// A returned root with outstanding deferred children belongs to
	// the existing receipt-only cleanup path, not successor admission.
	if source, e := r.runtime.Load(ctx, g.ChildRunID); e == nil {
		if scope, e := native.RootDeferredCleanupEligibility(ctx, g.ChildRunID, source.Run.Revision); e == nil && !scope.RetirementOnly() {
			return false, nil
		}
	} else {
		return false, e
	}
	b := t.SafePoint.Host
	op := nativeDispatchOperation(b.Phase)
	kind, err := native.ReconcileOriginalPhysicalDispatch(ctx, b.RunID, op, b.DispatchID)
	return kind == agent.PhysicalCallUnknown, err
}

// The original frozen invocation selects the same native transfer for
// Resume and Cancel. An absent or already retired ordinary point is not a
// second Host election. All other mismatches fail closed.
func (r *Runner) pendingManagedInvocation(ctx context.Context, inv Invocation, resume bool) (*agent.Runner, kernel.ManagedTransfer, error) {
	native, ok := r.agent.(*agent.Runner)
	host, bound := r.store.(kernel.ManagedRecoveryHostStore)
	if !ok || !bound || !native.SupportsManagedRecovery() || inv.ParentItemID != "" ||
		!native.ObservesKernelRuntime(r.runtime) || !r.runtime.UsesManagedRecoveryHost(host) {
		return nil, kernel.ManagedTransfer{}, nil
	}
	if resume {
		if err := r.requireInvocationRootModelOutputCeiling(ctx, inv); err != nil {
			return nil, kernel.ManagedTransfer{}, err
		}
	}
	t, err := r.runtime.LoadManagedRecovery(ctx, inv.ExecutionRefID)
	if kernel.ManagedRecoveryAbsent(err) {
		return nil, t, nil
	}
	if err != nil {
		return nil, t, err
	}
	b := t.SafePoint.Host
	if b.InvocationID != inv.ID || b.TurnID != inv.TurnID || b.RunID != inv.ExecutionRefID ||
		uint64(b.Attempt) != uint64(inv.Attempt) || !t.Native() || !t.SafePoint.Valid() {
		return nil, t, ErrConflict
	}
	pending, err := r.directManagedPending(ctx, native, t)
	if err != nil || !pending {
		return nil, t, err
	}
	return native, t, nil
}

func (r *Runner) recoverManagedInvocation(ctx context.Context, inv Invocation) (kernel.Snapshot, bool, error) {
	native, transfer, err := r.pendingManagedInvocation(ctx, inv, true)
	if native == nil {
		return kernel.Snapshot{}, err != nil, err
	}
	// An already committed native closure needs projection only, never a
	// Host reservation lookup or a second dispatch admission.
	if transfer.Resolved() {
		proof, proofErr := r.runtime.ManagedRecoveryClosure(ctx, transfer.SafePoint.ID)
		if proofErr != nil || !proof.Matches(transfer.Selection) {
			return kernel.Snapshot{}, true, errors.Join(ErrConflict, proofErr)
		}
		snap := proof.Snapshot()
		return snap, true, r.verifyManagedCancellationScope(ctx, inv)
	}
	// The Agent binds the original Host source itself at its native entry
	// boundary. No separate Host recovery selection precedes that call.
	snap, proof, runErr := native.RecoverManagedContinuation(ctx, transfer)
	// A real returned successor may be waiting for its original child
	// receipt. Do not close the original Host dispatch until retirement.
	if !proof.Resolved() {
		return snap, true, runErr
	}
	// A known terminal failure is an original result, not an uncertain
	// callback. Close only with the same durable snapshot and retirement;
	// return nonterminal errors only after closing the known physical dispatch.
	if kernel.ManagedDigest(snap) != kernel.ManagedDigest(proof.Snapshot()) {
		return snap, true, errors.Join(ErrConflict, runErr)
	}
	if snap.Run.Status == kernel.RunStatusCancelled {
		if e := r.verifyManagedCancellationScope(ctx, inv); e != nil {
			return snap, true, errors.Join(runErr, e)
		}
	}
	if runErr != nil && !terminalRuntimeStatus(snap.Run.Status) {
		return snap, true, runErr
	}
	return snap, true, nil
}

// cancelManagedOriginal passes the authorized root cancellation to the same
// native transaction as recovery admission; it creates no Host election.
func (r *Runner) cancelManagedOriginal(ctx context.Context, turn Turn, inv Invocation, reason string) (Snapshot, bool, error) {
	canceller, ok := r.cancellation.(*kernel.Runtime)
	if !ok || canceller != r.runtime {
		return Snapshot{}, false, nil
	}
	native, t, err := r.pendingManagedInvocation(ctx, inv, false)
	if native == nil {
		return Snapshot{}, err != nil, err
	}
	if turn.ID != inv.TurnID {
		return Snapshot{}, true, ErrConflict
	}
	if t.SafePoint.LifecycleVersion != kernel.ManagedLifecycleAgentObligations || t.Selection.Mode == kernel.ManagedResume {
		return Snapshot{}, false, nil
	}
	if err = r.verifyManagedCancellationScope(ctx, inv); err != nil {
		return Snapshot{}, true, err
	}
	// No new Host cancellation fence or mode reservation. The already
	// authorized command competes in the original native execution transaction.
	snap, proof, err := native.CancelManagedContinuation(ctx, t, reason)
	if err != nil {
		return Snapshot{}, true, err
	}
	if !proof.Resolved() || proof.Transfer().Selection.Mode != kernel.ManagedCancel || snap.Run.Status != kernel.RunStatusCancelled || kernel.ManagedDigest(proof.Snapshot()) != kernel.ManagedDigest(snap) {
		return Snapshot{}, true, ErrConflict
	}
	if err = r.verifyManagedCancellationScope(ctx, inv); err != nil {
		return Snapshot{}, true, err
	}
	out, err := r.syncRuntimeSnapshotWithRetryAfterCancellation(ctx, turn, inv, snap)
	return out, true, err
}

// A root cancellation proves only the original native scope. Both direct
// cancellation and replay must establish that no unrelated Host or child
// effects need cleanup before claiming whole-turn cancellation.
func (r *Runner) verifyManagedCancellationScope(ctx context.Context, inv Invocation) error {
	turn, err := loadVerifiedTurn(ctx, r.store, inv.TurnID)
	if err != nil {
		return err
	}
	invocations, err := r.store.ListInvocations(ctx, turn.ID)
	if err != nil {
		return err
	}
	if len(invocations) != 1 || invocations[0].ID != inv.ID {
		return ErrRuntimeCancellationUnverified
	}
	children, err := r.relatedChildRunIDs(ctx, inv.ExecutionRefID)
	if err != nil {
		return err
	}
	delegated, err := r.delegatedRunIDs(ctx, turn.ID, nil)
	if err != nil {
		return err
	}
	if len(children) != 0 || len(delegated) != 0 {
		return ErrRuntimeCancellationUnverified
	}
	items, err := r.loadGlobalPhysicalEvidence(ctx, turn.ID)
	if err != nil {
		return err
	}
	if PendingExecutionDispatch(items, "") {
		return ErrConflict
	}
	if err = r.checkInteractionCancellation(ctx, turn, ""); err != nil {
		return err
	}
	// No write-capable waiting Interaction projection is owned by this
	// specialized parked root selection.
	interactions, err := r.store.ListInteractions(ctx, turn.ID)
	if err != nil {
		return err
	}
	for _, i := range interactions {
		if i.Status == InteractionWaiting {
			return ErrConflict
		}
	}
	return nil
}

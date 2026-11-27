package harness

import (
	"context"
	"errors"
	"math"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
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
	id, err := r.runtime.NewID("nativecmd")
	if err != nil {
		return kernel.Snapshot{}, err
	}
	id = "nativecmd_" + kernel.ManagedDigest(struct{ Invocation, Run, Attempt string }{inv.ID, inv.ExecutionRefID, id})
	if native.SupportsManagedRecovery() && inv.Attempt == 1 {
		ctx, err = r.bindManagedRoot(ctx, inv, phase, id, approval)
		if err != nil {
			return kernel.Snapshot{}, err
		}
	}
	op := plugin.RunResume
	switch phase {
	case "start", "start-retry":
		op = plugin.RunStart
	case "resolve-approval":
		op = plugin.RunResolveApproval
	}
	snap, _, err := native.ObservePhysicalDispatch(ctx, inv.ExecutionRefID, op, id, physical)
	return snap, err
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
	op := plugin.RunResume
	switch b.Phase {
	case "start":
		op = plugin.RunStart
	case "resolve-approval":
		op = plugin.RunResolveApproval
	}
	kind, err := native.ReconcileOriginalPhysicalDispatch(ctx, b.RunID, op, b.DispatchID)
	return kind == agent.PhysicalCallUnknown, err
}

// recoverManagedInvocation does not open a new physical dispatch. The Core
// entry checks the original source, reads Host authorization and commits native admission.
func (r *Runner) recoverManagedInvocation(ctx context.Context, inv Invocation) (kernel.Snapshot, bool, error) {
	native, ok := r.agent.(*agent.Runner)
	if !ok || !native.SupportsManagedRecovery() || inv.ParentItemID != "" {
		return kernel.Snapshot{}, false, nil
	}
	if err := r.requireInvocationRootModelOutputCeiling(ctx, inv); err != nil {
		return kernel.Snapshot{}, true, err
	}
	transfer, err := r.runtime.LoadManagedRecovery(ctx, inv.ExecutionRefID)
	if kernel.ManagedRecoveryAbsent(err) {
		return kernel.Snapshot{}, false, nil
	}
	if err != nil {
		return kernel.Snapshot{}, true, err
	}
	b := transfer.SafePoint.Host
	if b.InvocationID != inv.ID || b.TurnID != inv.TurnID || b.RunID != inv.ExecutionRefID || uint64(b.Attempt) != uint64(inv.Attempt) {
		return kernel.Snapshot{}, true, ErrConflict
	}
	if !b.DirectNativeCommand() {
		return kernel.Snapshot{}, true, ErrConflict
	}
	pending, e := r.directManagedPending(ctx, native, transfer)
	if e != nil || !pending {
		return kernel.Snapshot{}, e != nil, e
	}
	// An already committed native closure needs projection only, never a
	// Host reservation lookup or a second dispatch admission.
	if transfer.Resolved() {
		proof, proofErr := r.runtime.ManagedRecoveryClosure(ctx, transfer.SafePoint.ID)
		if proofErr != nil || !proof.Matches(transfer.Selection) {
			return kernel.Snapshot{}, true, errors.Join(ErrConflict, proofErr)
		}
		if transfer.Selection.Mode == kernel.ManagedCancel {
			if e := r.verifyManagedCancelledProjection(ctx, inv, b.DispatchID); e != nil {
				return kernel.Snapshot{}, true, e
			}
		}
		return proof.Snapshot(), true, nil
	}
	boundCtx, err := r.runtime.BindManagedDispatch(ctx, b)
	if err != nil {
		return kernel.Snapshot{}, true, err
	}
	snap, proof, runErr := native.RecoverManagedContinuation(boundCtx, inv.ExecutionRefID)
	// A real returned successor may be waiting for its original child
	// receipt. Do not close the original Host dispatch until retirement.
	if !proof.Matches(proof.Transfer().Selection) {
		return snap, true, runErr
	}
	// A known terminal failure is an original result, not an uncertain
	// callback. Close only with the same durable snapshot and retirement;
	// return nonterminal errors only after closing the known physical dispatch.
	if kernel.ManagedDigest(snap) != kernel.ManagedDigest(proof.Snapshot()) {
		return snap, true, errors.Join(ErrConflict, runErr)
	}
	if snap.Run.Status == kernel.RunStatusCancelled {
		if e := r.verifyManagedCancelledProjection(ctx, inv, b.DispatchID); e != nil {
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
	native, ok := r.agent.(*agent.Runner)
	canceller, nativeCancel := r.cancellation.(*kernel.Runtime)
	host, bound := r.store.(kernel.ManagedRecoveryHostStore)
	if !ok || !nativeCancel || canceller != r.runtime || !native.ObservesKernelRuntime(r.runtime) ||
		!bound || !r.runtime.UsesManagedRecoveryHost(host) ||
		!native.SupportsManagedRecovery() {
		return Snapshot{}, false, nil
	}
	t, err := r.runtime.LoadManagedRecovery(ctx, inv.ExecutionRefID)
	if kernel.ManagedRecoveryAbsent(err) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, true, err
	}
	b := t.SafePoint.Host
	if b.TurnID != turn.ID || b.InvocationID != inv.ID || b.RunID != inv.ExecutionRefID || uint64(b.Attempt) != uint64(inv.Attempt) {
		return Snapshot{}, true, ErrConflict
	}
	if !b.DirectNativeCommand() {
		return Snapshot{}, true, ErrConflict
	}
	pending, e := r.directManagedPending(ctx, native, t)
	if e != nil || !pending {
		return Snapshot{}, e != nil, e
	}
	if t.SafePoint.LifecycleVersion != kernel.ManagedLifecycleAgentObligations || t.Selection.Mode == kernel.ManagedResume {
		return Snapshot{}, false, nil
	}
	if err = r.verifyManagedCancellationScope(ctx, turn, inv, b.DispatchID); err != nil {
		return Snapshot{}, true, err
	}
	if !t.Resolved() {
		ctx, err = r.runtime.BindManagedDispatch(ctx, b)
		if err != nil {
			return Snapshot{}, true, err
		}
	}
	// No new Host cancellation fence or mode reservation. The already
	// authorized command competes in the original native execution transaction.
	snap, proof, err := native.CancelManagedContinuation(ctx, inv.ExecutionRefID, reason)
	if err != nil {
		return Snapshot{}, true, err
	}
	if !proof.Matches(proof.Transfer().Selection) || proof.Transfer().Selection.Mode != kernel.ManagedCancel || snap.Run.Status != kernel.RunStatusCancelled {
		return Snapshot{}, true, ErrConflict
	}
	if err = r.verifyManagedCancellationScope(ctx, turn, inv, b.DispatchID); err != nil {
		return Snapshot{}, true, err
	}
	out, err := r.syncRuntimeSnapshotWithRetryAfterCancellation(ctx, turn, inv, snap)
	return out, true, err
}

func (r *Runner) verifyManagedCancelledProjection(ctx context.Context, inv Invocation, dispatchID string) error {
	turn, err := loadVerifiedTurn(ctx, r.store, inv.TurnID)
	if err != nil {
		return err
	}
	return r.verifyManagedCancellationScope(ctx, turn, inv, dispatchID)
}

// A root cancellation proves only the original native scope. Both direct
// cancellation and replay must establish that no unrelated Host or child
// effects need cleanup before claiming whole-turn cancellation.
func (r *Runner) verifyManagedCancellationScope(ctx context.Context, turn Turn, inv Invocation, dispatchID string) error {
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
	delegated, err := r.delegatedRunIDs(ctx, turn.ID)
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
	receipts := executionDispatchReceiptIndex(items)
	for _, item := range items {
		if item.ParentItemID == ExecutionDispatchParentItemID && item.ID != dispatchID {
			if _, settled := VerifiedExecutionDispatchReceipt(item, receipts[item.ID]); !settled {
				return ErrConflict
			}
		}
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

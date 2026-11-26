package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// managedObligations is a proof of the already-selected native boundary, not
// a second ledger or a replacement input queue. The full source aggregate
// (including input messages and cursor) is sealed by the Kernel safe point.
type managedObligations = kernel.ManagedAgentObligations

// Quiescence is deliberately narrower than ordinary receipt recovery. Never
// transfer an active child or held/waiting reservation (even a known unsent
// hold). No quota is reserved at this pre-dispatch point. Prior settled and
// released reservations remain the original accounting authority.
func quiescentManagedLedger(l budget.Ledger, runID string) bool {
	binding, ok := l.Runs[runID]
	if !ok || binding.ParentRunID != "" || l.ID == "" || l.Revision == 0 || len(l.RootRetries) != 0 {
		return false
	}
	for id := range l.Runs {
		if id != runID && !l.FinishedRuns[id] {
			return false
		}
	}
	for _, res := range l.Reservations {
		if res.Status != budget.ReservationSettled && res.Status != budget.ReservationReleased {
			return false
		}
	}
	return true
}

// Allow only the original Root cancellation and terminal lifetime closure to
// change the ledger during CANCEL. All charges/receipts, limits, ancestry,
// prior child closures and other reservations remain exactly the frozen facts.
func managedLedgerDigest(l budget.Ledger, runID string) string {
	l = budget.CloneLedger(l)
	l.Revision = 0
	l.Cancelled = false
	delete(l.FinishedRuns, runID)
	return kernel.ManagedDigest(l)
}

func (r *Runner) combinedManaged(state runState) bool {
	return r.nativeBudget != nil || state.InputBinding != ""
}

func (r *Runner) freezeManagedObligations(ctx context.Context, s kernel.Snapshot, state runState, inv ModelInvocation, binding kernel.ManagedHostBinding) (json.RawMessage, bool, error) {
	if r.rootCallbacks == nil || r.rootCallbacks.unknown.Load() ||
		binding.Attempt != 1 || len(state.PendingCalls) != 0 || state.PendingApproval != nil ||
		state.ToolAdmission != nil || state.ToolDispatchID != "" || state.ToolWait != nil ||
		len(state.DeferredChildren) != 0 || len(state.RecoveredTools) != 0 ||
		len(state.ExternalSignals) != 0 || state.ExternalToolCancellation != nil || state.ExternalToolHold != nil {
		return nil, false, nil
	}
	if state.InputBinding != "" && (r.inputs == nil || r.inputBinding != state.InputBinding) {
		return nil, false, ErrInvalidRequest
	}
	phase := ""
	proof := managedObligations{Phase: string(inv.Status), Purpose: inv.Purpose, SourceRevision: s.Run.Revision + 1,
		InvocationID: inv.ID, RequestHash: inv.RequestHash, SamplingDigest: kernel.ManagedDigest(inv.SamplingPlan),
		InputBinding: state.InputBinding, InputCursor: state.InputCursor}
	if inv.Purpose == ModelInvocationContextSummary {
		phase = string(inv.Status)
		proof.BindingDigest = kernel.ManagedDigest(inv.ContextSummary)
	}
	var budgetOwner any
	if r.nativeBudget != nil {
		frame, _ := callbackscope.RunCall(ctx).(*nativeRunAdmission)
		if frame == nil || frame.invocation.RunID != s.Run.ID {
			return nil, false, ErrInvalidRequest
		}
		admission := frame.budget.Load()
		if admission == nil || admission.adapter != r.nativeBudget || admission.projectionErr != nil || admission.ledger.ID != binding.TurnID {
			return nil, false, ErrInvalidRequest
		}
		l, err := r.nativeBudget.observeRunBudget(ctx, admission.ledger, s.Run.ID)
		if err != nil {
			return nil, false, err
		}
		if l.RunCancelled(s.Run.ID) || l.FinishedRuns[s.Run.ID] || !quiescentManagedLedger(l, s.Run.ID) {
			return nil, false, nil
		}
		if inv.Status != ModelInvocationConsumed && (inv.SamplingPlan == nil || inv.SamplingPlan.LedgerID != l.ID) {
			return nil, false, ErrInvalidRequest
		}
		if !managedModelLedgerPhase(inv, l) {
			return nil, false, nil
		}
		proof.LedgerID, proof.LedgerRevision, proof.LedgerDigest = l.ID, l.Revision, managedLedgerDigest(l, s.Run.ID)
		budgetOwner = r.nativeBudget
	}
	journal, ok := callbackscope.ManagedObligationSuspension(ctx, r.nativeContext, budgetOwner, inv.Request, phase)
	if !ok {
		return nil, false, nil
	}
	proof.Journal = journal
	raw, err := json.Marshal(proof)
	return raw, err == nil, err
}

func managedModelLedgerPhase(inv ModelInvocation, l budget.Ledger) bool {
	res, found := l.Reservations["model:"+inv.ID]
	if inv.Status == ModelInvocationPending {
		// A frozen but not dispatched invocation must not acquire a
		// historical reservation from a different execution attempt.
		return !found && inv.DispatchAttempt == 0 && inv.ExecutionAttempt == 0
	}
	if !found || res.RunID != inv.RunID || !res.Dispatched || res.Status != budget.ReservationSettled {
		return false
	}
	if inv.SamplingPlan != nil {
		expected, err := inv.SamplingPlan.reservation(inv.Request)
		if err != nil || expected.RequestHash != res.RequestHash || expected.PhysicalRequestHash != res.PhysicalRequestHash || expected.Requested != res.Requested {
			return false
		}
	}
	response, err := budget.DecodeModelReceipt(res)
	if err != nil || inv.ErrorCode != "" {
		return false
	}
	return samePersistedModelResponse(inv.Response, response)
}

func (r *Runner) validateManagedObligations(point kernel.ManagedSafePoint) (managedObligations, error) {
	p, valid := point.AgentObligations()
	if !valid || !point.Valid() {
		return p, ErrInvalidRequest
	}
	state, err := decodeState(point.Source.State)
	if err != nil || state.InputBinding != p.InputBinding || state.InputCursor != p.InputCursor ||
		(p.InputBinding != "" && (r.inputs == nil || r.inputBinding != p.InputBinding)) ||
		(p.LedgerID != "") != (r.nativeBudget != nil) ||
		(p.LedgerID == "" && p.InputBinding == "") {
		return p, ErrInvalidRequest
	}
	if len(state.PendingCalls) != 0 || state.PendingApproval != nil || state.ToolAdmission != nil || state.ToolDispatchID != "" ||
		state.ToolWait != nil || len(state.DeferredChildren) != 0 || len(state.RecoveredTools) != 0 || len(state.ExternalSignals) != 0 ||
		state.ExternalToolCancellation != nil || state.ExternalToolHold != nil {
		return p, ErrInvalidRequest
	}
	var inv ModelInvocation
	if p.Purpose == ModelInvocationContextSummary {
		var ok bool
		inv, ok = summaryNativePhase(state)
		if !r.SupportsSummaryNativeRecovery() || !ok || p.BindingDigest != kernel.ManagedDigest(inv.ContextSummary) ||
			inv.ContextSummary.Cursor.Turn.ID != point.Host.TurnID || inv.ContextSummary.Root.InvocationID != point.Host.InvocationID ||
			uint64(inv.ContextSummary.Root.Attempt) != uint64(point.Host.Attempt) {
			return p, ErrInvalidRequest
		}
	} else {
		active, ok := activeModelInvocation(state)
		if p.Purpose != "" || p.BindingDigest != "" || !r.SupportsManagedContextRecovery() || !ok ||
			active.Purpose != "" || active.Status != ModelInvocationPending || active.DispatchAttempt != 0 || active.ExecutionAttempt != 0 ||
			!knownRootPhysicalSettlement(point.Source) {
			return p, ErrInvalidRequest
		}
		inv = *active
	}
	if inv.ID != p.InvocationID || inv.RunID != point.Host.RunID || inv.RequestHash != p.RequestHash || string(inv.Status) != p.Phase ||
		kernel.ManagedDigest(inv.SamplingPlan) != p.SamplingDigest ||
		(inv.Status != ModelInvocationConsumed && p.LedgerID != "" && (inv.SamplingPlan == nil || inv.SamplingPlan.LedgerID != p.LedgerID)) {
		return p, ErrInvalidRequest
	}
	return p, nil
}

// Read only the original Ledger, never Ensure/Register or a Host callback.
// The registered exact first-party Store is the original accounting owner.
func (r *Runner) checkManagedLedger(ctx context.Context, p managedObligations, runID string, allowCancel, allowFinished bool) (budget.Ledger, error) {
	if p.LedgerID == "" {
		return budget.Ledger{}, nil
	}
	l, err := r.nativeBudget.coordinator.Store.LoadBudget(ctx, p.LedgerID)
	if err != nil {
		return l, err
	}
	if l.ID != p.LedgerID || l.Revision < p.LedgerRevision || !quiescentManagedLedger(l, runID) ||
		managedLedgerDigest(l, runID) != p.LedgerDigest || (!allowCancel && l.RunCancelled(runID)) ||
		(!allowFinished && l.FinishedRuns[runID]) || (l.FinishedRuns[runID] && !l.RunCancelled(runID)) {
		return l, budget.ErrLedgerConflict
	}
	return l, nil
}

// CANCEL is the elected immutable Host selection. Fence the same ledger
// before the Kernel cancel, and close its lifetime only after the terminal
// Kernel fact. Both existing ledger CAS operations are idempotent.
func (r *Runner) cancelManagedBudget(ctx context.Context, p managedObligations, runID string, finished bool) error {
	if p.LedgerID == "" {
		return nil
	}
	l, err := r.checkManagedLedger(ctx, p, runID, true, finished)
	if err != nil {
		return err
	}
	l, err = r.nativeBudget.coordinator.CancelRootAtRevision(ctx, p.LedgerID, runID, l.Revision)
	if err != nil {
		return err
	}
	if finished {
		l, err = r.nativeBudget.coordinator.FinishRunsAtRevision(ctx, p.LedgerID, l.Revision, runID)
		if err != nil {
			return err
		}
	}
	// Recheck after the CAS, not merely the earlier read. A different
	// reservation cannot be silently released by this cancellation.
	if !l.RunCancelled(runID) || l.FinishedRuns[runID] != finished ||
		!quiescentManagedLedger(l, runID) || managedLedgerDigest(l, runID) != p.LedgerDigest {
		return budget.ErrLedgerConflict
	}
	return r.nativeBudget.ports.Project(ctx, l)
}

// ValidateManagedBudgetClosure is used by both concrete Harness Stores before
// first closure. An opaque Kernel closure alone does not settle shared quota.
func ValidateManagedBudgetClosure(point kernel.ManagedSafePoint, terminal kernel.Snapshot, mode kernel.ManagedRecoveryMode, l budget.Ledger) error {
	if point.LifecycleVersion != kernel.ManagedLifecycleAgentObligations {
		return nil
	}
	p, ok := point.AgentObligations()
	if !ok {
		return ErrInvalidRequest
	}
	if p.LedgerID == "" {
		return nil
	}
	if l.ID != point.Host.TurnID || l.ID != p.LedgerID || l.Revision < p.LedgerRevision {
		return budget.ErrLedgerConflict
	}
	runID := point.Host.RunID
	if mode == kernel.ManagedCancel {
		if terminal.Run.Status != kernel.RunStatusCancelled || !l.RunCancelled(runID) || !l.FinishedRuns[runID] ||
			!quiescentManagedLedger(l, runID) || managedLedgerDigest(l, runID) != p.LedgerDigest {
			return budget.ErrLedgerConflict
		}
	} else if terminal.Run.Status == kernel.RunStatusCompleted || terminal.Run.Status == kernel.RunStatusFailed || terminal.Run.Status == kernel.RunStatusCancelled {
		if !l.FinishedRuns[runID] {
			return budget.ErrLedgerConflict
		}
	}
	return nil
}

func (r *Runner) preflightCombinedManaged(ctx context.Context, t kernel.ManagedTransfer) (managedObligations, error) {
	p, err := r.validateManagedObligations(t.SafePoint)
	if err != nil {
		return p, err
	}
	if t.State == kernel.ManagedClosed || t.State == kernel.ManagedCancelled {
		return p, nil
	}
	current, err := r.runtime.Load(ctx, t.SafePoint.Host.RunID)
	if err != nil {
		return p, err
	}
	if !kernel.ManagedSourceMatches(t.SafePoint, current) {
		return p, errors.Join(ErrRootOutcomeUnknown, kernel.ErrExecutionConflict)
	}
	_, err = r.checkManagedLedger(ctx, p, t.SafePoint.Host.RunID, true, false)
	return p, err
}

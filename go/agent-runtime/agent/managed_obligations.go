package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
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

// freezeManagedSource binds the common native frame, exact input cursor
// and original quiescent ledger. The caller selects only its feature phase.
func (r *Runner) freezeManagedSource(ctx context.Context, s kernel.Snapshot, state runState, binding kernel.ManagedHostBinding) (managedObligations, budget.Ledger, bool, error) {
	var p managedObligations
	var l budget.Ledger
	if r.rootCallbacks == nil || r.rootCallbacks.unknown.Load() || binding.Attempt != 1 || !managedObligationsQuiescent(state) {
		return p, l, false, nil
	}
	if state.InputBinding != "" && (r.inputs == nil || r.inputBinding != state.InputBinding) {
		return p, l, false, ErrInvalidRequest
	}
	p.SourceRevision, p.InputBinding, p.InputCursor = s.Run.Revision+1, state.InputBinding, state.InputCursor
	if r.nativeBudget != nil {
		frame, _ := callbackscope.RunCall(ctx).(*nativeRunAdmission)
		if frame == nil || frame.invocation.RunID != s.Run.ID {
			return p, l, false, ErrInvalidRequest
		}
		admission := frame.budget.Load()
		if admission == nil || admission.adapter != r.nativeBudget || admission.projectionErr != nil || admission.ledger.ID != binding.TurnID {
			return p, l, false, ErrInvalidRequest
		}
		var err error
		l, err = r.nativeBudget.observeRunBudget(ctx, admission.ledger, s.Run.ID)
		if err != nil {
			return p, l, false, err
		}
		if l.RunCancelled(s.Run.ID) || l.FinishedRuns[s.Run.ID] || !quiescentManagedLedger(l, s.Run.ID) {
			return p, l, false, nil
		}
		p.LedgerID, p.LedgerRevision, p.LedgerDigest = l.ID, l.Revision, managedLedgerDigest(l, s.Run.ID)
	}
	return p, l, true, nil
}

func managedObligationsQuiescent(state runState) bool {
	return len(state.PendingCalls) == 0 && state.PendingApproval == nil && state.ToolAdmission == nil &&
		state.ToolDispatchID == "" && state.ToolWait == nil && len(state.DeferredChildren) == 0 &&
		len(state.RecoveredTools) == 0 && len(state.ExternalSignals) == 0 &&
		state.ExternalToolCancellation == nil && state.ExternalToolHold == nil
}

func (r *Runner) managedBudgetOwner() any {
	if r.nativeBudget != nil {
		return r.nativeBudget
	}
	return nil
}

func (r *Runner) freezeManagedObligations(ctx context.Context, s kernel.Snapshot, state runState, inv ModelInvocation, binding kernel.ManagedHostBinding) (json.RawMessage, bool, error) {
	p, l, eligible, err := r.freezeManagedSource(ctx, s, state, binding)
	if err != nil || !eligible {
		return nil, false, err
	}
	p.Phase, p.Purpose, p.InvocationID, p.RequestHash = string(inv.Status), inv.Purpose, inv.ID, inv.RequestHash
	p.SamplingDigest = kernel.ManagedDigest(inv.SamplingPlan)
	phase := ""
	if inv.Purpose == ModelInvocationContextSummary {
		phase = string(inv.Status)
		p.BindingDigest = kernel.ManagedDigest(inv.ContextSummary)
	}
	if r.nativeBudget != nil {
		if inv.Status != ModelInvocationConsumed && (inv.SamplingPlan == nil || inv.SamplingPlan.LedgerID != l.ID) {
			return nil, false, ErrInvalidRequest
		}
		if !managedModelLedgerPhase(inv, l) {
			return nil, false, nil
		}
	}
	journal, ok := callbackscope.ManagedObligationSuspension(ctx, r.nativeContext, r.managedBudgetOwner(), inv.Request, phase)
	if !ok {
		return nil, false, nil
	}
	p.Journal = journal
	raw, err := json.Marshal(p)
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
	return matchesSettledModelReceipt(inv, res) == nil
}

func (r *Runner) validateManagedObligations(point kernel.ManagedSafePoint) (managedObligations, error) {
	p, valid := point.AgentObligations()
	if !valid || !point.Valid() {
		return p, ErrInvalidRequest
	}
	state, err := decodeState(point.Source.State)
	if err != nil || state.SummaryNativeRecovery != r.SupportsSummaryNativeRecovery() ||
		state.InputBinding != p.InputBinding || state.InputCursor != p.InputCursor ||
		(p.InputBinding != "" && (r.inputs == nil || r.inputBinding != p.InputBinding)) ||
		(p.LedgerID != "") != (r.nativeBudget != nil) ||
		(p.LedgerID == "" && p.InputBinding == "") {
		return p, ErrInvalidRequest
	}
	if !managedObligationsQuiescent(state) {
		return p, ErrInvalidRequest
	}
	if p.Phase == "prepare" {
		if !r.supportsManagedPreparation() || !managedPreparationState(point.Source, state) {
			return p, ErrInvalidRequest
		}
		return p, nil
	}
	if len(state.ModelInvocations) == 0 {
		return p, ErrInvalidRequest
	}
	inv := state.ModelInvocations[len(state.ModelInvocations)-1]
	if p.Purpose == ModelInvocationContextSummary {
		shape := state
		if inv.Status == ModelInvocationPending && p.RequalifiedFrom != "" {
			shape.ModelInvocations = cloneModelInvocations(state.ModelInvocations)
			shape.ModelInvocations[len(shape.ModelInvocations)-1].DispatchAttempt = 0
			shape.ModelInvocations[len(shape.ModelInvocations)-1].ExecutionAttempt = 0
		}
		_, ok := summaryNativePhase(shape)
		if !r.SupportsSummaryNativeRecovery() || !ok || p.BindingDigest != kernel.ManagedDigest(inv.ContextSummary) ||
			inv.ContextSummary.Cursor.Turn.ID != point.Host.TurnID || inv.ContextSummary.Root.InvocationID != point.Host.InvocationID ||
			uint64(inv.ContextSummary.Root.Attempt) != uint64(point.Host.Attempt) {
			return p, ErrInvalidRequest
		}
	} else if p.Purpose != "" || p.BindingDigest != "" || (!r.SupportsManagedContextRecovery() && !r.SupportsSummaryNativeRecovery()) ||
		inv.Purpose != "" || (inv.Status == ModelInvocationConsumed && (state.InputBinding == "" || state.InputCursor == 0)) {
		return p, ErrInvalidRequest
	}
	if inv.Status == ModelInvocationPending {
		if (inv.DispatchAttempt != 0) != (p.SettledReceiptDigest != "") || (inv.ExecutionAttempt != 0 && p.RequalifiedFrom == "") {
			return p, ErrInvalidRequest
		}
	} else if !validModelInvocationReceipt(inv) {
		return p, ErrInvalidRequest
	}
	if p.SettledReceiptDigest == "" && !knownRootStatePhysicalSettlement(state) {
		return p, ErrInvalidRequest
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

// qualifyManagedFrontier accepts only the same original frozen Model
// Invocation and unchanged remainder of the aggregate. An original first
// party source may have advanced its claim or exact settled model receipt,
// but never another Tool, input cursor, policy or external effect. The
// actual Store PARK rechecks both the old transfer and new source under lock.
func (r *Runner) qualifyManagedFrontier(ctx context.Context, t kernel.ManagedTransfer) (kernel.ManagedSafePoint, *featurecontrol.Proof, error) {
	var empty kernel.ManagedSafePoint
	p, err := r.validateManagedObligations(t.SafePoint)
	if err != nil {
		return empty, nil, err
	}
	if t.State != kernel.ManagedInvalidated || t.Selection.ID != "" || len(t.Activations) != 0 || p.Phase != "pending" ||
		p.RequalifiedFrom != "" || p.SettledReceiptDigest != "" {
		return empty, nil, ErrRootOutcomeUnknown
	}
	original := t.SafePoint.Source
	current, err := r.runtime.Load(ctx, original.Run.ID)
	if err != nil {
		return empty, nil, err
	}
	if current.Run.Revision < original.Run.Revision || current.EventHead < original.EventHead || current.Run.Status != kernel.RunStatusRunning ||
		!sameBoundedValue(current.Checkpoint, original.Checkpoint) || !sameBoundedValue(current.Result, original.Result) {
		return empty, nil, ErrRootOutcomeUnknown
	}
	expectedRun := original.Run
	expectedRun.Revision, expectedRun.UpdatedAt = current.Run.Revision, current.Run.UpdatedAt
	// Diagnostic projection is not dispatch evidence. The entire remaining
	// immutable Run identity and native aggregate must still be exact.
	expectedRun.ErrorCode, expectedRun.ErrorDetail = current.Run.ErrorCode, current.Run.ErrorDetail
	if !sameBoundedValue(expectedRun, current.Run) {
		return empty, nil, ErrRootOutcomeUnknown
	}
	old, err := decodeState(original.State)
	if err != nil {
		return empty, nil, err
	}
	state, err := decodeState(current.State)
	if err != nil {
		return empty, nil, err
	}
	first, ok := activeModelInvocation(old)
	last, found := activeModelInvocation(state)
	if !ok || !found || first.Status != ModelInvocationPending || first.ExecutionAttempt != 0 || first.DispatchAttempt != 0 ||
		len(state.ModelInvocations) != len(old.ModelInvocations) || last.ID != first.ID ||
		(last.Status != ModelInvocationPending && last.Status != ModelInvocationCompleted) ||
		last.ExecutionAttempt > 1 || last.DispatchAttempt > last.ExecutionAttempt {
		return empty, nil, ErrRootOutcomeUnknown
	}
	// Only the common claim/dispatch/receipt fields can advance. Check the
	// full frozen request and physical SamplingPlan via the original value.
	normalized := cloneModelInvocation(*last)
	normalized.Status = first.Status
	normalized.ExecutionAttempt, normalized.DispatchAttempt = first.ExecutionAttempt, first.DispatchAttempt
	normalized.ExecutionLeaseUntil, normalized.RetryAfter = first.ExecutionLeaseUntil, first.RetryAfter
	normalized.ProviderResponseID, normalized.Response, normalized.Usage = first.ProviderResponseID, first.Response, first.Usage
	normalized.CompletedAt, normalized.ErrorCode = first.CompletedAt, first.ErrorCode
	normalized.OutcomeErrorCode, normalized.OutcomeErrorDetail = first.OutcomeErrorCode, first.OutcomeErrorDetail
	if !sameBoundedValue(normalized, *first) {
		return empty, nil, ErrRootOutcomeUnknown
	}
	normalizedState := state
	normalizedState.ModelInvocations = cloneModelInvocations(state.ModelInvocations)
	normalizedState.ModelInvocations[len(normalizedState.ModelInvocations)-1] = *first
	if !sameBoundedValue(normalizedState, old) {
		return empty, nil, ErrRootOutcomeUnknown
	}
	p.SourceRevision = current.Run.Revision
	p.Phase = string(last.Status)
	p.RequalifiedFrom = t.SafePoint.ID
	if p.LedgerID != "" {
		l, e := r.nativeBudget.coordinator.Store.LoadBudget(ctx, p.LedgerID)
		if e != nil {
			return empty, nil, e
		}
		if l.ID != p.LedgerID || l.Revision < p.LedgerRevision || !quiescentManagedLedger(l, current.Run.ID) || l.FinishedRuns[current.Run.ID] || l.RunCancelled(current.Run.ID) {
			return empty, nil, ErrRootOutcomeUnknown
		}
		res, exists := l.Reservations["model:"+last.ID]
		without := budget.CloneLedger(l)
		delete(without.Reservations, "model:"+last.ID)
		if managedLedgerDigest(without, current.Run.ID) != p.LedgerDigest {
			return empty, nil, budget.ErrLedgerConflict
		}
		if last.DispatchAttempt == 0 {
			if exists || last.Status != ModelInvocationPending {
				return empty, nil, ErrRootOutcomeUnknown
			}
		} else {
			if !exists || last.SamplingPlan == nil || last.SamplingPlan.LedgerID != l.ID ||
				res.Status != budget.ReservationSettled || !res.Dispatched || res.Active {
				return empty, nil, ErrModelOutcomeUnknown
			}
			expected, e := last.SamplingPlan.reservation(last.Request)
			if e != nil || res.ID != expected.ID || res.RunID != expected.RunID || res.RequestHash != expected.RequestHash ||
				res.PhysicalRequestHash != expected.PhysicalRequestHash || res.Requested != expected.Requested {
				return empty, nil, ErrModelOutcomeUnknown
			}
			response, receiptErr := budget.DecodeModelReceipt(res)
			if receiptErr != nil && !model.IsResponseRejectedError(receiptErr) {
				return empty, nil, errors.Join(ErrModelOutcomeUnknown, receiptErr)
			}
			if receiptErr == nil && !model.ValidResponse(response) {
				return empty, nil, ErrInvalidModelResponse
			}
			if last.Status == ModelInvocationCompleted {
				if !managedModelLedgerPhase(*last, l) {
					return empty, nil, ErrModelOutcomeUnknown
				}
			} else {
				p.SettledReceiptDigest = kernel.ManagedDigest(res.Receipt)
			}
		}
		p.LedgerRevision, p.LedgerDigest = l.Revision, managedLedgerDigest(l, current.Run.ID)
	} else if last.DispatchAttempt != 0 || last.Status != ModelInvocationPending {
		// No accounting authority exists to prove a returned external result.
		return empty, nil, ErrModelOutcomeUnknown
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return empty, nil, err
	}
	point := t.SafePoint
	point.Source = current
	point.LifecycleSource = raw
	point.ID = "managed-qualified-" + kernel.ManagedDigest(struct {
		Original    string
		Source      kernel.Snapshot
		Obligations managedObligations
	}{t.SafePoint.ID, current, p})
	point.RecoveryID = kernel.ManagedRecoveryID(point.ID, point.Host)
	if _, err = r.validateManagedObligations(point); err != nil {
		return empty, nil, err
	}
	proof := featurecontrol.Seal(current.Run.ID, current.Run.Revision, "managed_qualified_park", t.SafePoint.ID, current.State, point)
	return point, proof, nil
}

// parkManagedFrontier has one original Store election, whether the source was
// committed directly or requalified from exact native evidence after Leave.
func (r *Runner) parkManagedFrontier(ctx context.Context, t kernel.ManagedTransfer, cancel bool) (kernel.ManagedParkedProof, managedObligations, error) {
	var parked kernel.ManagedParkedProof
	if t.SafePoint.LifecycleVersion != kernel.ManagedLifecycleAgentObligations {
		parked, err := r.runtime.ParkManagedContinuation(ctx, t.SafePoint.Host.RunID)
		return parked, managedObligations{}, err
	}
	var p managedObligations
	var err error
	if t.State == kernel.ManagedInvalidated && t.Selection.ID == "" {
		point, proof, e := r.qualifyManagedFrontier(ctx, t)
		if e != nil {
			return parked, p, e
		}
		p, _ = point.AgentObligations()
		if cancel && p.SettledReceiptDigest != "" {
			return parked, p, ErrModelOutcomeUnknown
		}
		// A previously selected immutable Host reservation can never be
		// rotated to a new point by this requalification.
		if _, e = r.runtime.ReadManagedReservation(ctx, t.SafePoint.Host, ""); !errors.Is(e, kernel.ErrNotFound) {
			return parked, p, errors.Join(kernel.ErrExecutionConflict, e)
		}
		parked, err = r.runtime.ParkQualifiedManagedContinuation(ctx, t, point, proof)
	} else {
		p, err = r.preflightCombinedManaged(ctx, t)
		if err != nil {
			return parked, p, err
		}
		if cancel && p.SettledReceiptDigest != "" {
			return parked, p, ErrModelOutcomeUnknown
		}
		parked, err = r.runtime.ParkManagedContinuation(ctx, t.SafePoint.Host.RunID)
	}
	return parked, p, err
}

// recoverManagedModelFrontier runs only after the actual successor entered the
// common native Run stage. It never dispatches a settled original receipt.
func (r *Runner) recoverManagedModelFrontier(ctx context.Context, s kernel.Snapshot, p managedObligations) (kernel.Snapshot, error) {
	if p.RequalifiedFrom == "" {
		return s, nil
	}
	state, err := decodeState(s.State)
	if err != nil {
		return s, err
	}
	active, ok := activeModelInvocation(state)
	if !ok || active.ID != p.InvocationID {
		return s, ErrInvalidRequest
	}
	inv := *active
	if inv.Status == ModelInvocationCompleted {
		return s, r.requireSettledModelQualification(ctx, inv)
	}
	if inv.Status != ModelInvocationPending {
		return s, ErrInvalidRequest
	}
	event := "agent.model_invocation.retryable"
	if p.SettledReceiptDigest != "" {
		res, e := r.originalModelReservation(ctx, inv)
		if e != nil || res.Status != budget.ReservationSettled || kernel.ManagedDigest(res.Receipt) != p.SettledReceiptDigest {
			return s, errors.Join(ErrModelOutcomeUnknown, e)
		}
		if !matchesFrozenProviderBinding(r.model, inv) {
			return s, ErrProviderBindingMismatch
		}
		response, receiptErr := budget.DecodeModelReceipt(res)
		inv, err = r.qualifiedRecoveredModelReceipt(state, inv, response, receiptErr, true)
		if err != nil {
			return s, err
		}
		event = "agent.model_invocation.completed"
	} else {
		if inv.DispatchAttempt != 0 {
			return s, ErrModelOutcomeUnknown
		}
		if inv.ExecutionAttempt == 0 {
			return s, nil
		}
		// Kernel just fenced the old execution epoch. Retire only its
		// proven-unentered local claim, retaining the same frozen intent.
		if inv.ExecutionLeaseUntil == nil {
			return s, nil
		}
		inv.ExecutionLeaseUntil = nil
		now := r.clock.Now().UTC()
		inv.RetryAfter = &now
	}
	if !replaceLastModelInvocation(&state, inv) {
		return s, ErrInvalidRequest
	}
	encoded, err := encodeState(state)
	if err != nil {
		return s, err
	}
	return r.commitNativeTransition(ctx, s, kernel.Mutation{Status: kernel.RunStatusRunning, State: encoded, Checkpoint: s.Checkpoint,
		Events: []kernel.EventDraft{{Type: event, Message: inv.ID, Wakeup: true}}})
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
	l, err := r.checkManagedLedger(ctx, p, t.SafePoint.Host.RunID, true, false)
	if err != nil {
		return p, err
	}
	if p.Phase == "prepare" {
		state, e := decodeState(current.State)
		if e != nil {
			return p, e
		}
		if _, err = r.readManagedPreparationContext(ctx, t.SafePoint.Host, p.PreparationContext, state); err != nil {
			return p, err
		}
		if p.LedgerID != "" && !managedPreparationLedger(state, l) {
			return p, budget.ErrLedgerConflict
		}
	}
	return p, nil
}

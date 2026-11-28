package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// managedObligations is a proof of the already-selected native boundary, not
// a second ledger or a replacement input queue. The full source aggregate
// (including input messages and cursor) is sealed by the Kernel safe point.
type managedObligations = kernel.ManagedAgentObligations

// freezeManagedSource binds the common native frame, exact input cursor
// and original quiescent ledger. The caller selects only its feature phase.
func (r *Runner) freezeManagedSource(ctx context.Context, s kernel.Snapshot, state runState, binding kernel.ManagedHostBinding, mutation *budget.LedgerMutation) (managedObligations, budget.Ledger, bool, error) {
	var p managedObligations
	var l budget.Ledger
	if r.rootCallbacks == nil || r.rootCallbacks.unknown.Load() || !managedObligationsQuiescent(state) {
		return p, l, false, nil
	}
	if state.InputBinding != "" && (r.inputs == nil || r.inputBinding != state.InputBinding) {
		return p, l, false, ErrInvalidRequest
	}
	p.SourceRevision, p.InputBinding, p.InputCursor = s.Run.Revision+1, state.InputBinding, state.InputCursor
	if r.boundedBudget != nil {
		admission := r.nativeRunBudget(ctx, s.Run.ID)
		if admission == nil || admission.projectionErr != nil || admission.ledger.ID != binding.TurnID {
			return p, l, false, ErrInvalidRequest
		}
		var err error
		l, err = r.boundedBudget.observeRunBudget(ctx, admission.ledger, s.Run.ID)
		if err != nil {
			return p, l, false, err
		}
		if mutation != nil {
			if err := mutation.Validate(l, s.Run.ID); err != nil {
				return p, l, false, err
			}
			l = mutation.Next()
		}
		if l.RunCancelled(s.Run.ID) || l.FinishedRuns[s.Run.ID] || !kernel.QuiescentManagedLedger(l, binding) {
			return p, l, false, nil
		}
		p.LedgerID, p.LedgerRevision, p.LedgerDigest = l.ID, l.Revision, kernel.ManagedLedgerDigest(l, s.Run.ID)
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
	if r.boundedBudget != nil {
		return r.boundedBudget
	}
	return nil
}

func (r *Runner) freezeManagedObligations(ctx context.Context, s kernel.Snapshot, state runState, inv ModelInvocation, binding kernel.ManagedHostBinding, mutation *budget.LedgerMutation) (json.RawMessage, bool, error) {
	p, l, eligible, err := r.freezeManagedSource(ctx, s, state, binding, mutation)
	if err != nil || !eligible {
		return nil, false, err
	}
	p.Phase, p.Purpose, p.InvocationID, p.RequestHash = string(inv.Status), inv.Purpose, inv.ID, inv.RequestHash
	p.SamplingDigest = kernel.ManagedDigest(inv.SamplingPlan)
	phase := ""
	if inv.Purpose == ModelInvocationContextSummary {
		phase = string(inv.Status)
		p.BindingDigest = kernel.ManagedDigest(inv.ContextSummary)
		if !callbackscope.Operation(ctx, r.boundedContext, "context.summary.native-phase:"+phase, inv) {
			return nil, false, ErrInvalidRequest
		}
	}
	if r.boundedBudget != nil {
		if inv.Status != ModelInvocationConsumed && (inv.SamplingPlan == nil || inv.SamplingPlan.LedgerID != l.ID) {
			return nil, false, ErrInvalidRequest
		}
		if !managedModelLedgerPhase(inv, l) {
			return nil, false, nil
		}
	}
	journal, ok := callbackscope.ManagedObligationSuspension(ctx, r.boundedContext, r.managedBudgetOwner(), inv.Request, phase)
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
	if err != nil || !state.ManagedRecovery || !r.SupportsManagedRecovery() ||
		state.InputBinding != p.InputBinding || state.InputCursor != p.InputCursor ||
		(p.InputBinding != "" && (r.inputs == nil || r.inputBinding != p.InputBinding)) ||
		(p.LedgerID != "") != (r.boundedBudget != nil) {
		return p, ErrInvalidRequest
	}
	if !managedObligationsQuiescent(state) {
		return p, ErrInvalidRequest
	}
	if p.Phase == "prepare" {
		if !managedPreparationState(point.Source.Run.Status, state) {
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
		if !validSummaryNativePhase(shape) || p.BindingDigest != kernel.ManagedDigest(inv.ContextSummary) ||
			inv.ContextSummary.Cursor.Turn.ID != point.Host.TurnID || inv.ContextSummary.Root.InvocationID != point.Host.InvocationID ||
			uint64(inv.ContextSummary.Root.Attempt) != uint64(point.Host.Attempt) {
			return p, ErrInvalidRequest
		}
	} else if p.Purpose != "" || p.BindingDigest != "" ||
		inv.Purpose != "" || (inv.Status == ModelInvocationConsumed && (state.InputBinding == "" || state.InputCursor == 0)) {
		return p, ErrInvalidRequest
	}
	if inv.Status == ModelInvocationPending {
		if inv.DispatchAttempt != 0 || (inv.ExecutionAttempt != 0 && p.RequalifiedFrom == "") {
			return p, ErrInvalidRequest
		}
	} else if !validModelInvocationReceipt(inv) {
		return p, ErrInvalidRequest
	}
	if !knownRootStatePhysicalSettlement(state) {
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
func (r *Runner) checkManagedLedger(ctx context.Context, p managedObligations, host kernel.ManagedHostBinding) (budget.Ledger, error) {
	if p.LedgerID == "" {
		return budget.Ledger{}, nil
	}
	l, err := r.boundedBudget.coordinator.Store.LoadBudget(ctx, p.LedgerID)
	if err != nil {
		return l, err
	}
	if !p.MatchesLiveLedger(l, host) {
		return l, budget.ErrLedgerConflict
	}
	return l, nil
}

// qualifyManagedFrontier accepts only the same original frozen Model
// Invocation and unchanged remainder of the aggregate. An original first
// party source may have advanced its claim or exact settled model receipt,
// but never another Tool, input cursor, policy or external effect. The
// actual native admission rechecks both the old transfer and new source under lock.
func (r *Runner) qualifyManagedFrontier(ctx context.Context, t kernel.ManagedTransfer) (kernel.ManagedSafePoint, *featurecontrol.Proof, error) {
	var empty kernel.ManagedSafePoint
	p, err := r.validateManagedObligations(t.SafePoint)
	if err != nil {
		return empty, nil, err
	}
	if !t.Valid() || t.Terminal != nil || t.Selection.Mode != "" || t.SuccessorGrant.ID != "" || p.Phase != "pending" ||
		p.RequalifiedFrom != "" {
		return empty, nil, ErrRootOutcomeUnknown
	}
	original := t.SafePoint.Source
	current, err := r.runtime.Load(ctx, original.Run.ID)
	if err != nil {
		return empty, nil, err
	}
	if current.Run.Revision <= original.Run.Revision || current.EventHead < original.EventHead || current.Run.Status != kernel.RunStatusRunning ||
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
	normalized.ProviderResponseID, normalized.Response = first.ProviderResponseID, first.Response
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
		l, e := r.boundedBudget.coordinator.Store.LoadBudget(ctx, p.LedgerID)
		if e != nil {
			return empty, nil, e
		}
		if l.ID != p.LedgerID || l.Revision < p.LedgerRevision || !kernel.QuiescentManagedLedger(l, t.SafePoint.Host) || l.FinishedRuns[current.Run.ID] || l.RunCancelled(current.Run.ID) {
			return empty, nil, ErrRootOutcomeUnknown
		}
		_, exists := l.Reservations["model:"+last.ID]
		without := budget.CloneLedger(l)
		delete(without.Reservations, "model:"+last.ID)
		if kernel.ManagedLedgerDigest(without, current.Run.ID) != p.LedgerDigest {
			return empty, nil, budget.ErrLedgerConflict
		}
		if last.DispatchAttempt != 0 && last.Status != ModelInvocationCompleted {
			return empty, nil, ErrModelOutcomeUnknown
		}
		if last.DispatchAttempt == 0 {
			if exists || last.Status != ModelInvocationPending {
				return empty, nil, ErrRootOutcomeUnknown
			}
		} else if !managedModelLedgerPhase(*last, l) {
			return empty, nil, ErrModelOutcomeUnknown
		}
		p.LedgerRevision, p.LedgerDigest = l.Revision, kernel.ManagedLedgerDigest(l, current.Run.ID)
	} else if last.DispatchAttempt != 0 || last.Status != ModelInvocationPending {
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
	proof := featurecontrol.Seal(current.Run.ID, current.Run.Revision, "managed_qualified_recovery", t.SafePoint.ID, current.State, point)
	return point, proof, nil
}

// Authorize the exact original (or requalified) frontier; no Store preflight
// can elect an execution owner.
func (r *Runner) authorizeManagedFrontier(ctx context.Context, t kernel.ManagedTransfer, cancelReason ...string) (kernel.ManagedAdmissionProof, managedObligations, error) {
	var empty kernel.ManagedAdmissionProof
	var p managedObligations
	current, err := r.runtime.Load(ctx, t.SafePoint.Host.RunID)
	if err != nil {
		return empty, p, err
	}
	if t.Selection.Mode == "" && !kernel.ManagedSourceMatches(t.SafePoint, current) {
		point, proof, err := r.qualifyManagedFrontier(ctx, t)
		if err != nil {
			return empty, p, err
		}
		p, _ = point.AgentObligations()
		selected, err := r.runtime.AuthorizeQualifiedManagedContinuation(ctx, t, point, proof, cancelReason...)
		return selected, p, err
	}
	p, err = r.preflightManagedPoint(ctx, t.SafePoint, current)
	if err != nil {
		return empty, p, err
	}
	selected, err := r.runtime.AuthorizeManagedContinuation(ctx, t, cancelReason...)
	return selected, p, err
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
	if inv.DispatchAttempt != 0 {
		return s, ErrModelOutcomeUnknown
	}
	if inv.ExecutionAttempt == 0 || inv.ExecutionLeaseUntil == nil {
		return s, nil
	}
	// The expired epoch is fenced; only the undispatched claim is retired.
	inv.ExecutionLeaseUntil = nil
	now := r.clock.Now().UTC()
	inv.RetryAfter = &now
	if !replaceLastModelInvocation(&state, inv) {
		return s, ErrInvalidRequest
	}
	encoded, err := encodeState(state)
	if err != nil {
		return s, err
	}
	return r.commitNativeTransition(ctx, s, kernel.Mutation{Status: kernel.RunStatusRunning, State: encoded, Checkpoint: s.Checkpoint,
		Events: []kernel.EventDraft{{Type: "agent.model_invocation.retryable", Message: inv.ID, Wakeup: true}}})
}

func (r *Runner) preflightManagedPoint(ctx context.Context, point kernel.ManagedSafePoint, current kernel.Snapshot) (managedObligations, error) {
	p, err := r.validateManagedObligations(point)
	if err != nil {
		return p, err
	}
	if !kernel.ManagedSourceMatches(point, current) {
		return p, errors.Join(ErrRootOutcomeUnknown, kernel.ErrExecutionConflict)
	}
	l, err := r.checkManagedLedger(ctx, p, point.Host)
	if err != nil {
		return p, err
	}
	if p.Phase == "prepare" {
		state, e := decodeState(current.State)
		if e != nil {
			return p, e
		}
		if _, err = r.readManagedPreparationContext(ctx, point.Host, p.PreparationContext, state); err != nil {
			return p, err
		}
		if p.LedgerID != "" && !managedPreparationLedger(state, l) {
			return p, budget.ErrLedgerConflict
		}
	}
	return p, nil
}

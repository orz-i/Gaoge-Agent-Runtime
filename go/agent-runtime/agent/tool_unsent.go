package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// unsentToolDisposition is a committed native admission result, not a Tool
// receipt. The same original state drives both ledger closure and consumption.
type unsentToolDisposition struct {
	Release     bool   `json:"release,omitempty"`
	Reservation bool   `json:"reservation,omitempty"`
	Code        string `json:"code,omitempty"`
}

func (u *unsentToolDisposition) valid(a *toolAdmission) bool {
	if u == nil || (u.Reservation && (!a.BudgetBound || a.LedgerID == "")) {
		return false
	}
	if !u.Release {
		return u.Code == ""
	}
	if !a.BudgetBound || a.LedgerID == "" {
		return false
	}
	switch u.Code {
	case "agent.shared_budget_exhausted", "agent.shared_llm_calls_budget", "agent.shared_tool_calls_budget", "agent.shared_hosted_tool_calls_budget",
		"agent.shared_input_tokens_budget", "agent.shared_output_tokens_budget", "agent.shared_total_tokens_budget", "agent.shared_output_bytes_budget",
		"agent.shared_state_bytes_budget", "agent.shared_child_runs_budget", "agent.shared_cost_units_budget":
		return true
	default:
		return false
	}
}

func originalToolReservation(q tools.ExecutionRequest) budget.Reservation {
	return budget.Reservation{ID: "tool:" + q.RunID + ":" + q.Call.ID, RunID: q.RunID, RequestHash: kernel.ManagedDigest(q), Requested: budget.Usage{ToolCalls: 1}}
}

func exactUnsentToolReservation(actual, expected budget.Reservation) bool {
	return actual.ID == expected.ID && actual.RunID == expected.RunID && actual.RequestHash == expected.RequestHash && actual.PhysicalRequestHash == "" &&
		actual.Requested == expected.Requested && !actual.Dispatched && actual.Usage == (budget.Usage{}) && len(actual.Receipt) == 0
}

// commitUnsentTool records the original no-send disposition before any
// cross-store release or park. Only the native admission boundary or its
// qualified actual Dispatch denial can close the original speculative marker,
// never an unknown callback or provider effect.
func (r *Runner) commitUnsentTool(ctx context.Context, s kernel.Snapshot, state runState, call tools.Call, disposition unsentToolDisposition) (kernel.Snapshot, error) {
	if err := state.markUnsentTool(call, disposition); err != nil {
		return s, err
	}
	return r.commitToolTransition(ctx, s, state, s.Checkpoint, kernel.RunStatusRunning, unsentToolEvent(call, r.clock.Now()))
}

func unsentToolEvent(call tools.Call, now time.Time) kernel.EventDraft {
	return kernel.EventDraft{Type: "tool.admission_unsent", Message: call.ID, Wakeup: true, WakeupAt: agentWakeupAt(now)}
}

func (state *runState) markUnsentTool(call tools.Call, disposition unsentToolDisposition) error {
	if state.ToolDispatchID != "" && state.ToolDispatchID != call.ID {
		return ErrToolOutcomeUnknown
	}
	if state.ToolWait != nil || state.ExternalToolHold != nil {
		return ErrToolOutcomeUnknown
	}
	current, ok := nextPendingCall(*state)
	if !ok || !sameRecoveredCall(current, call) {
		return ErrToolReceiptMismatch
	}
	if a := state.ToolAdmission; a != nil && !a.authorized(call) {
		return ErrToolReceiptMismatch
	}
	a := newToolAdmission(call, toolAdmissionAuthorized)
	if state.ToolAdmission != nil {
		copy := *state.ToolAdmission
		a = &copy
	}
	a.Phase, a.Unsent = toolAdmissionUnsent, &disposition
	if !disposition.valid(a) {
		return ErrToolReceiptMismatch
	}
	state.ToolAdmission = a
	state.ToolDispatchID = ""
	if len(state.DeferredChildren) != 0 {
		return state.activateDeferredChild()
	}
	return nil
}

// resolveUnsentReservation accepts the durable disposition's exact original
// ledger only. The native coordinator validates the no-dispatch predicate
// again at CAS. A lost ACK cannot make Release look like an unknown result.
func (r *Runner) loadBoundToolLedger(ctx context.Context, runID string, scope BoundedBudgetScope) (budget.Ledger, error) {
	b := r.toolBudget
	if b == nil || scope.LedgerID == "" {
		return budget.Ledger{}, ErrToolReceiptMismatch
	}
	l, err := b.coordinator.Store.LoadBudget(ctx, scope.LedgerID)
	if err != nil || l.ID != scope.LedgerID || l.Limits != scope.Limits {
		return l, errors.Join(ErrToolReceiptMismatch, err)
	}
	binding, registered := l.Runs[runID]
	if !registered {
		return l, ErrToolReceiptMismatch
	}
	for _, original := range scope.Runs {
		if original.RunID == runID && original.Binding != binding {
			return l, ErrToolReceiptMismatch
		}
	}
	return l, nil
}

// Inspect the committed original scope and reservation without mutation. The
// Root receipt-only path also uses this before it clears a physical marker.
func (r *Runner) inspectUnsentReservation(ctx context.Context, runID string, call tools.Call, a *toolAdmission) (budget.Ledger, budget.Reservation, error) {
	var zero budget.Ledger
	var none budget.Reservation
	if a == nil || a.Phase != toolAdmissionUnsent || !a.matches(call) || !a.Unsent.valid(a) {
		return zero, none, ErrToolReceiptMismatch
	}
	if !a.BudgetBound {
		return zero, none, nil
	}
	b := r.toolBudget
	if b == nil {
		return zero, none, ErrToolReceiptMismatch
	}
	scope, err := b.ports.ResolveScope(ctx, runID)
	if err != nil {
		return zero, none, err
	}
	if scope.LedgerID != a.LedgerID {
		return zero, none, ErrToolReceiptMismatch
	}
	if a.LedgerID == "" {
		return zero, none, nil
	}
	l, err := r.loadBoundToolLedger(ctx, runID, scope)
	if err != nil {
		return zero, none, err
	}
	expected := originalToolReservation(tools.ExecutionRequest{RunID: runID, Call: call})
	res, found := l.Reservations[expected.ID]
	terminal := a.Unsent.Release || l.RunCancelled(runID)
	if found != a.Unsent.Reservation || (found && (!exactUnsentToolReservation(res, expected) ||
		(res.Status != budget.ReservationHeld && res.Status != budget.ReservationWaiting && (!terminal || res.Status != budget.ReservationReleased)))) {
		return zero, none, ErrToolReceiptMismatch
	}
	return l, expected, nil
}

// The result is a terminal failure code or empty for safe re-admission.
// An elected ledger cancellation can only tighten an original retry disposition.
func (r *Runner) resolveUnsentReservation(ctx context.Context, runID string, call tools.Call, a *toolAdmission) (string, error) {
	l, expected, err := r.inspectUnsentReservation(ctx, runID, call, a)
	if err != nil {
		return "", err
	}
	return r.resolveInspectedUnsent(ctx, runID, a, l, expected)
}

func (r *Runner) resolveInspectedUnsent(ctx context.Context, runID string, a *toolAdmission, l budget.Ledger, expected budget.Reservation) (string, error) {
	if l.ID == "" {
		return "", nil
	}
	b := r.toolBudget
	var err error
	if a.Unsent.Reservation {
		l, err = b.coordinator.ResolveUnsentTool(ctx, l.ID, expected, a.Unsent.Release, l.Revision)
		if err != nil {
			return "", err
		}
	}
	if err = b.ports.Project(ctx, l); err != nil {
		return "", err
	}
	if a.Unsent.Release {
		return a.Unsent.Code, nil
	}
	if l.RunCancelled(runID) {
		return "agent.shared_budget_exhausted", nil
	}
	return "", nil
}

// Consume a native no-send disposition, independently of the current Tool
// catalog, approval policy or local permit. Do not terminalize until any
// earlier physical siblings and staged receipts have drained.
func (r *Runner) finishUnsentTool(ctx context.Context, s kernel.Snapshot, state runState, call tools.Call) (kernel.Snapshot, error) {
	a := state.ToolAdmission
	if state.ToolDispatchID != "" || state.ToolWait != nil || len(state.DeferredChildren) != 0 || len(state.RecoveredTools) != 0 || a == nil || !a.matches(call) || a.Phase != toolAdmissionUnsent {
		return s, ErrToolOutcomeUnknown
	}
	code, err := r.resolveUnsentReservation(ctx, s.Run.ID, call, a)
	if err != nil {
		return s, err
	}
	if code != "" {
		state.clearToolAdmission(call.ID)
		// The precise terminal reason is persisted even if the Agent CAS fails.
		dimension := budget.Dimension("")
		if code != "agent.shared_budget_exhausted" {
			dimension = budget.Dimension(strings.TrimSuffix(strings.TrimPrefix(code, "agent.shared_"), "_budget"))
		}
		failed, failErr := r.fail(ctx, s, state, code, budget.ExhaustedAt(dimension))
		if failed.Run.ID == "" {
			return s, failErr
		}
		return failed, failErr
	}
	ready := *a
	ready.Unsent = nil
	ready.Phase = toolAdmissionAuthorized
	if a.Unsent.Reservation {
		ready.Phase = toolAdmissionCapacity
	}
	state.ToolAdmission = &ready
	return r.commitToolTransition(ctx, s, state, s.Checkpoint, kernel.RunStatusRunning, kernel.EventDraft{Type: "tool.admission_requeued", Message: call.ID, Wakeup: true, WakeupAt: agentWakeupAt(r.clock.Now())})
}

// One no-send transition/closure path for capacity denial and interruption.
func (r *Runner) settleUnsentTool(ctx context.Context, s kernel.Snapshot, state runState, call tools.Call, disposition unsentToolDisposition) (kernel.Snapshot, error) {
	next, err := r.commitUnsentTool(ctx, s, state, call, disposition)
	if err != nil {
		return s, err
	}
	decoded, err := decodeState(next.State)
	if err != nil {
		return next, err
	}
	if decoded.ToolDispatchID != "" || len(decoded.DeferredChildren) != 0 {
		// Free only the proven-unsent reservation. Earlier original
		// siblings still own their physical grants and receipts.
		_, err = r.resolveUnsentReservation(ctx, next.Run.ID, call, decoded.ToolAdmission)
		return next, err
	}
	return r.finishUnsentTool(ctx, next, decoded, call)
}

// Called only when no ToolChain entry occurred or its complete original
// first-party return proved no dispatch. Caller cancellation cannot
// interrupt the bounded no-send accounting/CAS handoff.
func (r *Runner) parkOriginalUnsentTool(ctx context.Context, s kernel.Snapshot, state runState, call tools.Call, ticket *toolBudgetAdmission, cause error) (kernel.Snapshot, bool, error) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	reserved := ticket != nil && ticket.ledger.ID != ""
	next, err := r.settleUnsentTool(cleanup, s, state, call, unsentToolDisposition{Reservation: reserved})
	returned := errors.Join(cause, err)
	// A terminal closure may already have committed its native business
	// failure. Bind the exact composed return here, while both original
	// causes are known. A successor or an outer callback cannot do so.
	if r.rootCallbacks != nil {
		if original := r.rootCallbacks.knownReturn.Load(); original.matches(next, next, err) {
			qualified := *original
			qualified.cause = returned
			r.rootCallbacks.knownReturn.Store(&qualified)
		}
	}
	return next, true, returned
}

// Original no-send admission (before entry or an exact returned native
// denial) can close the unchanged source or its exact committed successor;
// never rebase onto unrelated state or another call's marker.
func (r *Runner) parkUnsentAfterDispatchCAS(ctx context.Context, source kernel.Snapshot, e pendingToolExecution, intended runState, ticket *toolBudgetAdmission, cause error) (kernel.Snapshot, bool, error) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	latest, err := r.runtime.Load(cleanup, source.Run.ID)
	if err != nil {
		return source, true, errors.Join(cause, err)
	}
	if sameToolTransitionSource(latest, source) {
		return r.parkOriginalUnsentTool(cleanup, latest, e.state, e.call, ticket, cause)
	}
	if sameToolTransitionTarget(latest, source, intended, source.Checkpoint, kernel.RunStatusRunning) {
		return r.parkOriginalUnsentTool(cleanup, latest, intended, e.call, ticket, cause)
	}
	return source, true, errors.Join(cause, ErrToolOutcomeUnknown)
}

// reconcileOriginalUnsentTool consumes only the original native no-send
// proof. A successor cannot create this proof from an undispatched ledger,
// a context error, or a missing provider receipt.
func (r *Runner) reconcileOriginalUnsentTool(ctx context.Context, s kernel.Snapshot, state runState, g kernel.ChildExecutionGrant, c kernel.OwnedPhysicalCallbackCompletion, q tools.ExecutionRequest, b kernel.OwnedToolReturn) (kernel.Snapshot, error) {
	if !b.Unsent || state.ToolDispatchID != q.Call.ID || !c.MatchesReturnSnapshot(s) {
		return s, ErrToolReceiptMismatch
	}
	a := state.ToolAdmission
	if b.LedgerID != "" {
		if !a.authorized(q.Call) || !a.BudgetBound || a.LedgerID != b.LedgerID {
			return s, ErrToolReceiptMismatch
		}
	} else if a != nil && a.BudgetBound && a.LedgerID != "" {
		return s, ErrToolReceiptMismatch
	}
	if err := state.markUnsentTool(q.Call, unsentToolDisposition{Reservation: b.LedgerID != ""}); err != nil {
		return s, err
	}
	l, expected, err := r.inspectUnsentReservation(ctx, s.Run.ID, q.Call, state.ToolAdmission)
	if err != nil {
		return s, err
	}
	// The immutable original callback return is itself the durable
	// no-send disposition. Confirm its active marker AFTER the ledger
	// read, then resolve only at that ledger revision. A competing peer
	// may retire the original grant, but cannot let this stale operation
	// park a successor's new capacity reservation.
	latest, err := r.runtime.Load(ctx, s.Run.ID)
	if err != nil {
		return s, err
	}
	if !sameToolTransitionSource(latest, s) {
		return latest, kernel.ErrConflict
	}
	active, err := r.runtime.LookupChildExecution(ctx, s.Run.ID)
	if err != nil || active != g {
		return s, errors.Join(kernel.ErrExecutionConflict, err)
	}
	if _, err = r.resolveInspectedUnsent(ctx, s.Run.ID, state.ToolAdmission, l, expected); err != nil {
		return s, err
	}
	encoded, err := encodeState(state)
	if err != nil {
		return s, err
	}
	source := featurecontrol.Seal(s.Run.ID, s.Run.Revision, "agent_owned_receipt_recovery_source", g.ID, s.State, s.Checkpoint)
	target := featurecontrol.Seal(s.Run.ID, s.Run.Revision, "agent_owned_receipt_recovery_target", g.ID, encoded, s.Checkpoint)
	next, err := r.runtime.ApplyOwnedPhysicalRecovery(ctx, g, s.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: encoded, Checkpoint: s.Checkpoint, Events: []kernel.EventDraft{unsentToolEvent(q.Call, r.clock.Now())}}, source, target)
	if err != nil {
		return s, err
	}
	// Only the original immutable no-send proof could resolve the ledger
	// before this CAS. No ledger mutation may follow grant retirement.
	// Retire only if every other original physical obligation drained.
	if knownRootPhysicalSettlement(next) {
		if err = r.reconcileRootRetirement(ctx, next, g, &c); err != nil {
			return next, err
		}
	}
	return next, nil
}

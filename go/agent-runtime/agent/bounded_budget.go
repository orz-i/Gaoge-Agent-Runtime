package agent

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type BoundedBudgetScope struct {
	LedgerID string
	Limits   budget.Limits
	Runs     []BoundedBudgetRunBinding
	Retry    budget.RootRetryGrant
}
type BoundedBudgetRunBinding struct {
	RunID   string
	Binding budget.RunBudget
}

// Ports retain Host authorization, policy and persistence projections. None
// receives Next; Runtime alone owns dispatch/accounting orchestration.
type BoundedBudgetPorts interface {
	ResolveScope(context.Context, string) (BoundedBudgetScope, error)
	Project(context.Context, budget.Ledger) error
	PlanModel(context.Context, budget.Ledger, model.Request) (model.Request, budget.Reservation, error)
	PreviewModel(budget.Ledger, model.Request) (model.Request, budget.Reservation, error)
}

// One Store owns both Run and accounting facts; it is never selected as
// an independent execution mode after construction.
type BoundedBudgetAdapter struct {
	store interface {
		kernel.Store
		budget.LedgerStore
	}
	ports BoundedBudgetPorts
}

func NewBoundedBudgetAdapter(store budget.LedgerStore, ports BoundedBudgetPorts) (*BoundedBudgetAdapter, error) {
	native, ok := store.(interface {
		kernel.Store
		budget.LedgerStore
	})
	if !ok || ports == nil {
		return nil, ErrInvalidRequest
	}
	return &BoundedBudgetAdapter{native, ports}, nil
}

// One explicit owner accounts for every Run, Model and Tool stage. Atomic
// accounting cannot be combined with a generic chain: its physical receipt
// transitions must remain inside the fixed first-party boundary.
func validBoundedBudgetComposition(dependencies Dependencies) bool {
	if a := dependencies.BoundedBudget; a != nil {
		return a.ports != nil && dependencies.Runtime.UsesLedgerStore(a.store)
	}
	return true
}

// Native scope selection is creation-only. Later phases use the persisted
// Run binding; a Host lookup cannot move or retroactively enroll a Run.
func (a *BoundedBudgetAdapter) creationBudget(ctx context.Context, id string) (*budget.RunEnrollment, error) {
	scope, err := a.ports.ResolveScope(ctx, id)
	if err != nil || scope.LedgerID == "" {
		return nil, err
	}
	var binding budget.RunBudget
	found := false
	for _, run := range scope.Runs {
		if run.RunID == id {
			if found {
				return nil, budget.ErrLedgerConflict
			}
			binding, found = run.Binding, true
		}
	}
	if !found {
		return nil, budget.ErrLedgerConflict
	}
	e := budget.RunEnrollment{ScopeID: scope.LedgerID, Limits: scope.Limits, Binding: binding, Retry: scope.Retry}
	if !e.Valid(id) {
		return nil, budget.ErrLedgerConflict
	}
	return &e, nil
}

func (a *BoundedBudgetAdapter) bind(ctx context.Context, id string) (budget.Ledger, error) {
	return a.bindReceiptScope(ctx, id, "")
}

func (a *BoundedBudgetAdapter) bindReceiptScope(ctx context.Context, id, expected string) (budget.Ledger, error) {
	s, e := a.store.Load(ctx, id)
	if e != nil {
		return budget.Ledger{}, e
	}
	if expected != "" && expected != s.Run.BudgetScopeID {
		return budget.Ledger{}, budget.ErrLedgerConflict
	}
	if s.Run.BudgetScopeID == "" {
		// Unbudgeted Runs may not acquire a scope on Resume.
		scope, e := a.ports.ResolveScope(ctx, id)
		if e != nil {
			return budget.Ledger{}, e
		}
		if scope.LedgerID != "" {
			return budget.Ledger{}, budget.ErrLedgerConflict
		}
		return budget.Ledger{}, nil
	}
	return kernel.LoadRunBudget(ctx, a.store, s.Run)
}

// observeRunBudget is the read-only admission decision at the existing
// authoritative Ledger, not at a previously projected copy. LoadBudget
// serializes the observation with the Store's cancellation CAS. If cancel
// wins before this observation, no subsequent local consumption is admitted.
// The Kernel Grant, not this read, still fences concurrent Kernel control.
func (a *BoundedBudgetAdapter) observeRunBudget(ctx context.Context, original budget.Ledger, runID string) (budget.Ledger, error) {
	if err := ctx.Err(); err != nil {
		return budget.Ledger{}, err
	}
	if original.ID == "" {
		return original, nil
	}
	current, err := a.store.LoadBudget(ctx, original.ID)
	if err != nil {
		return budget.Ledger{}, err
	}
	before, wasRegistered := original.Runs[runID]
	after, registered := current.Runs[runID]
	if current.ID != original.ID || current.Revision < original.Revision || current.Revision == 0 ||
		current.Limits != original.Limits || !wasRegistered || !registered || before != after {
		return budget.Ledger{}, budget.ErrLedgerConflict
	}
	return current, ctx.Err()
}

// The original Run adapter carries its selected immutable scope into the
// terminal. Both live and deadline-only receipt reducers consume this same
// selection, including when an inner layer blocked after Run admission.
type runBudgetAdmission struct {
	adapter       *BoundedBudgetAdapter
	ledger        budget.Ledger
	projectionErr error
	// The terminal can observe a cancellation after this outer Run layer
	// returned from projection. Carry that actual decision back to the
	// original adapter, which still owns the final projection obligation.
	receiptDenial atomic.Pointer[budget.Ledger]
}

// requireReceiptConsumption is the final Ledger decision for a previously
// committed receipt, not a new reservation or external dispatch. The read
// is the ordering point with CancelRoot/CancelRun. A cancellation elected
// AFTER it cannot retroactively erase the admitted reducer; the existing
// Kernel Grant/CAS still owns the state transition. No distributed atomic
// transaction or durable authority is inferred from this observation.
func requireReceiptConsumption(ctx context.Context, runID, originalLedger string, owner *BoundedBudgetAdapter) error {
	frame, _ := callbackscope.RunCall(ctx).(*nativeRunAdmission)
	var bound *runBudgetAdmission
	if frame != nil && frame.invocation.RunID == runID {
		bound = frame.budget.Load()
	}
	if bound != nil {
		if originalLedger != "" && (owner != bound.adapter || originalLedger != bound.ledger.ID) {
			return budget.ErrLedgerConflict
		}
		if bound.projectionErr != nil {
			return bound.projectionErr
		}
		l, err := bound.adapter.observeRunBudget(ctx, bound.ledger, runID)
		if err != nil {
			return err
		}
		if l.ID != "" && l.RunCancelled(runID) {
			bound.receiptDenial.Store(&l)
			return budget.ErrExhausted
		}
		return nil
	}
	// Receipt recovery outside the admitted Run frame still checks the
	// exact durable ledger without creating a new reservation.
	if originalLedger == "" {
		return nil
	}
	if owner == nil {
		return budget.ErrLedgerConflict
	}
	l, err := owner.store.LoadBudget(ctx, originalLedger)
	if err != nil {
		return err
	}
	if _, ok := l.Runs[runID]; l.ID != originalLedger || l.Revision == 0 || !ok {
		return budget.ErrLedgerConflict
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if l.RunCancelled(runID) {
		return budget.ErrExhausted
	}
	return nil
}

func (a *BoundedBudgetAdapter) admitRun(ctx context.Context, inv plugin.RunInvocation) (*runBudgetAdmission, error) {
	frame, _ := callbackscope.RunCall(ctx).(*nativeRunAdmission)
	receiptOnly := frame != nil && frame.invocation == inv && frame.receiptOnly
	var l budget.Ledger
	var err error
	if frame != nil && frame.managedObligations != nil {
		p := frame.managedObligations
		if p.LedgerID == "" {
			return nil, budget.ErrLedgerConflict
		}
		l, err = a.bindReceiptScope(ctx, inv.RunID, p.LedgerID)
		if err == nil && !p.MatchesLiveLedger(l, frame.managedHost) {
			err = budget.ErrLedgerConflict
		}
	} else if receiptOnly {
		l, err = a.bindReceiptScope(ctx, inv.RunID, frame.receiptLedger)
	} else {
		l, err = a.bind(ctx, inv.RunID)
	}
	if err != nil {
		return nil, err
	}
	var ownErr error
	if l.ID != "" {
		ownErr = a.ports.Project(ctx, l)
		// Projection is a Host port and may have blocked while the
		// authoritative cancellation was committed. The projected value
		// is not the Run entry decision.
		current, observeErr := a.observeRunBudget(ctx, l, inv.RunID)
		if observeErr != nil {
			return nil, errors.Join(ownErr, observeErr)
		}
		if current.RunCancelled(inv.RunID) && current.Revision != l.Revision {
			// Only an actual acknowledged projection of the elected denial
			// can qualify the original native Run return.
			ownErr = errors.Join(ownErr, a.ports.Project(ctx, current))
		}
		l = current
		if l.RunCancelled(inv.RunID) {
			// No Next has entered. Only the actual bounded frame, after
			// native ledger and projection acknowledgement, can return a
			// qualified original admission denial. Do not mint a Tool or
			// model receipt, or certify a failed Host projection.
			if ownErr == nil {
				if denial := recordBudgetRunDenial(ctx, a, inv, l); denial != nil {
					return nil, denial
				}
			}
			return nil, errors.Join(budget.ErrExhausted, ownErr)
		}
		if receiptOnly && ownErr != nil {
			return nil, ownErr
		}
	}
	admission := &runBudgetAdmission{adapter: a, ledger: l, projectionErr: ownErr}
	if frame != nil && frame.invocation == inv {
		frame.budget.Store(admission)
	}
	return admission, nil
}

func (a *BoundedBudgetAdapter) finishRun(ctx context.Context, inv plugin.RunInvocation, admission *runBudgetAdmission, out kernel.Snapshot, callErr error) (kernel.Snapshot, error) {
	l, ownErr := admission.ledger, admission.projectionErr
	if denial := admission.receiptDenial.Load(); denial != nil {
		l = *denial
		ownErr = errors.Join(ownErr, a.ports.Project(context.WithoutCancel(ctx), l))
	}
	if l.ID != "" && out.Run.ID == inv.RunID && (out.Run.Status == kernel.RunStatusCompleted || out.Run.Status == kernel.RunStatusFailed || out.Run.Status == kernel.RunStatusCancelled) {
		accounting := context.WithoutCancel(ctx)
		// The terminal Run transaction already returned lifetime capacity.
		// The Host observes that fact; it cannot finish a second ledger.
		updated, e := a.observeRunBudget(accounting, l, inv.RunID)
		if e == nil && !updated.FinishedRuns[inv.RunID] {
			e = budget.ErrLedgerConflict
		}
		ownErr = errors.Join(ownErr, e)
		if e == nil {
			ownErr = errors.Join(ownErr, a.ports.Project(accounting, updated))
			l = updated
		}
	}
	if ownErr == nil {
		callbackscope.Record(ctx, a, "budget.run.returned", struct {
			Invocation plugin.RunInvocation
			Ledger     budget.Ledger
		}{inv, l})
	}
	return out, errors.Join(callErr, ownErr)
}

// UsesNativeBudget identifies the sole configured accounting owner.
func (r *Runner) UsesNativeBudget(a *BoundedBudgetAdapter) bool {
	return r != nil && a != nil && r.boundedBudget == a
}

func (r *Runner) modelBudget(plan *SamplingPlan) *BoundedBudgetAdapter {
	if plan == nil || plan.LedgerID == "" {
		return nil
	}
	return r.boundedBudget
}

func (a *BoundedBudgetAdapter) planModelDispatch(ctx context.Context, original, candidate model.Request, plan *SamplingPlan) (*budget.LedgerMutation, error) {
	l, err := a.bindReceiptScope(ctx, original.RunID, plan.LedgerID)
	if err != nil {
		return nil, err
	}
	physical, res, err := a.samplingReservation(ctx, l, original, plan)
	if errors.Is(err, budget.ErrWaiting) {
		return nil, model.NewRetryableError(err)
	}
	if err != nil {
		return nil, err
	}
	if !sameBoundedValue(physical, candidate) {
		return nil, ErrInvalidRequest
	}
	if res.Dispatched || res.Status == budget.ReservationSettled {
		return nil, ErrModelOutcomeUnknown
	}
	mutation, err := budget.PlanDispatch(ctx, l, res, true)
	if errors.Is(err, budget.ErrWaiting) {
		return nil, model.NewRetryableError(err)
	}
	if err != nil {
		return nil, err
	}
	if mutation == nil {
		return nil, budget.ErrLedgerConflict
	}
	return mutation, nil
}

// A returned call with an unknown outcome retains its dispatched quota, but
// no longer occupies a live execution slot. Commit this with the existing
// Run outcome diagnostic, not as an independent accounting write.
func (a *BoundedBudgetAdapter) planModelSuspend(ctx context.Context, inv ModelInvocation) (*budget.LedgerMutation, error) {
	l, err := a.store.LoadBudget(ctx, inv.SamplingPlan.LedgerID)
	if err != nil {
		return nil, err
	}
	_, res, err := a.samplingReservation(ctx, l, inv.Request, inv.SamplingPlan)
	if err != nil {
		return nil, err
	}
	if !res.Dispatched || res.Status != budget.ReservationHeld {
		return nil, budget.ErrLedgerConflict
	}
	return budget.PlanLedgerMutation(ctx, l, func(c budget.Coordinator) error {
		_, e := c.Suspend(ctx, l.ID, res.ID)
		return e
	})
}

// Plan the receipt using the original physical identity and the same
// original accounting reducer. No separate receipt transaction or speculative
// projection is required.
func (a *BoundedBudgetAdapter) planModelReceipt(ctx context.Context, inv ModelInvocation, out model.Response, callErr error) (*budget.LedgerMutation, model.Response, error, error) {
	plan := inv.SamplingPlan
	l, err := a.store.LoadBudget(ctx, plan.LedgerID)
	if err != nil {
		return nil, model.Response{}, nil, err
	}
	physical, res, err := a.samplingReservation(ctx, l, inv.Request, plan)
	if err != nil {
		return nil, model.Response{}, nil, err
	}
	if !res.Dispatched || res.Status != budget.ReservationHeld {
		return nil, model.Response{}, nil, budget.ErrLedgerConflict
	}
	mutation, err := budget.PlanLedgerMutation(ctx, l, func(c budget.Coordinator) error {
		_, e := c.SettleModel(ctx, l.ID, physical, out, callErr)
		return e
	})
	if err != nil {
		return nil, model.Response{}, nil, err
	}
	if mutation == nil {
		return nil, model.Response{}, nil, budget.ErrLedgerConflict
	}
	settled, receiptErr := budget.DecodeModelReceipt(mutation.Next().Reservations[res.ID])
	if receiptErr != nil && !model.IsResponseRejectedError(receiptErr) {
		return nil, model.Response{}, nil, receiptErr
	}
	return mutation, settled, receiptErr, nil
}

// Reconstruct the frozen physical request against the current original ledger.
func (a *BoundedBudgetAdapter) samplingReservation(_ context.Context, ledger budget.Ledger, original model.Request, frozen *SamplingPlan) (model.Request, budget.Reservation, error) {
	if frozen == nil {
		return model.Request{}, budget.Reservation{}, budget.ErrLedgerConflict
	}
	if frozen.LedgerID != ledger.ID || !frozen.valid(original) ||
		ledger.HasRunTokenLimits(original.RunID) != (frozen.Requested.InputTokens > 0) {
		return model.Request{}, budget.Reservation{}, budget.ErrLedgerConflict
	}
	request := frozen.request(original)
	res, err := frozen.reservation(original)
	if err != nil {
		return model.Request{}, budget.Reservation{}, err
	}
	if existing, found := ledger.Reservations[res.ID]; found {
		if existing.RunID != res.RunID || existing.RequestHash != res.RequestHash || existing.PhysicalRequestHash != res.PhysicalRequestHash || existing.Requested != res.Requested {
			return model.Request{}, res, budget.ErrLedgerConflict
		}
		res = existing
		if res.Dispatched || res.Status == budget.ReservationSettled || res.Status == budget.ReservationReleased {
			return request, res, nil
		}
	}
	// Preview the restrictions with this exact reservation in place, not
	// a new reservation that could consume a different hosted-tool quota.
	projected := budget.CloneLedger(ledger)
	held := res
	held.Status = budget.ReservationHeld
	projected.Reservations[res.ID] = held
	allowed, _, err := a.ports.PreviewModel(projected, model.CloneRequest(original))
	if err != nil {
		return model.Request{}, res, err
	}
	if !validDispatchModelRequest(allowed, request) {
		// A concurrent use of local-tool slots cannot remove declarations
		// from an already measured request. Wait for transient holds; a
		// permanent shortage is an explicit shared-budget denial.
		if len(allowed.Tools) == 0 && len(request.Tools) > 0 && validDispatchModelRequest(original, allowed) {
			for current := original.RunID; ; current = ledger.Runs[current].ParentRunID {
				view := ledger.View(current)
				if view.Limits.MaxToolCalls > 0 && res.Requested.ToolCalls >= view.Limits.MaxToolCalls-view.Usage.ToolCalls {
					return model.Request{}, res, budget.ErrExhausted
				}
				if current == "" {
					break
				}
			}
			return model.Request{}, res, budget.ErrWaiting
		}
		return model.Request{}, res, budget.ErrLedgerConflict
	}
	return request, res, nil
}

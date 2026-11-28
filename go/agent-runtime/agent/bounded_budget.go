package agent

import (
	"context"
	"errors"
	"strings"
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
type BoundedBudgetAdapter struct {
	coordinator budget.Coordinator
	ports       BoundedBudgetPorts
}

func NewBoundedBudgetAdapter(store budget.LedgerStore, ports BoundedBudgetPorts) (*BoundedBudgetAdapter, error) {
	if store == nil || ports == nil {
		return nil, ErrInvalidRequest
	}
	return &BoundedBudgetAdapter{budget.Coordinator{Store: store}, ports}, nil
}

// One explicit owner accounts for every Run, Model and Tool stage. Atomic
// accounting cannot be combined with a generic chain: its physical receipt
// transitions must remain inside the fixed first-party boundary.
func validBoundedBudgetComposition(dependencies Dependencies) bool {
	if a := dependencies.BoundedBudget; a != nil {
		return a.coordinator.Store != nil && a.ports != nil
	}
	return true
}

// Native scope selection is creation-only. Later phases use the persisted
// Run binding; a Host lookup cannot move or retroactively enroll a Run.
func (a *BoundedBudgetAdapter) nativeStore() kernel.Store {
	store, _ := a.coordinator.Store.(kernel.Store)
	return store
}
func (a *BoundedBudgetAdapter) resolveScope(ctx context.Context, id string) (BoundedBudgetScope, error) {
	if a.nativeStore() != nil {
		l, err := a.boundNativeLedger(ctx, id, "")
		if err != nil || l.ID == "" {
			return BoundedBudgetScope{}, err
		}
		return BoundedBudgetScope{LedgerID: l.ID, Limits: l.Limits, Runs: []BoundedBudgetRunBinding{{RunID: id, Binding: l.Runs[id]}}}, nil
	}
	return a.ports.ResolveScope(ctx, id)
}

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
	if a.nativeStore() != nil {
		return a.boundNativeLedger(ctx, id, "")
	}
	scope, err := a.resolveScope(ctx, id)
	if err != nil {
		return budget.Ledger{}, err
	}
	return a.bindScope(ctx, id, scope)
}

func (a *BoundedBudgetAdapter) bindScope(ctx context.Context, id string, scope BoundedBudgetScope) (budget.Ledger, error) {
	if scope.LedgerID == "" {
		return budget.Ledger{}, nil
	}
	if a.nativeStore() != nil {
		return a.boundNativeLedger(ctx, id, scope.LedgerID)
	}
	l, err := a.coordinator.Ensure(ctx, scope.LedgerID, scope.Limits)
	if err != nil {
		return l, err
	}
	for _, r := range scope.Runs {
		if r.RunID == "" {
			return l, ErrInvalidRequest
		}
		if old, ok := l.Runs[r.RunID]; ok {
			if !sameBoundedValue(old, r.Binding) {
				return l, budget.ErrLedgerConflict
			}
			continue
		}
		l, err = a.coordinator.RegisterRun(ctx, l.ID, r.RunID, r.Binding)
		if err != nil {
			return l, err
		}
	}
	if _, ok := l.Runs[id]; !ok {
		return l, ErrInvalidRequest
	}
	return l, nil
}

func (a *BoundedBudgetAdapter) boundNativeLedger(ctx context.Context, id, expected string) (budget.Ledger, error) {
	s, e := a.nativeStore().Load(ctx, id)
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
	return kernel.LoadRunBudget(ctx, a.coordinator.Store, s.Run)
}

// A known receipt cannot create or rebind accounting authority. Use the
// selected native Run adapter, but qualify the already-existing ledger
// before consuming any local result after the deadline.
func (a *BoundedBudgetAdapter) bindReceiptScope(ctx context.Context, runID, originalLedger string) (budget.Ledger, error) {
	if a.nativeStore() != nil {
		return a.boundNativeLedger(ctx, runID, originalLedger)
	}
	scope, err := a.ports.ResolveScope(ctx, runID)
	if err != nil {
		return budget.Ledger{}, err
	}
	if originalLedger != "" && scope.LedgerID != originalLedger {
		return budget.Ledger{}, budget.ErrLedgerConflict
	}
	if scope.LedgerID == "" {
		return budget.Ledger{}, nil
	}
	return a.loadExistingScope(ctx, runID, scope)
}

// No receipt or unsent Tool check may establish an accounting binding.
func (a *BoundedBudgetAdapter) loadExistingScope(ctx context.Context, runID string, scope BoundedBudgetScope) (budget.Ledger, error) {
	l, err := a.coordinator.Store.LoadBudget(ctx, scope.LedgerID)
	if err != nil {
		return l, err
	}
	if l.ID != scope.LedgerID || l.Limits != scope.Limits {
		return l, budget.ErrLedgerConflict
	}
	for _, r := range scope.Runs {
		if old, ok := l.Runs[r.RunID]; !ok || !sameBoundedValue(old, r.Binding) {
			return l, budget.ErrLedgerConflict
		}
	}
	if _, ok := l.Runs[runID]; !ok {
		return l, budget.ErrLedgerConflict
	}
	return l, nil
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
	current, err := a.coordinator.Store.LoadBudget(ctx, original.ID)
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
	l, err := owner.coordinator.Store.LoadBudget(ctx, originalLedger)
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

// Run is the standalone accounting entry. Runner stages use the same
// admission and settlement operations directly.
func (a *BoundedBudgetAdapter) Run(ctx context.Context, inv plugin.RunInvocation, next runContinuation) (kernel.Snapshot, error) {
	admission, err := a.admitRun(ctx, inv, nil)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	out, callErr := next(ctx)
	return a.finishRun(ctx, inv, admission, out, callErr, false)
}

func (a *BoundedBudgetAdapter) admitRun(ctx context.Context, inv plugin.RunInvocation, original *budget.Ledger) (*runBudgetAdmission, error) {
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
	} else if original != nil {
		l = *original
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

func (a *BoundedBudgetAdapter) finishRun(ctx context.Context, inv plugin.RunInvocation, admission *runBudgetAdmission, out kernel.Snapshot, callErr error, atomic bool) (kernel.Snapshot, error) {
	l, ownErr := admission.ledger, admission.projectionErr
	if denial := admission.receiptDenial.Load(); denial != nil {
		l = *denial
		ownErr = errors.Join(ownErr, a.ports.Project(context.WithoutCancel(ctx), l))
	}
	if l.ID != "" && out.Run.ID == inv.RunID && (out.Run.Status == kernel.RunStatusCompleted || out.Run.Status == kernel.RunStatusFailed || out.Run.Status == kernel.RunStatusCancelled) {
		accounting := context.WithoutCancel(ctx)
		var updated budget.Ledger
		var e error
		if atomic {
			// The terminal Run transaction already returned lifetime capacity.
			// A Host projection cannot create a missing execution fact.
			updated, e = a.observeRunBudget(accounting, l, inv.RunID)
			if e == nil && !updated.FinishedRuns[inv.RunID] {
				e = budget.ErrLedgerConflict
			}
		} else {
			updated, e = a.coordinator.FinishRuns(accounting, l.ID, inv.RunID)
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

// A co-located native budget is not model middleware: its dispatch and
// receipt are mutations of the existing Agent invocation. Opaque/externally
// stored budgets retain their old conservative callback boundary until the
// Host storage and managed recovery cutover is complete.
func (r *Runner) atomicBudget() *BoundedBudgetAdapter {
	a := r.boundedBudget
	if a == nil || !r.runtime.UsesLedgerStore(a.coordinator.Store) {
		return nil
	}
	return a
}

// UsesNativeBudget proves that the configured Host adapter is the same
// concrete accounting stage selected by this Agent, not just the same DB.
func (r *Runner) UsesNativeBudget(a *BoundedBudgetAdapter) bool {
	return r != nil && a != nil && r.atomicBudget() == a
}

func (r *Runner) atomicModelBudget(plan *SamplingPlan) *BoundedBudgetAdapter {
	if plan == nil || plan.LedgerID == "" {
		return nil
	}
	return r.atomicBudget()
}

func (a *BoundedBudgetAdapter) planModelDispatch(ctx context.Context, original, candidate model.Request, plan *SamplingPlan) (*budget.LedgerMutation, error) {
	l, err := a.bindReceiptScope(ctx, original.RunID, plan.LedgerID)
	if err != nil {
		return nil, err
	}
	physical, res, err := a.samplingReservation(ctx, l, original, plan)
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
	l, err := a.coordinator.Store.LoadBudget(ctx, inv.SamplingPlan.LedgerID)
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
// accounting reducer as standalone model calls. No independently committed
// receipt, speculative projection, or callback-return proof is required.
func (a *BoundedBudgetAdapter) planModelReceipt(ctx context.Context, inv ModelInvocation, out model.Response, callErr error) (*budget.LedgerMutation, model.Response, error, error) {
	plan := inv.SamplingPlan
	l, err := a.coordinator.Store.LoadBudget(ctx, plan.LedgerID)
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

func (a *BoundedBudgetAdapter) Model(ctx context.Context, request model.Request, emit model.StreamSink, next modelCall) (model.Response, error) {
	original := model.CloneRequest(request)
	call := modelCallFrom(ctx)
	frozen := call.samplingPlan()
	var l budget.Ledger
	var err error
	if frozen != nil && frozen.LedgerID != "" {
		l, err = a.bindReceiptScope(ctx, request.RunID, frozen.LedgerID)
	} else {
		l, err = a.bind(ctx, request.RunID)
	}
	if err != nil {
		return model.Response{}, err
	}
	if l.ID == "" {
		if frozen != nil && frozen.LedgerID != "" {
			return model.Response{}, budget.ErrLedgerConflict
		}
		out, e := next(ctx, request, emit)
		callbackscope.Record(ctx, a, "budget.unconfigured.returned", original)
		return out, e
	}
	// A Runtime invocation may not acquire a new ledger or re-plan after its
	// original intent was committed. Standalone calls have no such intent.
	if call != nil && (frozen == nil || frozen.LedgerID == "") {
		return model.Response{}, budget.ErrLedgerConflict
	}
	if strings.TrimSpace(request.InvocationID) == "" {
		return model.Response{}, ErrInvalidRequest
	}
	var res budget.Reservation
	for {
		revision := l.Revision
		request, res, err = a.samplingReservation(ctx, l, original, frozen)
		if err != nil {
			var denial samplingQuotaDenial
			if errors.As(err, &denial) {
				return model.Response{}, a.samplingDenial(ctx, original, l, res, denial.cause)
			}
			return model.Response{}, err
		}
		if res.Dispatched && res.Status != budget.ReservationSettled {
			return model.Response{}, model.NewRetryableError(budget.ErrWaiting)
		}
		l, err = a.coordinator.ReserveAtRevision(ctx, l.ID, res, true, revision)
		if errors.Is(err, budget.ErrLedgerConflict) && l.Revision != revision && l.ID != "" {
			// A concurrent reservation changed the ledger. Recheck the
			// same frozen physical request against the new revision.
			continue
		}
		break
	}
	projectErr := a.ports.Project(ctx, l)
	if value := l.Reservations[res.ID]; err == nil && value.Status == budget.ReservationSettled {
		out, callErr := budget.DecodeModelReceipt(value)
		out, callErr = ReplayModelResponseAuthority(ctx, request, out, callErr)
		if projectErr == nil {
			a.record(ctx, "budget.model.replayed", original, l, res.ID)
		}
		return a.settledModelResult(ctx, out, callErr, projectErr)
	}
	if projectErr != nil {
		return model.Response{}, projectErr
	}
	if err != nil {
		return model.Response{}, a.samplingDenial(ctx, original, l, res, err)
	}
	dispatched, err := a.coordinator.Dispatch(ctx, l.ID, res.ID)
	if err != nil {
		if errors.Is(err, budget.ErrWaiting) {
			err = model.NewRetryableError(err)
		}
		return model.Response{}, err
	}
	out, callErr := next(ctx, request, emit)
	out, callErr = ValidateModelResponseAuthority(ctx, out, callErr)
	accounting := context.WithoutCancel(ctx)
	if modelDeniedForRequest(ctx, original, callErr) {
		updated, e := a.coordinator.RequeueProvenUnsent(accounting, l.ID, res.ID, dispatched.Revision)
		if e != nil {
			return model.Response{}, errors.Join(callErr, e)
		}
		e = a.ports.Project(accounting, updated)
		if e == nil {
			a.record(ctx, "budget.model.requeued_unsent", original, updated, res.ID)
		}
		return model.Response{}, errors.Join(callErr, e)
	}
	knownRejection := model.IsResponseRejectedError(callErr) && out.Usage != nil
	if (callErr != nil && !knownRejection) || (l.HasRunTokenLimits(res.RunID) && out.Usage == nil) {
		updated, e := a.coordinator.Suspend(accounting, l.ID, res.ID)
		if callErr == nil {
			callErr = budget.ErrUnmetered
		}
		return out, errors.Join(callErr, e, a.ports.Project(accounting, updated))
	}
	updated, e := a.coordinator.SettleModel(accounting, l.ID, request, out, callErr)
	if e != nil {
		if errors.Is(e, budget.ErrInvalidUsage) {
			suspended, suspendErr := a.coordinator.Suspend(accounting, l.ID, res.ID)
			return out, errors.Join(e, suspendErr, a.ports.Project(accounting, suspended))
		}
		return out, e
	}
	// Both live delivery and read-only recovery interpret the same sealed
	// execution qualification. Projection failure is local, not a quota fact.
	settled, receiptErr := budget.DecodeModelReceipt(updated.Reservations[res.ID])
	projectErr = a.ports.Project(accounting, updated)
	if projectErr == nil {
		a.record(ctx, "budget.model.settled", original, updated, res.ID)
	}
	return a.settledModelResult(ctx, settled, errors.Join(receiptErr, callErr), projectErr)
}

// samplingReservation either reconstructs the previously admitted physical
// request or plans a legacy standalone Budget call. A live ledger may refuse
// the frozen plan; it must never rewrite it after Context admission.
func (a *BoundedBudgetAdapter) samplingReservation(ctx context.Context, ledger budget.Ledger, original model.Request, frozen *SamplingPlan) (model.Request, budget.Reservation, error) {
	if frozen == nil {
		request, res, err := a.ports.PlanModel(ctx, ledger, model.CloneRequest(original))
		if err != nil {
			return request, res, err
		}
		if res.RunID != original.RunID || res.ID == "" || !validDispatchModelRequest(original, request) {
			return request, res, ErrInvalidRequest
		}
		// Standalone Adapter calls have no durable Agent intent. Their ledger
		// still binds the physical receipt before dispatch. Existing settled
		// reservations can be replayed by their original logical admission ID.
		if res.Status == budget.ReservationSettled || res.Dispatched {
			return request, res, nil
		}
		hash, err := model.PhysicalRequestHash(request)
		if err != nil {
			return request, res, err
		}
		if res.PhysicalRequestHash != "" && res.PhysicalRequestHash != hash {
			return request, res, budget.ErrLedgerConflict
		}
		res.PhysicalRequestHash = hash
		return request, res, nil
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
					return model.Request{}, res, samplingQuotaDenial{budget.ErrExhausted}
				}
				if current == "" {
					break
				}
			}
			return model.Request{}, res, samplingQuotaDenial{budget.ErrWaiting}
		}
		return model.Request{}, res, budget.ErrLedgerConflict
	}
	return request, res, nil
}

// Only the Runtime's live quota check can certify a planning-time denial.
type samplingQuotaDenial struct{ cause error }

func (e samplingQuotaDenial) Error() string { return e.cause.Error() }
func (e samplingQuotaDenial) Unwrap() error { return e.cause }

// Only the concrete budget adapter can prove that reservation admission
// failed before a provider dispatch. An already-dispatched reservation is
// never converted into a known no-effect outcome.
func (a *BoundedBudgetAdapter) samplingDenial(ctx context.Context, original model.Request, ledger budget.Ledger, res budget.Reservation, err error) error {
	if !errors.Is(err, budget.ErrWaiting) && !errors.Is(err, budget.ErrExhausted) {
		return err
	}
	existing := ledger.Reservations[res.ID]
	if ledger.ID != "" && res.ID == "model:"+original.InvocationID && res.RunID == original.RunID &&
		!res.Dispatched && !existing.Dispatched && res.Status != budget.ReservationSettled && existing.Status != budget.ReservationSettled {
		a.record(ctx, "budget.model.admission_denied", original, ledger, res.ID)
	}
	if errors.Is(err, budget.ErrWaiting) {
		return model.NewRetryableError(err)
	}
	return err
}

func (a *BoundedBudgetAdapter) record(ctx context.Context, op string, request model.Request, l budget.Ledger, id string) {
	callbackscope.Record(ctx, a, op, struct {
		Request     model.Request
		LedgerID    string
		Revision    uint64
		Reservation budget.Reservation
	}{request, l.ID, l.Revision, l.Reservations[id]})
}
func (a *BoundedBudgetAdapter) settledModelResult(ctx context.Context, out model.Response, callErr, localErr error) (model.Response, error) {
	if localErr != nil {
		if model.IsResponseRejectedError(callErr) {
			callErr = errors.Join(callErr, localErr)
		} else {
			callErr = model.NewClassifiedError(errors.Join(callErr, localErr), "model.post_generation_failed")
			if out.Usage != nil {
				callErr = model.NewResponseRejectedError(callErr)
			}
		}
	}
	if call := modelCallFrom(ctx); call != nil && call.completion != nil {
		return call.completion.settle(out, callErr)
	}
	return out, callErr
}

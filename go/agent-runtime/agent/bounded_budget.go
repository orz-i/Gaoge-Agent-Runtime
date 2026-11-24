package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

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
}
type BoundedBudgetRunBinding struct {
	RunID   string
	Binding budget.RunBudget
}
type BoundedBudgetSettlement struct {
	SuspendOnError bool
	Response       model.Response
	Usage          budget.Usage
	Receipt        json.RawMessage
	Related        []budget.Reservation
}

// Ports retain Host authorization, policy and persistence projections. None
// receives Next; Runtime alone owns dispatch/accounting orchestration.
type BoundedBudgetPorts interface {
	ResolveScope(context.Context, string) (BoundedBudgetScope, error)
	Project(context.Context, budget.Ledger) error
	PlanModel(context.Context, budget.Ledger, model.Request) (model.Request, budget.Reservation, error)
	PreviewModel(budget.Ledger, model.Request) (model.Request, budget.Reservation, error)
	RequiresTokenUsage(budget.Ledger, string) bool
	PlanSettlement(budget.Ledger, budget.Reservation, model.Response, error) (BoundedBudgetSettlement, error)
	ValidateSettlement(budget.Ledger, budget.Ledger, budget.Reservation, budget.Usage) error
	DecodeReceipt(json.RawMessage) (model.Response, error)
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
func (*BoundedBudgetAdapter) Name() string { return "runtime.bounded_budget.v1" }

// One Agent execution has ONE original Budget ticket owner. Different
// adapters in Run/Model/Tool chains would split the scope and allow a callback
// to be charged or certified by an unrelated budget authority.
func validBoundedBudgetComposition(dependencies Dependencies) bool {
	var original *BoundedBudgetAdapter
	accept := func(candidate *BoundedBudgetAdapter) bool {
		if candidate == nil {
			return false
		}
		if original == nil {
			original = candidate
			return true
		}
		return original == candidate
	}
	run, modelSelected, tool := false, false, false
	for _, layer := range dependencies.RunMiddleware {
		if b, ok := layer.(*BoundedBudgetAdapter); ok {
			if run || !accept(b) {
				return false
			}
			run = true
		}
	}
	for _, layer := range dependencies.ModelMiddleware {
		if b, ok := layer.(*BoundedBudgetAdapter); ok {
			if modelSelected || !accept(b) {
				return false
			}
			modelSelected = true
		}
	}
	for _, layer := range dependencies.ToolMiddleware {
		if b, ok := layer.(*BoundedBudgetAdapter); ok {
			if tool || !accept(b) {
				return false
			}
			tool = true
		}
	}
	return true
}
func (a *BoundedBudgetAdapter) bind(ctx context.Context, id string) (budget.Ledger, error) {
	scope, err := a.ports.ResolveScope(ctx, id)
	if err != nil || scope.LedgerID == "" {
		return budget.Ledger{}, err
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
func (a *BoundedBudgetAdapter) Run(ctx context.Context, inv plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	l, err := a.bind(ctx, inv.RunID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	var ownErr error
	if l.ID != "" {
		ownErr = a.ports.Project(ctx, l)
		if l.View(inv.RunID).Cancelled {
			return kernel.Snapshot{}, errors.Join(budget.ErrExhausted, ownErr)
		}
	}
	out, callErr := next(ctx)
	if l.ID != "" && out.Run.ID == inv.RunID && (out.Run.Status == kernel.RunStatusCompleted || out.Run.Status == kernel.RunStatusFailed || out.Run.Status == kernel.RunStatusCancelled) {
		updated, e := a.coordinator.FinishRuns(context.WithoutCancel(ctx), l.ID, inv.RunID)
		ownErr = errors.Join(ownErr, e, a.ports.Project(context.WithoutCancel(ctx), updated))
		l = updated
	}
	if ownErr == nil {
		callbackscope.Record(ctx, a, "budget.run.returned", struct {
			Invocation plugin.RunInvocation
			Ledger     budget.Ledger
		}{inv, l})
	}
	return out, errors.Join(callErr, ownErr)
}
func (a *BoundedBudgetAdapter) Model(ctx context.Context, request model.Request, emit model.StreamSink, next plugin.ModelNext) (model.Response, error) {
	original := model.CloneRequest(request)
	call := modelCallFrom(ctx)
	frozen := call.samplingPlan()
	l, err := a.bind(ctx, request.RunID)
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
		out, callErr := a.ports.DecodeReceipt(value.Receipt)
		out, callErr = ReplayModelResponseAuthority(ctx, request, out, callErr)
		if projectErr == nil {
			a.record(ctx, "budget.model.replayed", original, l, res.ID)
		}
		return boundedSettledModelResult(out, callErr, projectErr)
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
	if (callErr != nil && !knownRejection) || (a.ports.RequiresTokenUsage(l, res.RunID) && out.Usage == nil) {
		updated, e := a.coordinator.Suspend(accounting, l.ID, res.ID)
		if callErr == nil {
			callErr = budget.ErrUnmetered
		}
		return out, errors.Join(callErr, e, a.ports.Project(accounting, updated))
	}
	plan, e := a.ports.PlanSettlement(l, res, out, callErr)
	if e != nil {
		if !plan.SuspendOnError {
			return out, e
		}
		updated, suspendErr := a.coordinator.Suspend(accounting, l.ID, res.ID)
		return out, errors.Join(e, suspendErr, a.ports.Project(accounting, updated))
	}
	updated, e := a.coordinator.SettleWithReceipts(accounting, l.ID, res.ID, plan.Usage, plan.Receipt, plan.Related)
	if e != nil {
		return plan.Response, e
	}
	validationErr := a.ports.ValidateSettlement(l, updated, res, plan.Usage)
	projectErr = a.ports.Project(accounting, updated)
	if projectErr == nil {
		a.record(ctx, "budget.model.settled", original, updated, res.ID)
	}
	return boundedSettledModelResult(plan.Response, callErr, errors.Join(validationErr, projectErr))
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
		a.ports.RequiresTokenUsage(ledger, original.RunID) != (frozen.Requested.InputTokens > 0) {
		return model.Request{}, budget.Reservation{}, budget.ErrLedgerConflict
	}
	request, res := frozen.request(original), frozen.reservation(original)
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
func boundedSettledModelResult(out model.Response, callErr, localErr error) (model.Response, error) {
	if localErr != nil {
		callErr = model.NewClassifiedError(errors.Join(callErr, localErr), "model.post_generation_failed")
		if out.Usage != nil {
			callErr = model.NewResponseRejectedError(callErr)
		}
	}
	return out, callErr
}

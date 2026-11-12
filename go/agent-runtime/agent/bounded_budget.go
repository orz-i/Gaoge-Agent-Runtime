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
	ApplyLocalToolBudget(model.Request, budget.Ledger) model.Request
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
	l, err := a.bind(ctx, request.RunID)
	if err != nil {
		return model.Response{}, err
	}
	if l.ID == "" {
		out, e := next(ctx, request, emit)
		callbackscope.Record(ctx, a, "budget.unconfigured.returned", original)
		return out, e
	}
	if strings.TrimSpace(request.InvocationID) == "" {
		return model.Response{}, ErrInvalidRequest
	}
	request, res, err := a.ports.PlanModel(ctx, l, request)
	if err != nil {
		return model.Response{}, err
	}
	if res.RunID != original.RunID || res.ID == "" || request.InvocationID != original.InvocationID || request.RunID != original.RunID {
		return model.Response{}, ErrInvalidRequest
	}
	if res.Dispatched && res.Status != budget.ReservationSettled {
		return model.Response{}, model.NewRetryableError(budget.ErrWaiting)
	}
	l, err = a.coordinator.Reserve(ctx, l.ID, res, true)
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
	if errors.Is(err, budget.ErrWaiting) {
		return model.Response{}, model.NewRetryableError(err)
	}
	if err != nil {
		return model.Response{}, err
	}
	request = a.ports.ApplyLocalToolBudget(request, l)
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
	if modelDeniedForRequest(ctx, request, callErr) {
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

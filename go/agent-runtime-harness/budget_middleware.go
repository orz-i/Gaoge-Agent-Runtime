package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type BudgetMiddlewareDependencies struct {
	Store     Store
	Ledgers   budget.LedgerStore
	Relations ContextRunRelationSource
	Meter     model.TokenAdmissionPlanner
	Clock     Clock
	TurnFeed  *TurnFeed
}

// BudgetMiddleware is composed at the universal sampling and local Tool
// boundaries. Run wrapping registers ancestry before child execution. It does
// not own Feature steps, loops, retries, or application-domain state.
type BudgetMiddleware struct{ dependencies BudgetMiddlewareDependencies }

func NewBudgetMiddleware(dependencies BudgetMiddlewareDependencies) (*BudgetMiddleware, error) {
	if dependencies.Ledgers == nil {
		dependencies.Ledgers, _ = dependencies.Store.(budget.LedgerStore)
	}
	if dependencies.Store == nil || dependencies.Ledgers == nil || dependencies.Clock == nil {
		return nil, ErrInvalidRequest
	}
	return &BudgetMiddleware{dependencies: dependencies}, nil
}

func (*BudgetMiddleware) Name() string { return "harness.shared_budget" }

func (middleware *BudgetMiddleware) coordinator() budget.Coordinator {
	return budget.Coordinator{Store: middleware.dependencies.Ledgers}
}

// AdmitRemote admits the child count but forbids a remote execution from
// bypassing token limits when the protocol has no enforceable usage contract.
func (middleware *BudgetMiddleware) AdmitRemote(ctx context.Context, runID string) error {
	ledger, err := middleware.bindRun(ctx, runID)
	if err != nil || ledger.ID == "" {
		return err
	}
	if hasRunTokenLimits(ledger, runID) {
		return budget.ErrUnmetered
	}
	return middleware.project(ctx, ledger)
}

func (middleware *BudgetMiddleware) Run(ctx context.Context, invocation plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	ledger, err := middleware.bindRun(ctx, invocation.RunID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if err = middleware.project(ctx, ledger); err != nil {
		return kernel.Snapshot{}, err
	}
	if ledger.View(invocation.RunID).Cancelled {
		return kernel.Snapshot{}, budget.ErrExhausted
	}
	return next(ctx)
}

func (middleware *BudgetMiddleware) bindRun(ctx context.Context, runID string) (budget.Ledger, error) {
	turn, _, err := resolveContextTurnForRun(ctx, middleware.dependencies.Store, middleware.dependencies.Relations, runID)
	if errors.Is(err, ErrNotFound) {
		return budget.Ledger{}, nil
	}
	if err != nil {
		return budget.Ledger{}, err
	}
	config, err := middleware.dependencies.Store.GetConfigSnapshot(ctx, turn.ConfigSnapshotID)
	if err != nil || config.SharedBudget == nil {
		return budget.Ledger{}, err
	}
	ledger, err := middleware.coordinator().Ensure(ctx, turn.ID, *config.SharedBudget)
	if err != nil {
		return ledger, err
	}
	return middleware.registerAncestry(ctx, ledger, runID, map[string]bool{})
}

func (middleware *BudgetMiddleware) registerAncestry(ctx context.Context, ledger budget.Ledger, runID string, seen map[string]bool) (budget.Ledger, error) {
	if _, exists := ledger.Runs[runID]; exists {
		return ledger, nil
	}
	if seen[runID] {
		return ledger, ErrConflict
	}
	seen[runID] = true
	parent, found, err := contextParentRunID(ctx, middleware.dependencies.Relations, runID)
	if err != nil {
		return ledger, err
	}
	if found {
		ledger, err = middleware.registerAncestry(ctx, ledger, parent, seen)
		if err != nil {
			return ledger, err
		}
	}
	return middleware.coordinator().RegisterRun(ctx, ledger.ID, runID, budget.RunBudget{ParentRunID: parent})
}

func (middleware *BudgetMiddleware) Model(ctx context.Context, request model.Request, emit model.StreamSink, next plugin.ModelNext) (model.Response, error) {
	ledger, err := middleware.bindRun(ctx, request.RunID)
	if err != nil {
		return model.Response{}, err
	}
	if ledger.ID == "" {
		return next(ctx, request, emit)
	}
	if strings.TrimSpace(request.InvocationID) == "" {
		return model.Response{}, ErrInvalidRequest
	}
	request, reservation, err := middleware.modelReservation(ctx, ledger, request)
	if err != nil {
		return model.Response{}, err
	}
	if reservation.Dispatched && reservation.Status != budget.ReservationSettled {
		// A previous process may have sent this exact logical request. Only an
		// authoritative receipt may reconcile it; retrying could charge twice.
		return model.Response{}, model.NewRetryableError(budget.ErrWaiting)
	}
	ledger, err = middleware.coordinator().Reserve(ctx, ledger.ID, reservation, true)
	if projectErr := middleware.project(ctx, ledger); projectErr != nil {
		return model.Response{}, projectErr
	}
	if errors.Is(err, budget.ErrWaiting) {
		return model.Response{}, model.NewRetryableError(err)
	}
	if err != nil {
		return model.Response{}, err
	}
	if value := ledger.Reservations[reservation.ID]; value.Status == budget.ReservationSettled {
		var response model.Response
		err = json.Unmarshal(value.Receipt, &response)
		return response, err
	}
	if _, err = middleware.coordinator().Dispatch(ctx, ledger.ID, reservation.ID); err != nil {
		if errors.Is(err, budget.ErrWaiting) {
			return model.Response{}, model.NewRetryableError(err)
		}
		return model.Response{}, err
	}
	response, callErr := next(ctx, request, emit)
	return middleware.settleModel(ctx, ledger, reservation, response, callErr)
}

func (middleware *BudgetMiddleware) modelReservation(ctx context.Context, ledger budget.Ledger, request model.Request) (model.Request, budget.Reservation, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return request, budget.Reservation{}, err
	}
	reservation := budget.Reservation{ID: "model:" + request.InvocationID, RunID: request.RunID, RequestHash: hashInvocationBytes(raw), Requested: budget.Usage{LLMCalls: 1}}
	if existing, found := ledger.Reservations[reservation.ID]; found {
		if existing.RequestHash != reservation.RequestHash {
			return request, reservation, budget.ErrLedgerConflict
		}
		request.MaxOutputTokens = existing.Requested.OutputTokens
		if existing.Status != budget.ReservationSettled && !existing.Dispatched && hasRunTokenLimits(ledger, request.RunID) {
			if middleware.dependencies.Meter == nil {
				return request, existing, budget.ErrUnmetered
			}
			admission, admissionErr := middleware.dependencies.Meter.PlanTokenAdmission(ctx, model.CloneRequest(request))
			if admissionErr != nil {
				return request, existing, admissionErr
			}
			if admission.InputUpperBound <= 0 || admission.InputUpperBound > existing.Requested.InputTokens || admission.MaxOutputTokens <= 0 {
				return request, existing, budget.ErrUnmetered
			}
		}
		return request, existing, nil
	}
	if !hasRunTokenLimits(ledger, request.RunID) {
		return request, reservation, nil
	}
	if middleware.dependencies.Meter == nil {
		return request, reservation, budget.ErrUnmetered
	}
	admission, err := middleware.dependencies.Meter.PlanTokenAdmission(ctx, model.CloneRequest(request))
	if err != nil {
		return request, reservation, err
	}
	if admission.InputUpperBound <= 0 || admission.MaxOutputTokens <= 0 {
		return request, reservation, budget.ErrUnmetered
	}
	output := remainingOutputCeiling(ledger, request.RunID, admission.InputUpperBound, admission.MaxOutputTokens)
	if output <= 0 {
		return request, reservation, budget.ErrExhausted
	}
	request.MaxOutputTokens = output
	reservation.Requested.InputTokens = admission.InputUpperBound
	reservation.Requested.OutputTokens = output
	reservation.Requested.TotalTokens = admission.InputUpperBound + output
	if !budget.ValidUsage(reservation.Requested) {
		return request, reservation, budget.ErrInvalidUsage
	}
	return request, reservation, nil
}

func (middleware *BudgetMiddleware) settleModel(ctx context.Context, ledger budget.Ledger, reservation budget.Reservation, response model.Response, callErr error) (model.Response, error) {
	accountingCtx := context.WithoutCancel(ctx)
	if callErr != nil || (hasRunTokenLimits(ledger, reservation.RunID) && response.Usage == nil) {
		// A timeout, cancellation, or missing receipt cannot prove a zero charge.
		updated, err := middleware.coordinator().Suspend(accountingCtx, ledger.ID, reservation.ID)
		if callErr == nil {
			callErr = budget.ErrUnmetered
		}
		return response, errors.Join(callErr, err, middleware.project(accountingCtx, updated))
	}
	var tokens *budget.TokenUsage
	if response.Usage != nil {
		tokens = &budget.TokenUsage{InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens,
			CacheReadTokens: response.Usage.CacheReadTokens, CacheWriteTokens: response.Usage.CacheWriteTokens, ReasoningTokens: response.Usage.ReasoningTokens}
	}
	usage, err := budget.ChargeModelCall(budget.Usage{}, tokens)
	if err != nil {
		return response, err
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return response, err
	}
	updated, err := middleware.coordinator().Settle(accountingCtx, ledger.ID, reservation.ID, usage, raw)
	if err != nil {
		return response, err
	}
	if hasRunTokenLimits(ledger, reservation.RunID) && (usage.InputTokens > reservation.Requested.InputTokens || usage.OutputTokens > reservation.Requested.OutputTokens) {
		err = budget.ErrInvalidUsage
	}
	return response, errors.Join(err, middleware.project(accountingCtx, updated))
}

func (middleware *BudgetMiddleware) Tool(ctx context.Context, invocation plugin.ToolInvocation, next plugin.ToolNext) (tools.ExecutionResult, error) {
	ledger, err := middleware.bindRun(ctx, invocation.Run.ID)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	if ledger.ID == "" {
		return next(ctx)
	}
	raw, err := json.Marshal(invocation.Request)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	id := "tool:" + invocation.Run.ID + ":" + invocation.Request.Call.ID
	reservation := budget.Reservation{ID: id, RunID: invocation.Run.ID, RequestHash: hashInvocationBytes(raw), Requested: budget.Usage{ToolCalls: 1}}
	// Infrastructure calls may synchronously enter child Features; counting the
	// waiting coordinator as active would deadlock a one-slot environment.
	active := invocation.Definition.Key != DelegationToolKey && invocation.Definition.Key != CapabilityInvocationToolKey
	ledger, err = middleware.coordinator().Reserve(ctx, ledger.ID, reservation, active)
	if projectErr := middleware.project(ctx, ledger); projectErr != nil {
		return tools.ExecutionResult{}, projectErr
	}
	if errors.Is(err, budget.ErrWaiting) {
		return tools.ExecutionResult{Content: json.RawMessage(`{"status":"waiting_budget"}`), Receipt: tools.Receipt{ExecutionID: invocation.Request.Call.ID, Disposition: tools.ReceiptDispositionPending}}, nil
	}
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	if value := ledger.Reservations[id]; value.Status == budget.ReservationSettled {
		return decodeBudgetToolReceipt(value.Receipt)
	}
	if !ledger.Reservations[id].Dispatched {
		if _, err = middleware.coordinator().Dispatch(ctx, ledger.ID, id); err != nil {
			return tools.ExecutionResult{}, err
		}
	}
	result, callErr := next(ctx)
	accountingCtx := context.WithoutCancel(ctx)
	code, message, recoverable := tools.RecoverableCallErrorInfo(callErr)
	if (callErr != nil && !recoverable) || result.Receipt.Disposition == tools.ReceiptDispositionPending {
		updated, suspendErr := middleware.coordinator().Suspend(accountingCtx, ledger.ID, id)
		return result, errors.Join(callErr, suspendErr, middleware.project(accountingCtx, updated))
	}
	raw, err = json.Marshal(budgetToolReceipt{Result: result, ErrorCode: code, ErrorMessage: message, BlockedTools: tools.RecoverableCallErrorBlockedToolKeys(callErr)})
	if err != nil {
		return result, err
	}
	updated, err := middleware.coordinator().Settle(accountingCtx, ledger.ID, id, budget.Usage{ToolCalls: 1}, raw)
	return result, errors.Join(callErr, err, middleware.project(accountingCtx, updated))
}

type budgetToolReceipt struct {
	Result       tools.ExecutionResult `json:"result"`
	ErrorCode    string                `json:"errorCode,omitempty"`
	ErrorMessage string                `json:"errorMessage,omitempty"`
	BlockedTools []string              `json:"blockedTools,omitempty"`
}

func decodeBudgetToolReceipt(raw json.RawMessage) (tools.ExecutionResult, error) {
	var receipt budgetToolReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return tools.ExecutionResult{}, err
	}
	if receipt.ErrorCode != "" {
		return receipt.Result, tools.NewRecoverableCallErrorWithBlockedTools(receipt.ErrorCode, receipt.ErrorMessage, nil, receipt.BlockedTools...)
	}
	return receipt.Result, nil
}

func (middleware *BudgetMiddleware) project(ctx context.Context, ledger budget.Ledger) error {
	if ledger.ID == "" {
		return nil
	}
	return projectBudgetItem(ctx, middleware.dependencies.Store, middleware.dependencies.TurnFeed, middleware.dependencies.Clock, ledger)
}

func projectBudgetItem(ctx context.Context, store Store, feed *TurnFeed, clock Clock, ledger budget.Ledger) error {
	payload, err := json.Marshal(ledger.View(""))
	if err != nil {
		return err
	}
	now := clock.Now().UTC()
	_, err = appendItemFact(ctx, store, feed, Item{ID: stableID("hbudget", ledger.ID, uintString(ledger.Revision)), TurnID: ledger.ID,
		Kind: ItemBudget, Status: ItemCompleted, Payload: payload, CreatedAt: now, UpdatedAt: now})
	return err
}

func hasRunTokenLimits(ledger budget.Ledger, runID string) bool {
	if budget.HasTokenLimits(ledger.Limits) {
		return true
	}
	for runID != "" {
		binding := ledger.Runs[runID]
		if budget.HasTokenLimits(binding.Limits) {
			return true
		}
		runID = binding.ParentRunID
	}
	return false
}

func remainingOutputCeiling(ledger budget.Ledger, runID string, input, output int64) int64 {
	for current := runID; ; current = ledger.Runs[current].ParentRunID {
		view := ledger.View(current)
		if view.Limits.MaxOutputTokens > 0 {
			output = min(output, view.Limits.MaxOutputTokens-view.Usage.OutputTokens)
		}
		if view.Limits.MaxTotalTokens > 0 {
			output = min(output, view.Limits.MaxTotalTokens-view.Usage.TotalTokens-input)
		}
		if current == "" {
			return output
		}
	}
}

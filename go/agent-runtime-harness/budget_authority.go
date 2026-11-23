package harness

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type BudgetAuthorityDependencies struct {
	Store     Store
	Ledgers   budget.LedgerStore
	Relations ContextRunRelationSource
	Meter     model.TokenAdmissionPlanner
	Clock     Clock
	TurnFeed  *TurnFeed
}

// BudgetAuthority is the Host-owned ledger and original-receipt authority.
// It NEVER implements Run/Model/Tool middleware: the sole physical execution
// driver is the native agent.BoundedBudgetAdapter, composed via Adapter().
type BudgetAuthority struct {
	dependencies BudgetAuthorityDependencies
	adapterOnce  sync.Once
	adapter      *agent.BoundedBudgetAdapter
	adapterErr   error
}

func NewBudgetAuthority(dependencies BudgetAuthorityDependencies) (*BudgetAuthority, error) {
	if dependencies.Ledgers == nil {
		dependencies.Ledgers, _ = dependencies.Store.(budget.LedgerStore)
	}
	if dependencies.Store == nil || dependencies.Ledgers == nil || dependencies.Clock == nil {
		return nil, ErrInvalidRequest
	}
	return &BudgetAuthority{dependencies: dependencies}, nil
}

func (authority *BudgetAuthority) coordinator() budget.Coordinator {
	return budget.Coordinator{Store: authority.dependencies.Ledgers}
}

// AdmitRemote admits the child count but forbids a remote execution from
// bypassing token limits when the protocol has no enforceable usage contract.
func (authority *BudgetAuthority) AdmitRemote(ctx context.Context, runID string) error {
	ledger, err := authority.bindRun(ctx, runID)
	if err != nil || ledger.ID == "" {
		return err
	}
	if hasRunTokenLimits(ledger, runID) {
		return budget.ErrUnmetered
	}
	return authority.project(ctx, ledger)
}

func (authority *BudgetAuthority) resolveScope(ctx context.Context, runID string) (agent.BoundedBudgetScope, error) {
	d := authority.dependencies
	turn, _, err := resolveContextTurnForRun(ctx, d.Store, d.Relations, runID)
	if errors.Is(err, ErrNotFound) {
		return agent.BoundedBudgetScope{}, nil
	}
	if err != nil {
		return agent.BoundedBudgetScope{}, err
	}
	config, err := loadSealedConfig(ctx, d.Store, turn.ConfigSnapshotID)
	if err != nil || config.SharedBudget == nil {
		return agent.BoundedBudgetScope{}, err
	}
	scope := agent.BoundedBudgetScope{LedgerID: turn.ID, Limits: *config.SharedBudget}
	existing, err := d.Ledgers.LoadBudget(ctx, turn.ID)
	if err != nil && !errors.Is(err, budget.ErrLedgerNotFound) {
		return scope, err
	}
	seen := map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if seen[id] {
			return ErrConflict
		}
		seen[id] = true
		if binding, ok := existing.Runs[id]; ok {
			// Existing bindings already carry their admitted ancestry and limits.
			scope.Runs = append(scope.Runs, agent.BoundedBudgetRunBinding{RunID: id, Binding: binding})
			return nil
		}
		parent, found, err := contextParentRunID(ctx, d.Relations, id)
		if err != nil {
			return err
		}
		if found {
			if err = visit(parent); err != nil {
				return err
			}
		}
		scope.Runs = append(scope.Runs, agent.BoundedBudgetRunBinding{RunID: id, Binding: budget.RunBudget{ParentRunID: parent}})
		return nil
	}
	if err = visit(runID); err != nil {
		return scope, err
	}
	return scope, nil
}

func (authority *BudgetAuthority) bindRun(ctx context.Context, runID string) (budget.Ledger, error) {
	scope, err := authority.resolveScope(ctx, runID)
	if err != nil || scope.LedgerID == "" {
		return budget.Ledger{}, err
	}
	coordinator := authority.coordinator()
	ledger, err := coordinator.Ensure(ctx, scope.LedgerID, scope.Limits)
	if err != nil {
		return ledger, err
	}
	for _, run := range scope.Runs {
		if original, exists := ledger.Runs[run.RunID]; exists {
			if original != run.Binding {
				return ledger, budget.ErrLedgerConflict
			}
			continue
		}
		ledger, err = coordinator.RegisterRun(ctx, ledger.ID, run.RunID, run.Binding)
		if err != nil {
			return ledger, err
		}
	}
	if _, exists := ledger.Runs[runID]; !exists {
		return ledger, ErrConflict
	}
	return ledger, nil
}

func (authority *BudgetAuthority) modelReservation(ctx context.Context, ledger budget.Ledger, request model.Request) (model.Request, budget.Reservation, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return request, budget.Reservation{}, err
	}
	reservation := budget.Reservation{ID: "model:" + request.InvocationID, RunID: request.RunID, RequestHash: hashInvocationBytes(raw), Requested: budget.Usage{LLMCalls: 1}}
	if existing, found := ledger.Reservations[reservation.ID]; found {
		if existing.RequestHash != reservation.RequestHash {
			return request, reservation, budget.ErrLedgerConflict
		}
		// Count-only reservations have no output-token allowance. They must
		// not erase a positive ceiling already frozen in the model intent.
		// A token reservation can only narrow that original ceiling.
		if ceiling := existing.Requested.OutputTokens; ceiling > 0 &&
			(request.MaxOutputTokens == 0 || ceiling < request.MaxOutputTokens) {
			request.MaxOutputTokens = ceiling
		}
		request = applyHostedReservation(request, existing.Requested.HostedToolCalls, ledger, existing.RunID)
		if existing.Status != budget.ReservationSettled && !existing.Dispatched && hasRunTokenLimits(ledger, request.RunID) {
			if authority.dependencies.Meter == nil {
				return request, existing, budget.ErrUnmetered
			}
			admission, admissionErr := authority.dependencies.Meter.PlanTokenAdmission(ctx, model.CloneRequest(request))
			if admissionErr != nil {
				return request, existing, admissionErr
			}
			if admission.InputUpperBound <= 0 || admission.InputUpperBound > existing.Requested.InputTokens || admission.MaxOutputTokens <= 0 {
				return request, existing, budget.ErrUnmetered
			}
		}
		return request, existing, nil
	}
	request, reservation = reserveHostedModelTools(request, reservation, ledger)
	if !hasRunTokenLimits(ledger, request.RunID) {
		return request, reservation, nil
	}
	if authority.dependencies.Meter == nil {
		return request, reservation, budget.ErrUnmetered
	}
	admission, err := authority.dependencies.Meter.PlanTokenAdmission(ctx, model.CloneRequest(request))
	if err != nil {
		return request, reservation, err
	}
	if admission.InputUpperBound <= 0 || admission.MaxOutputTokens <= 0 {
		return request, reservation, budget.ErrUnmetered
	}
	output := remainingOutputCeiling(ledger, request.RunID, admission.InputUpperBound, admission.MaxOutputTokens)
	if request.MaxOutputTokens > 0 {
		output = min(output, request.MaxOutputTokens)
	}
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

// VerifiedToolOutcomeVerifier composes an already trusted, read-only Tool
// verifier with the SAME durable Tool reservation used by the native Adapter.
// Reconciliation must not re-enter the Adapter (which can execute effects),
// but it must charge a confirmed COMMIT exactly once. The budget settlement
// can precede the Agent Kernel receipt CAS: a failed second commit retains
// the pending Tool intent, and repeating this exact verification is idempotent.
func (authority *BudgetAuthority) VerifiedToolOutcomeVerifier(
	verifier agent.ToolOutcomeVerifier,
) (agent.ToolOutcomeVerifier, error) {
	if authority == nil || verifier == nil {
		return nil, ErrInvalidRequest
	}
	return agent.ToolOutcomeVerifierFunc(func(ctx context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
		verified, err := verifier.VerifyCommittedTool(ctx, tools.CloneExecutionRequest(request))
		if err != nil {
			return tools.ExecutionResult{}, err
		}
		if tools.ValidateExecutionResult(verified) != nil ||
			verified.Receipt.ExecutionID != request.Call.ID ||
			verified.Receipt.Disposition != "committed" || verified.Pending != nil {
			return tools.ExecutionResult{}, agent.ErrToolReceiptMismatch
		}
		ledger, err := authority.bindRun(ctx, request.RunID)
		if err != nil {
			return tools.ExecutionResult{}, err
		}
		if ledger.ID == "" {
			return verified, nil // No shared ledger was configured by this Host.
		}
		rawRequest, err := json.Marshal(request)
		if err != nil {
			return tools.ExecutionResult{}, err
		}
		id := "tool:" + request.RunID + ":" + request.Call.ID
		current, found := ledger.Reservations[id]
		if !found || current.RunID != request.RunID ||
			current.RequestHash != hashInvocationBytes(rawRequest) ||
			!current.Dispatched ||
			(current.Status != budget.ReservationHeld && current.Status != budget.ReservationSettled) {
			return tools.ExecutionResult{}, budget.ErrLedgerConflict
		}
		receipt, err := json.Marshal(budgetToolReceipt{Result: verified})
		if err != nil {
			return tools.ExecutionResult{}, err
		}
		// Identical verified receipts are idempotent across Agent CAS loss.
		accountingCtx := context.WithoutCancel(ctx)
		updated, err := authority.coordinator().Settle(accountingCtx, ledger.ID, id,
			budget.Usage{ToolCalls: 1}, receipt)
		if err != nil {
			return tools.ExecutionResult{}, err
		}
		if err = authority.project(accountingCtx, updated); err != nil {
			return tools.ExecutionResult{}, err
		}
		return verified, nil
	}), nil
}

type budgetToolReceipt struct {
	Result       tools.ExecutionResult `json:"result"`
	ErrorCode    string                `json:"errorCode,omitempty"`
	ErrorMessage string                `json:"errorMessage,omitempty"`
	BlockedTools []string              `json:"blockedTools,omitempty"`
}

func (authority *BudgetAuthority) project(ctx context.Context, ledger budget.Ledger) error {
	if ledger.ID == "" {
		return nil
	}
	return projectBudgetItem(ctx, authority.dependencies.Store, authority.dependencies.TurnFeed, authority.dependencies.Clock, ledger)
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

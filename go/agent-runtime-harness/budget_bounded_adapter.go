package harness

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// NewBoundedBudgetAdapter explicitly delegates Run/Model orchestration to
// Runtime. Host configuration, routing, accounting storage and projection
// remain Host-owned. The ordinary BudgetMiddleware constructor is unchanged.
func NewBoundedBudgetAdapter(dependencies BudgetMiddlewareDependencies) (*agent.BoundedBudgetAdapter, error) {
	shared, err := NewBudgetMiddleware(dependencies)
	if err != nil {
		return nil, err
	}
	return agent.NewBoundedBudgetAdapter(shared.dependencies.Ledgers, boundedBudgetPorts{shared: shared})
}

type boundedBudgetPorts struct{ shared *BudgetMiddleware }

func (p boundedBudgetPorts) ResolveScope(ctx context.Context, runID string) (agent.BoundedBudgetScope, error) {
	d := p.shared.dependencies
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
func (p boundedBudgetPorts) Project(ctx context.Context, ledger budget.Ledger) error {
	return p.shared.project(ctx, ledger)
}
func (p boundedBudgetPorts) PlanModel(ctx context.Context, l budget.Ledger, r model.Request) (model.Request, budget.Reservation, error) {
	return p.shared.modelReservation(ctx, l, r)
}
func (p boundedBudgetPorts) ApplyLocalToolBudget(r model.Request, l budget.Ledger) model.Request {
	return applyLocalToolBudget(r, l, r.RunID)
}
func (p boundedBudgetPorts) RequiresTokenUsage(l budget.Ledger, id string) bool {
	return hasRunTokenLimits(l, id)
}
func (p boundedBudgetPorts) DecodeReceipt(raw json.RawMessage) (model.Response, error) {
	return decodeBudgetModelReceipt(raw)
}
func (p boundedBudgetPorts) PlanSettlement(l budget.Ledger, res budget.Reservation, response model.Response, callErr error) (agent.BoundedBudgetSettlement, error) {
	var tokens *budget.TokenUsage
	if response.Usage != nil {
		u := response.Usage
		tokens = &budget.TokenUsage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheReadTokens: u.CacheReadTokens, CacheWriteTokens: u.CacheWriteTokens, ReasoningTokens: u.ReasoningTokens}
	}
	usage, err := budget.ChargeModelCall(budget.Usage{}, tokens)
	if err != nil {
		return agent.BoundedBudgetSettlement{}, err
	}
	hosted, related, err := hostedReceiptTransfers(l, res.RunID, response.HostedToolCalls)
	if err != nil {
		return agent.BoundedBudgetSettlement{SuspendOnError: true}, err
	}
	usage.HostedToolCalls = hosted
	usage.ToolCalls = hosted
	if model.IsResponseRejectedError(callErr) && response.Usage != nil {
		response = model.Response{ResponseID: response.ResponseID, Usage: model.CloneUsage(response.Usage)}
	}
	raw, err := encodeBudgetModelReceipt(response, callErr)
	if err != nil {
		return agent.BoundedBudgetSettlement{}, err
	}
	return agent.BoundedBudgetSettlement{Response: response, Usage: usage, Receipt: raw, Related: related}, nil
}
func (p boundedBudgetPorts) ValidateSettlement(before, after budget.Ledger, res budget.Reservation, usage budget.Usage) error {
	if hasRunTokenLimits(before, res.RunID) && (usage.InputTokens > res.Requested.InputTokens || usage.OutputTokens > res.Requested.OutputTokens) {
		return budget.ErrInvalidUsage
	}
	_, limited := remainingHostedCalls(before, res.RunID)
	if limited && usage.HostedToolCalls+pendingHostedAllowance(after, res.RunID) > res.Requested.HostedToolCalls+pendingHostedAllowance(before, res.RunID) {
		return budget.ErrInvalidUsage
	}
	return nil
}

// Coordinator Tools wait for children and must not occupy the child's slot.
func (p boundedBudgetPorts) ToolReservationActive(invocation plugin.ToolInvocation) bool {
	return invocation.Definition.Key != DelegationToolKey && invocation.Definition.Key != CapabilityInvocationToolKey
}

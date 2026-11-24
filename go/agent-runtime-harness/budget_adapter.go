package harness

import (
	"context"
	"encoding/json"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// Adapter composes one native, trusted execution/settlement path around this
// Host-owned BudgetAuthority. Host code installs the same native adapter in
// each selected Run/Model/Tool middleware slot; the authority cannot dispatch.
func (authority *BudgetAuthority) Adapter() (*agent.BoundedBudgetAdapter, error) {
	if authority == nil {
		return nil, ErrInvalidRequest
	}
	// A single Host authority has one native execution driver. Its Run, Model
	// and Tool callbacks must share original ticket identity across layers.
	authority.adapterOnce.Do(func() {
		authority.adapter, authority.adapterErr = agent.NewBoundedBudgetAdapter(authority.dependencies.Ledgers, boundedBudgetPorts{authority: authority})
	})
	return authority.adapter, authority.adapterErr
}

type boundedBudgetPorts struct{ authority *BudgetAuthority }

func (p boundedBudgetPorts) ResolveScope(ctx context.Context, runID string) (agent.BoundedBudgetScope, error) {
	return p.authority.resolveScope(ctx, runID)
}
func (p boundedBudgetPorts) Project(ctx context.Context, ledger budget.Ledger) error {
	return p.authority.project(ctx, ledger)
}
func (p boundedBudgetPorts) PlanModel(ctx context.Context, l budget.Ledger, r model.Request) (model.Request, budget.Reservation, error) {
	return p.authority.modelReservation(ctx, l, r)
}
func (p boundedBudgetPorts) PreviewModel(l budget.Ledger, r model.Request) (model.Request, budget.Reservation, error) {
	return p.authority.previewModelReservation(l, r)
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
	return invocation.Definition.Key != DelegationToolKey
}

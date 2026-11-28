package harness

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
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

// Managed continuation uses the original Host selection and the native
// Runtime accounting store, never a Host-owned accounting history.
func (p boundedBudgetPorts) ContextSummaryRecoveryHost() kernel.ManagedRecoveryHostStore {
	d := p.authority.dependencies
	host, ok := d.Store.(kernel.ManagedRecoveryHostStore)
	if !ok || d.Runtime == nil || !d.Runtime.UsesLedgerStore(d.Ledgers) {
		return nil
	}
	return host
}

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

// Coordinator Tools wait for children and must not occupy the child's slot.
func (p boundedBudgetPorts) ToolReservationActive(invocation plugin.ToolInvocation) bool {
	return invocation.Definition.Key != DelegationToolKey
}

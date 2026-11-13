package conformance

import (
	"context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Keep fault wrappers transparent to the Root protocol upgrade. They still
// intercept only their originally named failure port; no fallback receipt.
func retirementPort(s kernel.Store) (kernel.OwnedRetirementStore, error) {
	p, ok := s.(kernel.OwnedRetirementStore)
	if !ok {
		return nil, kernel.ErrExecutionRequired
	}
	return p, nil
}

func (s *ea1AgentReceiptFault) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	p, e := retirementPort(s.Store)
	if e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	return p.LoadOwnedRetirement(ctx, id)
}
func (s *ea1AgentReceiptFault) AcknowledgeOwnedRetirement(ctx context.Context, g kernel.ChildExecutionGrant, a kernel.OwnedRetirementAcknowledgement) (kernel.OwnedRetirementAckResult, error) {
	p, e := retirementPort(s.Store)
	if e != nil {
		return kernel.OwnedRetirementAckResult{}, e
	}
	return p.AcknowledgeOwnedRetirement(ctx, g, a)
}

func (s *ea1AgentAdmissionStore) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	p, e := retirementPort(s.Store)
	if e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	return p.LoadOwnedRetirement(ctx, id)
}
func (s *ea1AgentAdmissionStore) AcknowledgeOwnedRetirement(ctx context.Context, g kernel.ChildExecutionGrant, a kernel.OwnedRetirementAcknowledgement) (kernel.OwnedRetirementAckResult, error) {
	p, e := retirementPort(s.Store)
	if e != nil {
		return kernel.OwnedRetirementAckResult{}, e
	}
	return p.AcknowledgeOwnedRetirement(ctx, g, a)
}

func (s *ea1StartSettlementStore) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	p, e := retirementPort(s.Store)
	if e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	return p.LoadOwnedRetirement(ctx, id)
}
func (s *ea1StartSettlementStore) AcknowledgeOwnedRetirement(ctx context.Context, g kernel.ChildExecutionGrant, a kernel.OwnedRetirementAcknowledgement) (kernel.OwnedRetirementAckResult, error) {
	p, e := retirementPort(s.Store)
	if e != nil {
		return kernel.OwnedRetirementAckResult{}, e
	}
	return p.AcknowledgeOwnedRetirement(ctx, g, a)
}

func (s *ea1OwnedRecoveryCapture) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	p, e := retirementPort(s.Store)
	if e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	return p.LoadOwnedRetirement(ctx, id)
}
func (s *ea1OwnedRecoveryCapture) AcknowledgeOwnedRetirement(ctx context.Context, g kernel.ChildExecutionGrant, a kernel.OwnedRetirementAcknowledgement) (kernel.OwnedRetirementAckResult, error) {
	p, e := retirementPort(s.Store)
	if e != nil {
		return kernel.OwnedRetirementAckResult{}, e
	}
	return p.AcknowledgeOwnedRetirement(ctx, g, a)
}

func (s *ea1AgentReceiptFault) LookupOwnedRetirementForRun(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	p, e := retirementPort(s.Store)
	if e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	return p.LookupOwnedRetirementForRun(ctx, id)
}

func (s *ea1AgentAdmissionStore) LookupOwnedRetirementForRun(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	p, e := retirementPort(s.Store)
	if e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	return p.LookupOwnedRetirementForRun(ctx, id)
}

func (s *ea1StartSettlementStore) LookupOwnedRetirementForRun(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	p, e := retirementPort(s.Store)
	if e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	return p.LookupOwnedRetirementForRun(ctx, id)
}

func (s *ea1OwnedRecoveryCapture) LookupOwnedRetirementForRun(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	p, e := retirementPort(s.Store)
	if e != nil {
		return kernel.OwnedRetirementReceipt{}, e
	}
	return p.LookupOwnedRetirementForRun(ctx, id)
}

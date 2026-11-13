package harness

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func (s *receiptFaultStore) AcquireExecution(ctx context.Context, q kernel.ExecutionAcquireRequest) (kernel.ExecutionLease, error) {
	return s.Store.(kernel.ExecutionStore).AcquireExecution(ctx, q)
}
func (s *receiptFaultStore) RenewExecution(ctx context.Context, q kernel.ExecutionRenewRequest) (kernel.ExecutionLease, error) {
	return s.Store.(kernel.ExecutionStore).RenewExecution(ctx, q)
}
func (s *receiptFaultStore) ReleaseExecution(ctx context.Context, l kernel.ExecutionLease) error {
	return s.Store.(kernel.ExecutionStore).ReleaseExecution(ctx, l)
}
func (s *receiptFaultStore) CheckExecution(ctx context.Context, id string, l *kernel.ExecutionLease) error {
	return s.Store.(kernel.ExecutionStore).CheckExecution(ctx, id, l)
}
func (s *receiptFaultStore) ApplyExecution(ctx context.Context, l kernel.ExecutionLease, r uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {

	for _, event := range m.Events {
		if !s.fired && event.Type == "agent.model_invocation.completed" {
			s.fired = true
			if s.afterCommit {
				if _, err := s.Store.(kernel.ExecutionStore).ApplyExecution(ctx, l, r, m); err != nil {
					return kernel.Snapshot{}, err
				}
			}
			return kernel.Snapshot{}, errBudgetResponseRejected
		}
	}
	return s.Store.(kernel.ExecutionStore).ApplyExecution(ctx, l, r, m)

}
func (s *receiptFaultStore) ApplyControl(ctx context.Context, id string, r uint64, g kernel.ControlGuard, m kernel.StoreMutation) (kernel.Snapshot, bool, error) {
	return s.Store.(kernel.ControlStore).ApplyControl(ctx, id, r, g, m)
}
func (s *receiptFaultStore) BeginChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	return s.Store.(kernel.ChildExecutionStore).BeginChildExecution(ctx, g)
}
func (s *receiptFaultStore) LookupChildExecution(ctx context.Context, id string) (kernel.ChildExecutionGrant, error) {
	return s.Store.(kernel.ChildExecutionStore).LookupChildExecution(ctx, id)
}
func (s *receiptFaultStore) MarkChildExecutionSettled(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	return s.Store.(kernel.ChildExecutionStore).MarkChildExecutionSettled(ctx, g, p)
}
func (s *receiptFaultStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	return s.Store.(kernel.ChildExecutionStore).FinishChildExecution(ctx, g)
}
func (s *receiptFaultStore) BeginOwnedPhysicalExecution(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, p kernel.OwnedPhysicalAdmission) error {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).BeginOwnedPhysicalExecution(ctx, g, l, p)
}
func (s *receiptFaultStore) ApplyOwnedPhysicalRecovery(ctx context.Context, g kernel.ChildExecutionGrant, r uint64, m kernel.StoreMutation, p kernel.OwnedPhysicalRecovery) (kernel.Snapshot, error) {

	for _, event := range m.Events {
		if !s.fired && event.Type == "agent.model_invocation.completed" {
			s.fired = true
			if s.afterCommit {
				if _, err := s.Store.(kernel.OwnedPhysicalExecutionStore).ApplyOwnedPhysicalRecovery(ctx, g, r, m, p); err != nil {
					return kernel.Snapshot{}, err
				}
			}
			return kernel.Snapshot{}, errBudgetResponseRejected
		}
	}
	return s.Store.(kernel.OwnedPhysicalExecutionStore).ApplyOwnedPhysicalRecovery(ctx, g, r, m, p)

}
func (s *receiptFaultStore) RecordOwnedPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, r uint64, o kernel.OwnedPhysicalCallbackOutcome, p kernel.OwnedPhysicalCallbackReturn) error {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).RecordOwnedPhysicalCallbackReturn(ctx, g, r, o, p)
}
func (s *receiptFaultStore) LookupOwnedPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant) (kernel.OwnedPhysicalCallbackCompletion, error) {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).LookupOwnedPhysicalCallbackReturn(ctx, g)
}
func (s *receiptFaultStore) QuarantineExecution(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).QuarantineExecution(ctx, q)
}
func (s *receiptFaultStore) RepairExecutionQuarantine(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).RepairExecutionQuarantine(ctx, q)
}

// Preserve the backend's atomic retirement authority through this fault decorator.
func (s *receiptFaultStore) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.OwnedRetirementStore).LoadOwnedRetirement(ctx, id)
}
func (s *receiptFaultStore) AcknowledgeOwnedRetirement(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.OwnedRetirementAcknowledgement) (kernel.OwnedRetirementAckResult, error) {
	return s.Store.(kernel.OwnedRetirementStore).AcknowledgeOwnedRetirement(ctx, g, p)
}

func (s *receiptFaultStore) LookupOwnedRetirementForRun(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.OwnedRetirementStore).LookupOwnedRetirementForRun(ctx, id)
}

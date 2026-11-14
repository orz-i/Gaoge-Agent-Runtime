package continuation_test

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Test fault decorators preserve native authority ports and intercept the actual
// fenced or receipt-recovery mutation, not an unused legacy Apply path.
func (s *claimantAfterCreateStore) AcquireExecution(ctx context.Context, q kernel.ExecutionAcquireRequest) (kernel.ExecutionLease, error) {
	return s.Store.(kernel.ExecutionStore).AcquireExecution(ctx, q)
}
func (s *claimantAfterCreateStore) RenewExecution(ctx context.Context, q kernel.ExecutionRenewRequest) (kernel.ExecutionLease, error) {
	return s.Store.(kernel.ExecutionStore).RenewExecution(ctx, q)
}
func (s *claimantAfterCreateStore) ReleaseExecution(ctx context.Context, l kernel.ExecutionLease) error {
	return s.Store.(kernel.ExecutionStore).ReleaseExecution(ctx, l)
}
func (s *claimantAfterCreateStore) CheckExecution(ctx context.Context, id string, l *kernel.ExecutionLease) error {
	return s.Store.(kernel.ExecutionStore).CheckExecution(ctx, id, l)
}
func (s *claimantAfterCreateStore) ApplyExecution(ctx context.Context, l kernel.ExecutionLease, r uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	return s.Store.(kernel.ExecutionStore).ApplyExecution(ctx, l, r, m)
}
func (s *claimantAfterCreateStore) ApplyControl(ctx context.Context, id string, r uint64, g kernel.ControlGuard, m kernel.StoreMutation) (kernel.Snapshot, bool, error) {
	return s.Store.(kernel.ControlStore).ApplyControl(ctx, id, r, g, m)
}
func (s *claimantAfterCreateStore) BeginChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	return s.Store.(kernel.ChildExecutionStore).BeginChildExecution(ctx, g)
}
func (s *claimantAfterCreateStore) LookupChildExecution(ctx context.Context, id string) (kernel.ChildExecutionGrant, error) {
	return s.Store.(kernel.ChildExecutionStore).LookupChildExecution(ctx, id)
}
func (s *claimantAfterCreateStore) MarkChildExecutionSettled(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	return s.Store.(kernel.ChildExecutionStore).MarkChildExecutionSettled(ctx, g, p)
}
func (s *claimantAfterCreateStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	return s.Store.(kernel.ChildExecutionStore).FinishChildExecution(ctx, g)
}
func (s *claimantAfterCreateStore) BeginOwnedPhysicalExecution(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, p kernel.OwnedPhysicalAdmission) error {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).BeginOwnedPhysicalExecution(ctx, g, l, p)
}
func (s *claimantAfterCreateStore) ApplyOwnedPhysicalRecovery(ctx context.Context, g kernel.ChildExecutionGrant, r uint64, m kernel.StoreMutation, p kernel.OwnedPhysicalRecovery) (kernel.Snapshot, error) {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).ApplyOwnedPhysicalRecovery(ctx, g, r, m, p)
}
func (s *claimantAfterCreateStore) RecordOwnedPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, r uint64, o kernel.OwnedPhysicalCallbackOutcome, p kernel.OwnedPhysicalCallbackReturn) error {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).RecordOwnedPhysicalCallbackReturn(ctx, g, r, o, p)
}
func (s *claimantAfterCreateStore) LookupOwnedPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant) (kernel.OwnedPhysicalCallbackCompletion, error) {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).LookupOwnedPhysicalCallbackReturn(ctx, g)
}
func (s *claimantAfterCreateStore) QuarantineExecution(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).QuarantineExecution(ctx, q)
}
func (s *claimantAfterCreateStore) RepairExecutionQuarantine(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).RepairExecutionQuarantine(ctx, q)
}

var _ kernel.OwnedRetirementStore = (*claimantAfterCreateStore)(nil)

func (s *claimantAfterCreateStore) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.OwnedRetirementStore).LoadOwnedRetirement(ctx, id)
}
func (s *claimantAfterCreateStore) LookupOwnedRetirementForRun(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.OwnedRetirementStore).LookupOwnedRetirementForRun(ctx, id)
}
func (s *claimantAfterCreateStore) AcknowledgeOwnedRetirement(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.OwnedRetirementAcknowledgement) (kernel.OwnedRetirementAckResult, error) {
	return s.Store.(kernel.OwnedRetirementStore).AcknowledgeOwnedRetirement(ctx, g, p)
}

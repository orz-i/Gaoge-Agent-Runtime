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

func (s *claimantAfterCreateStore) LookupChildExecution(ctx context.Context, id string) (kernel.ChildExecutionGrant, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupChildExecution(ctx, id)
}

func (s *claimantAfterCreateStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	return s.Store.(kernel.PhysicalExecutionStore).FinishChildExecution(ctx, g, p)
}
func (s *claimantAfterCreateStore) BeginPhysicalExecution(ctx context.Context, p kernel.PhysicalAdmission) (kernel.PhysicalAdmissionResult, error) {
	return s.Store.(kernel.PhysicalExecutionStore).BeginPhysicalExecution(ctx, p)
}
func (s *claimantAfterCreateStore) CheckOwnedPhysicalEntry(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, source kernel.Snapshot) (kernel.OwnedPhysicalEntryWindow, error) {
	return s.Store.(kernel.PhysicalExecutionStore).CheckOwnedPhysicalEntry(ctx, g, l, source)
}

func (s *claimantAfterCreateStore) ApplyOwnedPhysicalRecovery(ctx context.Context, g kernel.ChildExecutionGrant, r uint64, m kernel.StoreMutation, p kernel.OwnedPhysicalRecovery) (kernel.Snapshot, error) {
	return s.Store.(kernel.PhysicalExecutionStore).ApplyOwnedPhysicalRecovery(ctx, g, r, m, p)
}
func (s *claimantAfterCreateStore) RecordPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, r uint64, o kernel.PhysicalCallbackOutcome, p kernel.PhysicalCallbackReturn) error {
	return s.Store.(kernel.PhysicalExecutionStore).RecordPhysicalCallbackReturn(ctx, g, r, o, p)
}
func (s *claimantAfterCreateStore) LookupPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant) (kernel.PhysicalCallbackCompletion, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupPhysicalCallbackReturn(ctx, g)
}
func (s *claimantAfterCreateStore) QuarantineExecution(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).QuarantineExecution(ctx, q)
}
func (s *claimantAfterCreateStore) RepairExecutionQuarantine(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).RepairExecutionQuarantine(ctx, q)
}

func (s *claimantAfterCreateStore) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LoadOwnedRetirement(ctx, id)
}
func (s *claimantAfterCreateStore) LookupOwnedRetirementForRun(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupOwnedRetirementForRun(ctx, id)
}

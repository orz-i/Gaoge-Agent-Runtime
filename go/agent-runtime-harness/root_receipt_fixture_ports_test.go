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

func (s *receiptFaultStore) LookupChildExecution(ctx context.Context, id string) (kernel.ChildExecutionGrant, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupChildExecution(ctx, id)
}

func (s *receiptFaultStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	return s.Store.(kernel.PhysicalExecutionStore).FinishChildExecution(ctx, g, p)
}
func (s *receiptFaultStore) BeginPhysicalExecution(ctx context.Context, p kernel.PhysicalAdmission) (kernel.PhysicalAdmissionResult, error) {
	return s.Store.(kernel.PhysicalExecutionStore).BeginPhysicalExecution(ctx, p)
}
func (s *receiptFaultStore) CheckOwnedPhysicalEntry(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, source kernel.Snapshot) (kernel.OwnedPhysicalEntryWindow, error) {
	return s.Store.(kernel.PhysicalExecutionStore).CheckOwnedPhysicalEntry(ctx, g, l, source)
}

func (s *receiptFaultStore) RecordPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, r uint64, o kernel.PhysicalCallbackOutcome, p kernel.PhysicalCallbackReturn) error {
	return s.Store.(kernel.PhysicalExecutionStore).RecordPhysicalCallbackReturn(ctx, g, r, o, p)
}
func (s *receiptFaultStore) LookupPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant) (kernel.PhysicalCallbackCompletion, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupPhysicalCallbackReturn(ctx, g)
}
func (s *receiptFaultStore) QuarantineExecution(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).QuarantineExecution(ctx, q)
}
func (s *receiptFaultStore) RepairExecutionQuarantine(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).RepairExecutionQuarantine(ctx, q)
}

// Preserve the backend's atomic retirement authority through this fault decorator.
func (s *receiptFaultStore) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LoadOwnedRetirement(ctx, id)
}

func (s *receiptFaultStore) LookupOwnedRetirementForRun(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupOwnedRetirementForRun(ctx, id)
}

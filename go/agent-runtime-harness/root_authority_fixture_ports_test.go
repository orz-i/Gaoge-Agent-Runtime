package harness_test

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Test fault decorators preserve native authority ports and intercept the actual
// fenced or receipt-recovery mutation, not an unused legacy Apply path.
func (s *resumeTurnFaultStore) AcquireExecution(ctx context.Context, q kernel.ExecutionAcquireRequest) (kernel.ExecutionLease, error) {
	return s.Store.(kernel.ExecutionStore).AcquireExecution(ctx, q)
}
func (s *resumeTurnFaultStore) RenewExecution(ctx context.Context, q kernel.ExecutionRenewRequest) (kernel.ExecutionLease, error) {
	return s.Store.(kernel.ExecutionStore).RenewExecution(ctx, q)
}
func (s *resumeTurnFaultStore) ReleaseExecution(ctx context.Context, l kernel.ExecutionLease) error {
	return s.Store.(kernel.ExecutionStore).ReleaseExecution(ctx, l)
}
func (s *resumeTurnFaultStore) CheckExecution(ctx context.Context, id string, l *kernel.ExecutionLease) error {
	return s.Store.(kernel.ExecutionStore).CheckExecution(ctx, id, l)
}

func (s *resumeTurnFaultStore) LookupChildExecution(ctx context.Context, id string) (kernel.ChildExecutionGrant, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupChildExecution(ctx, id)
}

func (s *resumeTurnFaultStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	return s.Store.(kernel.PhysicalExecutionStore).FinishChildExecution(ctx, g, p)
}
func (s *resumeTurnFaultStore) BeginPhysicalExecution(ctx context.Context, p kernel.PhysicalAdmission) (kernel.PhysicalAdmissionResult, error) {
	return s.Store.(kernel.PhysicalExecutionStore).BeginPhysicalExecution(ctx, p)
}
func (s *resumeTurnFaultStore) CheckOwnedPhysicalEntry(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, source kernel.Snapshot) (kernel.OwnedPhysicalEntryWindow, error) {
	return s.Store.(kernel.PhysicalExecutionStore).CheckOwnedPhysicalEntry(ctx, g, l, source)
}

func (s *resumeTurnFaultStore) RecordPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, r uint64, o kernel.PhysicalCallbackOutcome, p kernel.PhysicalCallbackReturn) error {
	return s.Store.(kernel.PhysicalExecutionStore).RecordPhysicalCallbackReturn(ctx, g, r, o, p)
}
func (s *resumeTurnFaultStore) LookupPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant) (kernel.PhysicalCallbackCompletion, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupPhysicalCallbackReturn(ctx, g)
}
func (s *resumeTurnFaultStore) QuarantineExecution(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).QuarantineExecution(ctx, q)
}
func (s *resumeTurnFaultStore) RepairExecutionQuarantine(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).RepairExecutionQuarantine(ctx, q)
}
func (s *preDispatchReadFaultStore) AcquireExecution(ctx context.Context, q kernel.ExecutionAcquireRequest) (kernel.ExecutionLease, error) {
	return s.Store.(kernel.ExecutionStore).AcquireExecution(ctx, q)
}
func (s *preDispatchReadFaultStore) RenewExecution(ctx context.Context, q kernel.ExecutionRenewRequest) (kernel.ExecutionLease, error) {
	return s.Store.(kernel.ExecutionStore).RenewExecution(ctx, q)
}
func (s *preDispatchReadFaultStore) ReleaseExecution(ctx context.Context, l kernel.ExecutionLease) error {
	return s.Store.(kernel.ExecutionStore).ReleaseExecution(ctx, l)
}
func (s *preDispatchReadFaultStore) CheckExecution(ctx context.Context, id string, l *kernel.ExecutionLease) error {
	return s.Store.(kernel.ExecutionStore).CheckExecution(ctx, id, l)
}

func (s *preDispatchReadFaultStore) LookupChildExecution(ctx context.Context, id string) (kernel.ChildExecutionGrant, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupChildExecution(ctx, id)
}

func (s *preDispatchReadFaultStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	return s.Store.(kernel.PhysicalExecutionStore).FinishChildExecution(ctx, g, p)
}
func (s *preDispatchReadFaultStore) BeginPhysicalExecution(ctx context.Context, p kernel.PhysicalAdmission) (kernel.PhysicalAdmissionResult, error) {
	return s.Store.(kernel.PhysicalExecutionStore).BeginPhysicalExecution(ctx, p)
}
func (s *preDispatchReadFaultStore) CheckOwnedPhysicalEntry(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, source kernel.Snapshot) (kernel.OwnedPhysicalEntryWindow, error) {
	return s.Store.(kernel.PhysicalExecutionStore).CheckOwnedPhysicalEntry(ctx, g, l, source)
}

func (s *preDispatchReadFaultStore) RecordPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, r uint64, o kernel.PhysicalCallbackOutcome, p kernel.PhysicalCallbackReturn) error {
	return s.Store.(kernel.PhysicalExecutionStore).RecordPhysicalCallbackReturn(ctx, g, r, o, p)
}
func (s *preDispatchReadFaultStore) LookupPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant) (kernel.PhysicalCallbackCompletion, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupPhysicalCallbackReturn(ctx, g)
}
func (s *preDispatchReadFaultStore) QuarantineExecution(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).QuarantineExecution(ctx, q)
}
func (s *preDispatchReadFaultStore) RepairExecutionQuarantine(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).RepairExecutionQuarantine(ctx, q)
}
func (s *beforeModelIntentStore) AcquireExecution(ctx context.Context, q kernel.ExecutionAcquireRequest) (kernel.ExecutionLease, error) {
	return s.Store.(kernel.ExecutionStore).AcquireExecution(ctx, q)
}
func (s *beforeModelIntentStore) RenewExecution(ctx context.Context, q kernel.ExecutionRenewRequest) (kernel.ExecutionLease, error) {
	return s.Store.(kernel.ExecutionStore).RenewExecution(ctx, q)
}
func (s *beforeModelIntentStore) ReleaseExecution(ctx context.Context, l kernel.ExecutionLease) error {
	return s.Store.(kernel.ExecutionStore).ReleaseExecution(ctx, l)
}
func (s *beforeModelIntentStore) CheckExecution(ctx context.Context, id string, l *kernel.ExecutionLease) error {
	return s.Store.(kernel.ExecutionStore).CheckExecution(ctx, id, l)
}

func (s *beforeModelIntentStore) LookupChildExecution(ctx context.Context, id string) (kernel.ChildExecutionGrant, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupChildExecution(ctx, id)
}

func (s *beforeModelIntentStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	return s.Store.(kernel.PhysicalExecutionStore).FinishChildExecution(ctx, g, p)
}
func (s *beforeModelIntentStore) BeginPhysicalExecution(ctx context.Context, p kernel.PhysicalAdmission) (kernel.PhysicalAdmissionResult, error) {
	return s.Store.(kernel.PhysicalExecutionStore).BeginPhysicalExecution(ctx, p)
}
func (s *beforeModelIntentStore) CheckOwnedPhysicalEntry(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, source kernel.Snapshot) (kernel.OwnedPhysicalEntryWindow, error) {
	return s.Store.(kernel.PhysicalExecutionStore).CheckOwnedPhysicalEntry(ctx, g, l, source)
}

func (s *beforeModelIntentStore) RecordPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, r uint64, o kernel.PhysicalCallbackOutcome, p kernel.PhysicalCallbackReturn) error {
	return s.Store.(kernel.PhysicalExecutionStore).RecordPhysicalCallbackReturn(ctx, g, r, o, p)
}
func (s *beforeModelIntentStore) LookupPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant) (kernel.PhysicalCallbackCompletion, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupPhysicalCallbackReturn(ctx, g)
}
func (s *beforeModelIntentStore) QuarantineExecution(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).QuarantineExecution(ctx, q)
}
func (s *beforeModelIntentStore) RepairExecutionQuarantine(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).RepairExecutionQuarantine(ctx, q)
}

// Preserve the backend's atomic retirement authority through this fault decorator.
func (s *resumeTurnFaultStore) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LoadOwnedRetirement(ctx, id)
}

// Preserve the backend's atomic retirement authority through this fault decorator.
func (s *preDispatchReadFaultStore) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LoadOwnedRetirement(ctx, id)
}

// Preserve the backend's atomic retirement authority through this fault decorator.
func (s *beforeModelIntentStore) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LoadOwnedRetirement(ctx, id)
}

func (s *resumeTurnFaultStore) LookupOwnedRetirementForRun(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupOwnedRetirementForRun(ctx, id)
}

func (s *preDispatchReadFaultStore) LookupOwnedRetirementForRun(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupOwnedRetirementForRun(ctx, id)
}

func (s *beforeModelIntentStore) LookupOwnedRetirementForRun(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupOwnedRetirementForRun(ctx, id)
}

func (s *resumeTurnFaultStore) LoadBudget(ctx context.Context, id string) (budget.Ledger, error) {
	return s.Store.(budget.LedgerStore).LoadBudget(ctx, id)
}
func (s *resumeTurnFaultStore) SaveBudget(ctx context.Context, l budget.Ledger, rev uint64) error {
	return s.Store.(budget.LedgerStore).SaveBudget(ctx, l, rev)
}

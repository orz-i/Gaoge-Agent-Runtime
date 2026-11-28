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
func (s *resumeTurnFaultStore) ApplyExecution(ctx context.Context, l kernel.ExecutionLease, r uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {

	for _, event := range m.Events {
		if event.Type == "agent.model_invocation.pending" && s.failed.CompareAndSwap(false, true) {
			if _, err := s.Store.(kernel.ExecutionStore).ApplyExecution(ctx, l, r, m); err != nil {
				return kernel.Snapshot{}, err
			}
			return kernel.Snapshot{}, errResumeTurnInterrupted
		}
	}
	return s.Store.(kernel.ExecutionStore).ApplyExecution(ctx, l, r, m)

}
func (s *resumeTurnFaultStore) ApplyControl(ctx context.Context, id string, r uint64, g kernel.ControlGuard, m kernel.StoreMutation) (kernel.Snapshot, bool, error) {
	return s.Store.(kernel.ControlStore).ApplyControl(ctx, id, r, g, m)
}
func (s *resumeTurnFaultStore) BeginChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	return s.Store.(kernel.ChildExecutionStore).BeginChildExecution(ctx, g)
}
func (s *resumeTurnFaultStore) LookupChildExecution(ctx context.Context, id string) (kernel.ChildExecutionGrant, error) {
	return s.Store.(kernel.ChildExecutionStore).LookupChildExecution(ctx, id)
}

func (s *resumeTurnFaultStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	return s.Store.(kernel.ChildExecutionStore).FinishChildExecution(ctx, g, p)
}
func (s *resumeTurnFaultStore) BeginOwnedPhysicalExecution(ctx context.Context, p kernel.OwnedPhysicalAdmission) (kernel.OwnedPhysicalAdmissionResult, error) {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).BeginOwnedPhysicalExecution(ctx, p)
}
func (s *resumeTurnFaultStore) CheckOwnedPhysicalEntry(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, source kernel.Snapshot) (kernel.OwnedPhysicalEntryWindow, error) {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).CheckOwnedPhysicalEntry(ctx, g, l, source)
}

func (s *resumeTurnFaultStore) ApplyOwnedPhysicalRecovery(ctx context.Context, g kernel.ChildExecutionGrant, r uint64, m kernel.StoreMutation, p kernel.OwnedPhysicalRecovery) (kernel.Snapshot, error) {

	for _, event := range m.Events {
		if event.Type == "agent.model_invocation.pending" && s.failed.CompareAndSwap(false, true) {
			if _, err := s.Store.(kernel.OwnedPhysicalExecutionStore).ApplyOwnedPhysicalRecovery(ctx, g, r, m, p); err != nil {
				return kernel.Snapshot{}, err
			}
			return kernel.Snapshot{}, errResumeTurnInterrupted
		}
	}
	return s.Store.(kernel.OwnedPhysicalExecutionStore).ApplyOwnedPhysicalRecovery(ctx, g, r, m, p)

}
func (s *resumeTurnFaultStore) RecordPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, r uint64, o kernel.PhysicalCallbackOutcome, p kernel.PhysicalCallbackReturn) error {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).RecordPhysicalCallbackReturn(ctx, g, r, o, p)
}
func (s *resumeTurnFaultStore) LookupPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant) (kernel.PhysicalCallbackCompletion, error) {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).LookupPhysicalCallbackReturn(ctx, g)
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
func (s *preDispatchReadFaultStore) ApplyExecution(ctx context.Context, l kernel.ExecutionLease, r uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	return s.Store.(kernel.ExecutionStore).ApplyExecution(ctx, l, r, m)
}
func (s *preDispatchReadFaultStore) ApplyControl(ctx context.Context, id string, r uint64, g kernel.ControlGuard, m kernel.StoreMutation) (kernel.Snapshot, bool, error) {
	return s.Store.(kernel.ControlStore).ApplyControl(ctx, id, r, g, m)
}
func (s *preDispatchReadFaultStore) BeginChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	return s.Store.(kernel.ChildExecutionStore).BeginChildExecution(ctx, g)
}
func (s *preDispatchReadFaultStore) LookupChildExecution(ctx context.Context, id string) (kernel.ChildExecutionGrant, error) {
	return s.Store.(kernel.ChildExecutionStore).LookupChildExecution(ctx, id)
}

func (s *preDispatchReadFaultStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	return s.Store.(kernel.ChildExecutionStore).FinishChildExecution(ctx, g, p)
}
func (s *preDispatchReadFaultStore) BeginOwnedPhysicalExecution(ctx context.Context, p kernel.OwnedPhysicalAdmission) (kernel.OwnedPhysicalAdmissionResult, error) {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).BeginOwnedPhysicalExecution(ctx, p)
}
func (s *preDispatchReadFaultStore) CheckOwnedPhysicalEntry(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, source kernel.Snapshot) (kernel.OwnedPhysicalEntryWindow, error) {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).CheckOwnedPhysicalEntry(ctx, g, l, source)
}

func (s *preDispatchReadFaultStore) ApplyOwnedPhysicalRecovery(ctx context.Context, g kernel.ChildExecutionGrant, r uint64, m kernel.StoreMutation, p kernel.OwnedPhysicalRecovery) (kernel.Snapshot, error) {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).ApplyOwnedPhysicalRecovery(ctx, g, r, m, p)
}
func (s *preDispatchReadFaultStore) RecordPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, r uint64, o kernel.PhysicalCallbackOutcome, p kernel.PhysicalCallbackReturn) error {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).RecordPhysicalCallbackReturn(ctx, g, r, o, p)
}
func (s *preDispatchReadFaultStore) LookupPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant) (kernel.PhysicalCallbackCompletion, error) {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).LookupPhysicalCallbackReturn(ctx, g)
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
func (s *beforeModelIntentStore) ApplyExecution(ctx context.Context, l kernel.ExecutionLease, r uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {

	for _, event := range m.Events {
		if event.Type == "agent.model_invocation.pending" && s.failed.CompareAndSwap(false, true) {
			return kernel.Snapshot{}, errBeforeModelIntentCommit
		}
	}
	return s.Store.(kernel.ExecutionStore).ApplyExecution(ctx, l, r, m)

}
func (s *beforeModelIntentStore) ApplyControl(ctx context.Context, id string, r uint64, g kernel.ControlGuard, m kernel.StoreMutation) (kernel.Snapshot, bool, error) {
	return s.Store.(kernel.ControlStore).ApplyControl(ctx, id, r, g, m)
}
func (s *beforeModelIntentStore) BeginChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	return s.Store.(kernel.ChildExecutionStore).BeginChildExecution(ctx, g)
}
func (s *beforeModelIntentStore) LookupChildExecution(ctx context.Context, id string) (kernel.ChildExecutionGrant, error) {
	return s.Store.(kernel.ChildExecutionStore).LookupChildExecution(ctx, id)
}

func (s *beforeModelIntentStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	return s.Store.(kernel.ChildExecutionStore).FinishChildExecution(ctx, g, p)
}
func (s *beforeModelIntentStore) BeginOwnedPhysicalExecution(ctx context.Context, p kernel.OwnedPhysicalAdmission) (kernel.OwnedPhysicalAdmissionResult, error) {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).BeginOwnedPhysicalExecution(ctx, p)
}
func (s *beforeModelIntentStore) CheckOwnedPhysicalEntry(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, source kernel.Snapshot) (kernel.OwnedPhysicalEntryWindow, error) {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).CheckOwnedPhysicalEntry(ctx, g, l, source)
}

func (s *beforeModelIntentStore) ApplyOwnedPhysicalRecovery(ctx context.Context, g kernel.ChildExecutionGrant, r uint64, m kernel.StoreMutation, p kernel.OwnedPhysicalRecovery) (kernel.Snapshot, error) {

	for _, event := range m.Events {
		if event.Type == "agent.model_invocation.pending" && s.failed.CompareAndSwap(false, true) {
			return kernel.Snapshot{}, errBeforeModelIntentCommit
		}
	}
	return s.Store.(kernel.OwnedPhysicalExecutionStore).ApplyOwnedPhysicalRecovery(ctx, g, r, m, p)

}
func (s *beforeModelIntentStore) RecordPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, r uint64, o kernel.PhysicalCallbackOutcome, p kernel.PhysicalCallbackReturn) error {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).RecordPhysicalCallbackReturn(ctx, g, r, o, p)
}
func (s *beforeModelIntentStore) LookupPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant) (kernel.PhysicalCallbackCompletion, error) {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).LookupPhysicalCallbackReturn(ctx, g)
}
func (s *beforeModelIntentStore) QuarantineExecution(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).QuarantineExecution(ctx, q)
}
func (s *beforeModelIntentStore) RepairExecutionQuarantine(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).RepairExecutionQuarantine(ctx, q)
}

// Preserve the backend's atomic retirement authority through this fault decorator.
func (s *resumeTurnFaultStore) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.OwnedRetirementStore).LoadOwnedRetirement(ctx, id)
}

// Preserve the backend's atomic retirement authority through this fault decorator.
func (s *preDispatchReadFaultStore) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.OwnedRetirementStore).LoadOwnedRetirement(ctx, id)
}

// Preserve the backend's atomic retirement authority through this fault decorator.
func (s *beforeModelIntentStore) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.OwnedRetirementStore).LoadOwnedRetirement(ctx, id)
}

func (s *resumeTurnFaultStore) LookupOwnedRetirementForRun(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.OwnedRetirementStore).LookupOwnedRetirementForRun(ctx, id)
}

func (s *preDispatchReadFaultStore) LookupOwnedRetirementForRun(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.OwnedRetirementStore).LookupOwnedRetirementForRun(ctx, id)
}

func (s *beforeModelIntentStore) LookupOwnedRetirementForRun(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.OwnedRetirementStore).LookupOwnedRetirementForRun(ctx, id)
}

func (s *resumeTurnFaultStore) LoadBudget(ctx context.Context, id string) (budget.Ledger, error) {
	return s.Store.(budget.LedgerStore).LoadBudget(ctx, id)
}
func (s *resumeTurnFaultStore) SaveBudget(ctx context.Context, l budget.Ledger, rev uint64) error {
	return s.Store.(budget.LedgerStore).SaveBudget(ctx, l, rev)
}

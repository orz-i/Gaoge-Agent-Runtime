package agent_test

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Fault wrappers preserve the complete native capability set. The explicit
// legacyStoreView intentionally remains unsupported for fail-closed tests.
func (s *modelInvocationFaultStore) BeginPhysicalExecution(ctx context.Context, p kernel.PhysicalAdmission) (kernel.PhysicalAdmissionResult, error) {
	return s.Store.(kernel.PhysicalExecutionStore).BeginPhysicalExecution(ctx, p)
}
func (s *modelInvocationFaultStore) CheckOwnedPhysicalEntry(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, source kernel.Snapshot) (kernel.OwnedPhysicalEntryWindow, error) {
	return s.Store.(kernel.PhysicalExecutionStore).CheckOwnedPhysicalEntry(ctx, g, l, source)
}

func (s *modelInvocationFaultStore) LookupChildExecution(ctx context.Context, id string) (kernel.ChildExecutionGrant, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupChildExecution(ctx, id)
}

func (s *modelInvocationFaultStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	return s.Store.(kernel.PhysicalExecutionStore).FinishChildExecution(ctx, g, p)
}

func (s *modelInvocationFaultStore) QuarantineExecution(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).QuarantineExecution(ctx, q)
}
func (s *modelInvocationFaultStore) RepairExecutionQuarantine(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).RepairExecutionQuarantine(ctx, q)
}
func (s *lostCreateResponseStore) BeginPhysicalExecution(ctx context.Context, p kernel.PhysicalAdmission) (kernel.PhysicalAdmissionResult, error) {
	return s.Store.(kernel.PhysicalExecutionStore).BeginPhysicalExecution(ctx, p)
}
func (s *lostCreateResponseStore) CheckOwnedPhysicalEntry(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, source kernel.Snapshot) (kernel.OwnedPhysicalEntryWindow, error) {
	return s.Store.(kernel.PhysicalExecutionStore).CheckOwnedPhysicalEntry(ctx, g, l, source)
}

func (s *lostCreateResponseStore) LookupChildExecution(ctx context.Context, id string) (kernel.ChildExecutionGrant, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupChildExecution(ctx, id)
}

func (s *lostCreateResponseStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	return s.Store.(kernel.PhysicalExecutionStore).FinishChildExecution(ctx, g, p)
}

func (s *lostCreateResponseStore) QuarantineExecution(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).QuarantineExecution(ctx, q)
}
func (s *lostCreateResponseStore) RepairExecutionQuarantine(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).RepairExecutionQuarantine(ctx, q)
}
func (s *stealerOnCreateStore) BeginPhysicalExecution(ctx context.Context, p kernel.PhysicalAdmission) (kernel.PhysicalAdmissionResult, error) {
	return s.Store.(kernel.PhysicalExecutionStore).BeginPhysicalExecution(ctx, p)
}
func (s *stealerOnCreateStore) CheckOwnedPhysicalEntry(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, source kernel.Snapshot) (kernel.OwnedPhysicalEntryWindow, error) {
	return s.Store.(kernel.PhysicalExecutionStore).CheckOwnedPhysicalEntry(ctx, g, l, source)
}

func (s *stealerOnCreateStore) LookupChildExecution(ctx context.Context, id string) (kernel.ChildExecutionGrant, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupChildExecution(ctx, id)
}

func (s *stealerOnCreateStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	return s.Store.(kernel.PhysicalExecutionStore).FinishChildExecution(ctx, g, p)
}

func (s *stealerOnCreateStore) QuarantineExecution(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).QuarantineExecution(ctx, q)
}
func (s *stealerOnCreateStore) RepairExecutionQuarantine(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).RepairExecutionQuarantine(ctx, q)
}
func (s *modelInvocationFaultStore) AcquireExecution(ctx context.Context, r kernel.ExecutionAcquireRequest) (kernel.ExecutionLease, error) {
	return s.Store.(kernel.ExecutionStore).AcquireExecution(ctx, r)
}
func (s *modelInvocationFaultStore) RenewExecution(ctx context.Context, r kernel.ExecutionRenewRequest) (kernel.ExecutionLease, error) {
	return s.Store.(kernel.ExecutionStore).RenewExecution(ctx, r)
}
func (s *modelInvocationFaultStore) ReleaseExecution(ctx context.Context, l kernel.ExecutionLease) error {
	return s.Store.(kernel.ExecutionStore).ReleaseExecution(ctx, l)
}
func (s *modelInvocationFaultStore) CheckExecution(ctx context.Context, id string, l *kernel.ExecutionLease) error {
	return s.Store.(kernel.ExecutionStore).CheckExecution(ctx, id, l)
}

func (s *modelInvocationFaultStore) RecordPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, r uint64, o kernel.PhysicalCallbackOutcome, p kernel.PhysicalCallbackReturn) error {
	return s.Store.(kernel.PhysicalExecutionStore).RecordPhysicalCallbackReturn(ctx, g, r, o, p)
}
func (s *modelInvocationFaultStore) LookupPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant) (kernel.PhysicalCallbackCompletion, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupPhysicalCallbackReturn(ctx, g)
}

func (s *lostCreateResponseStore) RecordPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, r uint64, o kernel.PhysicalCallbackOutcome, p kernel.PhysicalCallbackReturn) error {
	return s.Store.(kernel.PhysicalExecutionStore).RecordPhysicalCallbackReturn(ctx, g, r, o, p)
}
func (s *lostCreateResponseStore) LookupPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant) (kernel.PhysicalCallbackCompletion, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupPhysicalCallbackReturn(ctx, g)
}

func (s *stealerOnCreateStore) RecordPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, r uint64, o kernel.PhysicalCallbackOutcome, p kernel.PhysicalCallbackReturn) error {
	return s.Store.(kernel.PhysicalExecutionStore).RecordPhysicalCallbackReturn(ctx, g, r, o, p)
}
func (s *stealerOnCreateStore) LookupPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant) (kernel.PhysicalCallbackCompletion, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupPhysicalCallbackReturn(ctx, g)
}

func (s *modelInvocationFaultStore) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LoadOwnedRetirement(ctx, id)
}

func (s *lostCreateResponseStore) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LoadOwnedRetirement(ctx, id)
}

func (s *stealerOnCreateStore) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LoadOwnedRetirement(ctx, id)
}

func (s *modelInvocationFaultStore) LookupOwnedRetirementForRun(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupOwnedRetirementForRun(ctx, id)
}

func (s *lostCreateResponseStore) LookupOwnedRetirementForRun(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupOwnedRetirementForRun(ctx, id)
}

func (s *stealerOnCreateStore) LookupOwnedRetirementForRun(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	return s.Store.(kernel.PhysicalExecutionStore).LookupOwnedRetirementForRun(ctx, id)
}

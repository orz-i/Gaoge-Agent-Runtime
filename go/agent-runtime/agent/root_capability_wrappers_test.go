package agent_test

import (
	"context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Fault wrappers preserve the complete native capability set. The explicit
// legacyStoreView intentionally remains unsupported for fail-closed tests.
func (s *modelInvocationFaultStore) BeginOwnedPhysicalExecution(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, p kernel.OwnedPhysicalAdmission) error {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).BeginOwnedPhysicalExecution(ctx, g, l, p)
}
func (s *modelInvocationFaultStore) ApplyOwnedPhysicalRecovery(ctx context.Context, g kernel.ChildExecutionGrant, r uint64, m kernel.StoreMutation, p kernel.OwnedPhysicalRecovery) (kernel.Snapshot, error) {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).ApplyOwnedPhysicalRecovery(ctx, g, r, m, p)
}
func (s *modelInvocationFaultStore) BeginChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	return s.Store.(kernel.ChildExecutionStore).BeginChildExecution(ctx, g)
}
func (s *modelInvocationFaultStore) LookupChildExecution(ctx context.Context, id string) (kernel.ChildExecutionGrant, error) {
	return s.Store.(kernel.ChildExecutionStore).LookupChildExecution(ctx, id)
}
func (s *modelInvocationFaultStore) MarkChildExecutionSettled(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	return s.Store.(kernel.ChildExecutionStore).MarkChildExecutionSettled(ctx, g, p)
}
func (s *modelInvocationFaultStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	return s.Store.(kernel.ChildExecutionStore).FinishChildExecution(ctx, g)
}
func (s *modelInvocationFaultStore) ApplyControl(ctx context.Context, id string, r uint64, g kernel.ControlGuard, m kernel.StoreMutation) (kernel.Snapshot, bool, error) {
	return s.Store.(kernel.ControlStore).ApplyControl(ctx, id, r, g, m)
}
func (s *modelInvocationFaultStore) QuarantineExecution(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).QuarantineExecution(ctx, q)
}
func (s *modelInvocationFaultStore) RepairExecutionQuarantine(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).RepairExecutionQuarantine(ctx, q)
}
func (s *lostCreateResponseStore) BeginOwnedPhysicalExecution(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, p kernel.OwnedPhysicalAdmission) error {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).BeginOwnedPhysicalExecution(ctx, g, l, p)
}
func (s *lostCreateResponseStore) ApplyOwnedPhysicalRecovery(ctx context.Context, g kernel.ChildExecutionGrant, r uint64, m kernel.StoreMutation, p kernel.OwnedPhysicalRecovery) (kernel.Snapshot, error) {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).ApplyOwnedPhysicalRecovery(ctx, g, r, m, p)
}
func (s *lostCreateResponseStore) BeginChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	return s.Store.(kernel.ChildExecutionStore).BeginChildExecution(ctx, g)
}
func (s *lostCreateResponseStore) LookupChildExecution(ctx context.Context, id string) (kernel.ChildExecutionGrant, error) {
	return s.Store.(kernel.ChildExecutionStore).LookupChildExecution(ctx, id)
}
func (s *lostCreateResponseStore) MarkChildExecutionSettled(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	return s.Store.(kernel.ChildExecutionStore).MarkChildExecutionSettled(ctx, g, p)
}
func (s *lostCreateResponseStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	return s.Store.(kernel.ChildExecutionStore).FinishChildExecution(ctx, g)
}
func (s *lostCreateResponseStore) ApplyControl(ctx context.Context, id string, r uint64, g kernel.ControlGuard, m kernel.StoreMutation) (kernel.Snapshot, bool, error) {
	return s.Store.(kernel.ControlStore).ApplyControl(ctx, id, r, g, m)
}
func (s *lostCreateResponseStore) QuarantineExecution(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).QuarantineExecution(ctx, q)
}
func (s *lostCreateResponseStore) RepairExecutionQuarantine(ctx context.Context, q kernel.ExecutionQuarantine) error {
	return s.Store.(kernel.ExecutionQuarantineStore).RepairExecutionQuarantine(ctx, q)
}
func (s *stealerOnCreateStore) BeginOwnedPhysicalExecution(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, p kernel.OwnedPhysicalAdmission) error {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).BeginOwnedPhysicalExecution(ctx, g, l, p)
}
func (s *stealerOnCreateStore) ApplyOwnedPhysicalRecovery(ctx context.Context, g kernel.ChildExecutionGrant, r uint64, m kernel.StoreMutation, p kernel.OwnedPhysicalRecovery) (kernel.Snapshot, error) {
	return s.Store.(kernel.OwnedPhysicalExecutionStore).ApplyOwnedPhysicalRecovery(ctx, g, r, m, p)
}
func (s *stealerOnCreateStore) BeginChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	return s.Store.(kernel.ChildExecutionStore).BeginChildExecution(ctx, g)
}
func (s *stealerOnCreateStore) LookupChildExecution(ctx context.Context, id string) (kernel.ChildExecutionGrant, error) {
	return s.Store.(kernel.ChildExecutionStore).LookupChildExecution(ctx, id)
}
func (s *stealerOnCreateStore) MarkChildExecutionSettled(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	return s.Store.(kernel.ChildExecutionStore).MarkChildExecutionSettled(ctx, g, p)
}
func (s *stealerOnCreateStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	return s.Store.(kernel.ChildExecutionStore).FinishChildExecution(ctx, g)
}
func (s *stealerOnCreateStore) ApplyControl(ctx context.Context, id string, r uint64, g kernel.ControlGuard, m kernel.StoreMutation) (kernel.Snapshot, bool, error) {
	return s.Store.(kernel.ControlStore).ApplyControl(ctx, id, r, g, m)
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
func (s *modelInvocationFaultStore) ApplyExecution(ctx context.Context, l kernel.ExecutionLease, r uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	s.mu.Lock()
	fail := !s.failed && mutationHasEvent(m.Events, s.eventType)
	if fail {
		s.failed = true
	}
	s.mu.Unlock()
	if fail && !s.afterCommit {
		return kernel.Snapshot{}, errInjectedModelInvocationCrash
	}
	out, err := s.Store.(kernel.ExecutionStore).ApplyExecution(ctx, l, r, m)
	if err != nil {
		return out, err
	}
	if fail {
		return kernel.Snapshot{}, errInjectedModelInvocationCrash
	}
	return out, nil
}

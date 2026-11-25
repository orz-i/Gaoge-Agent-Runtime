package conformance

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Stop the original recovery process at one write or ACK. All subsequent
// retirement operations from that same interrupted owner fail until the
// test constructs a new Runtime from independent reopened Store handles.
var errReturnedRootRetirementCut = errors.New("original returned Root retirement interrupted")

type returnedRootRetirementFault struct {
	managedStorePorts
	mode, recoveryCallID string
	dead                 bool
	hits                 int
	grant                kernel.ChildExecutionGrant
}

func (s *returnedRootRetirementFault) cut(g kernel.ChildExecutionGrant) error {
	s.hits++
	s.dead = true
	s.grant = g
	return errReturnedRootRetirementCut
}
func (s *returnedRootRetirementFault) matchesRecovery(m kernel.StoreMutation) bool {
	if s.recoveryCallID == "" {
		return true
	}
	for _, e := range m.Events {
		if e.Type == "tool.receipt_recovered" && e.Message == s.recoveryCallID {
			return true
		}
	}
	return false
}
func (s *returnedRootRetirementFault) ApplyOwnedPhysicalRecovery(ctx context.Context, g kernel.ChildExecutionGrant, rev uint64, m kernel.StoreMutation, p kernel.OwnedPhysicalRecovery) (kernel.Snapshot, error) {
	if s.dead {
		return kernel.Snapshot{}, errReturnedRootRetirementCut
	}
	if s.mode == "recovery-before" && s.matchesRecovery(m) {
		return kernel.Snapshot{}, s.cut(g)
	}
	next, err := s.managedStorePorts.ApplyOwnedPhysicalRecovery(ctx, g, rev, m, p)
	if err == nil && s.mode == "recovery-ack" && s.matchesRecovery(m) {
		return kernel.Snapshot{}, s.cut(g)
	}
	return next, err
}
func (s *returnedRootRetirementFault) MarkChildExecutionSettled(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	if s.dead {
		return errReturnedRootRetirementCut
	}
	if s.mode == "mark-before" {
		return s.cut(g)
	}
	err := s.managedStorePorts.MarkChildExecutionSettled(ctx, g, p)
	if err == nil && s.mode == "mark-ack" {
		return s.cut(g)
	}
	return err
}
func (s *returnedRootRetirementFault) CompleteManagedRecovery(ctx context.Context, g kernel.ChildExecutionGrant, source kernel.Snapshot, p kernel.ManagedCompletionProof) error {
	if s.dead {
		return errReturnedRootRetirementCut
	}
	if s.mode == "t4-before" {
		return s.cut(g)
	}
	err := s.managedStorePorts.CompleteManagedRecovery(ctx, g, source, p)
	if err == nil && s.mode == "t4-ack" {
		return s.cut(g)
	}
	return err
}
func (s *returnedRootRetirementFault) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	if s.dead {
		return errReturnedRootRetirementCut
	}
	if s.mode == "finish-before" {
		return s.cut(g)
	}
	err := s.managedStorePorts.FinishChildExecution(ctx, g)
	if err == nil && s.mode == "finish-ack" {
		return s.cut(g)
	}
	return err
}
func (s *returnedRootRetirementFault) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	if s.dead && id == s.grant.ID {
		return kernel.OwnedRetirementReceipt{}, errReturnedRootRetirementCut
	}
	return s.managedStorePorts.LoadOwnedRetirement(ctx, id)
}
func (s *returnedRootRetirementFault) AcknowledgeOwnedRetirement(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.OwnedRetirementAcknowledgement) (kernel.OwnedRetirementAckResult, error) {
	if s.dead {
		return kernel.OwnedRetirementAckResult{}, errReturnedRootRetirementCut
	}
	if s.mode == "ack-before" {
		return kernel.OwnedRetirementAckResult{}, s.cut(g)
	}
	out, err := s.managedStorePorts.AcknowledgeOwnedRetirement(ctx, g, p)
	if err == nil && s.mode == "ack-ack" {
		return kernel.OwnedRetirementAckResult{}, s.cut(g)
	}
	return out, err
}

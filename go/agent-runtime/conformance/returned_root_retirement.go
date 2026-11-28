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

func (s *returnedRootRetirementFault) Commit(commitCtx context.Context, commitID string, commitRev uint64, commitMutation kernel.StoreMutation, commitAuth kernel.CommitAuthority) (kernel.Snapshot, bool, error) {
	if commitAuth.Recovery != nil {
		g := commitAuth.Recovery.Grant

		if s.dead {
			return kernel.Snapshot{}, false, errReturnedRootRetirementCut
		}
		if s.mode == "recovery-before" && s.matchesRecovery(commitMutation) {
			return kernel.Snapshot{}, false, s.cut(g)
		}
		next, _, err := s.managedStorePorts.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
		if err == nil && s.mode == "recovery-ack" && s.matchesRecovery(commitMutation) {
			return kernel.Snapshot{}, false, s.cut(g)
		}
		return next, false, err
	}
	return s.managedStorePorts.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
}

func (s *returnedRootRetirementFault) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	if s.dead {
		return errReturnedRootRetirementCut
	}
	if s.mode == "finish-before" {
		return s.cut(g)
	}
	err := s.managedStorePorts.FinishChildExecution(ctx, g, p)
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

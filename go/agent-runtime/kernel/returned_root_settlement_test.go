package kernel_test

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestReturnedRootCustodyRequiresActualCallbackAndCompleteHistory(t *testing.T) {
	current, previous := settledRootFixture()
	current.Run.Status = kernel.RunStatusRunning
	grant := previous.Evidence.Grant
	grant.ID, grant.ExecutionLeaseID, grant.ExecutionEpoch = "current-grant", "new-lease", 4
	grant.AdmissionOperation, grant.ExpectedRevision = "resume", current.Run.Revision
	completion := previous.Evidence.Completion
	completion.GrantID, completion.ControlCustody = grant.ID, kernel.OwnedCustodyScoped
	lease := kernel.ExecutionLease{RunID: current.Run.ID, OwnerID: grant.ExecutionOwnerID, LeaseID: grant.ExecutionLeaseID, Epoch: grant.ExecutionEpoch}
	if err := kernel.VerifyReturnedRootSettlementFacts(current, current, 4, 4, lease, grant, completion, []kernel.OwnedRetirementReceipt{previous}); err != nil {
		t.Fatalf("actual returned callback and safe history rejected: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*kernel.Snapshot, *kernel.ExecutionLease, *kernel.ChildExecutionGrant, *kernel.OwnedPhysicalCallbackCompletion, *[]kernel.OwnedRetirementReceipt)
	}{
		{"not-returned", func(_ *kernel.Snapshot, _ *kernel.ExecutionLease, _ *kernel.ChildExecutionGrant, c *kernel.OwnedPhysicalCallbackCompletion, _ *[]kernel.OwnedRetirementReceipt) {
			c.HostCompleted = false
		}},
		{"opaque-callback", func(_ *kernel.Snapshot, _ *kernel.ExecutionLease, _ *kernel.ChildExecutionGrant, c *kernel.OwnedPhysicalCallbackCompletion, _ *[]kernel.OwnedRetirementReceipt) {
			c.ControlCustody = ""
		}},
		{"borrowed-owner", func(_ *kernel.Snapshot, _ *kernel.ExecutionLease, _ *kernel.ChildExecutionGrant, c *kernel.OwnedPhysicalCallbackCompletion, _ *[]kernel.OwnedRetirementReceipt) {
			c.LeaseOwned = false
		}},
		{"foreign-owner", func(_ *kernel.Snapshot, l *kernel.ExecutionLease, _ *kernel.ChildExecutionGrant, _ *kernel.OwnedPhysicalCallbackCompletion, _ *[]kernel.OwnedRetirementReceipt) {
			l.OwnerID = "other"
		}},
		{"missing-grant", func(_ *kernel.Snapshot, _ *kernel.ExecutionLease, g *kernel.ChildExecutionGrant, _ *kernel.OwnedPhysicalCallbackCompletion, _ *[]kernel.OwnedRetirementReceipt) {
			g.ID = ""
		}},
		{"stale-epoch", func(_ *kernel.Snapshot, _ *kernel.ExecutionLease, g *kernel.ChildExecutionGrant, _ *kernel.OwnedPhysicalCallbackCompletion, _ *[]kernel.OwnedRetirementReceipt) {
			g.ExecutionEpoch++
		}},
		{"stale-snapshot", func(s *kernel.Snapshot, _ *kernel.ExecutionLease, _ *kernel.ChildExecutionGrant, _ *kernel.OwnedPhysicalCallbackCompletion, _ *[]kernel.OwnedRetirementReceipt) {
			s.EventHead++
		}},
		{"foreign-completion", func(_ *kernel.Snapshot, _ *kernel.ExecutionLease, _ *kernel.ChildExecutionGrant, c *kernel.OwnedPhysicalCallbackCompletion, _ *[]kernel.OwnedRetirementReceipt) {
			c.GrantID = "other"
		}},
		{"opaque-history", func(_ *kernel.Snapshot, _ *kernel.ExecutionLease, _ *kernel.ChildExecutionGrant, _ *kernel.OwnedPhysicalCallbackCompletion, rows *[]kernel.OwnedRetirementReceipt) {
			(*rows)[0].Evidence.Completion.ControlCustody = ""
			(*rows)[0].ACKIdentity = (*rows)[0].Identity()
		}},
		{"missing-old-ACK", func(_ *kernel.Snapshot, _ *kernel.ExecutionLease, _ *kernel.ChildExecutionGrant, _ *kernel.OwnedPhysicalCallbackCompletion, rows *[]kernel.OwnedRetirementReceipt) {
			(*rows)[0].Phase, (*rows)[0].ACKIdentity = "RETIRED", ""
		}},
		{"ambiguous-history", func(_ *kernel.Snapshot, _ *kernel.ExecutionLease, _ *kernel.ChildExecutionGrant, _ *kernel.OwnedPhysicalCallbackCompletion, rows *[]kernel.OwnedRetirementReceipt) {
			*rows = append(*rows, (*rows)[0])
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, l, g, c, rows := current, lease, grant, completion, []kernel.OwnedRetirementReceipt{previous}
			tc.change(&s, &l, &g, &c, &rows)
			if err := kernel.VerifyReturnedRootSettlementFacts(current, s, 4, 4, l, g, c, rows); !errors.Is(err, kernel.ErrExecutionConflict) {
				t.Fatalf("uncertain callback obtained cancellation recovery custody: %v", err)
			}
		})
	}
}

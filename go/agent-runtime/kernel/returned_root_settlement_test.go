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
	if err := kernel.VerifyReturnedRootSettlementFacts(current, current, 4, 4, lease, grant, completion, []kernel.OwnedRetirementReceipt{previous}, true); err != nil {
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
			if err := kernel.VerifyReturnedRootSettlementFacts(current, s, 4, 4, l, g, c, rows, true); !errors.Is(err, kernel.ErrExecutionConflict) {
				t.Fatalf("uncertain callback obtained cancellation recovery custody: %v", err)
			}
		})
	}
}

// The same returned-root custody can survive Finish before ACK, but only for
// the unique exact current retirement selected by the existing Store rules.
func TestReturnedRootCustodyAcrossPendingRetirement(t *testing.T) {
	current, previous := settledRootFixture()
	current.Run.Status = kernel.RunStatusRunning
	g := previous.Evidence.Grant
	g.ID, g.ExecutionLeaseID, g.ExecutionEpoch = "current-grant", "new-lease", 4
	g.AdmissionOperation, g.ExpectedRevision = "resume", current.Run.Revision
	c := previous.Evidence.Completion
	c.GrantID, c.ControlCustody = g.ID, kernel.OwnedCustodyScoped
	c.ReturnRevision = current.Run.Revision
	l := kernel.ExecutionLease{RunID: current.Run.ID, OwnerID: g.ExecutionOwnerID, LeaseID: g.ExecutionLeaseID, Epoch: g.ExecutionEpoch}
	pending := kernel.OwnedRetirementReceipt{Version: 1, Evidence: kernel.OwnedSettlementEvidence{Version: 1, Grant: g, Snapshot: current, Completion: c, MarkOrigin: "RECOVERED_RETURN"}, Closures: []kernel.ManagedTransfer{}, Phase: "RETIRED"}
	if !pending.Valid() {
		t.Fatal("invalid retirement fixture")
	}
	for _, mode := range []string{"exact", "foreign-owner", "stale-source", "active-other-grant", "old-unacknowledged", "duplicate", "opaque", "borrowed", "historical-ack", "missing-current"} {
		t.Run(mode, func(t *testing.T) {
			s, lease, old, row := current, l, previous, pending
			active := false
			switch mode {
			case "foreign-owner":
				lease.LeaseID = "other"
			case "stale-source":
				s.EventHead++
			case "active-other-grant":
				active = true
			case "old-unacknowledged":
				old.Phase, old.ACKIdentity = "RETIRED", ""
			case "opaque":
				row.Evidence.Completion.ControlCustody = ""
			case "borrowed":
				row.Evidence.Completion.LeaseOwned = false
			case "historical-ack":
				row.Phase = "ACKNOWLEDGED"
				row.ACKIdentity = row.Identity()
			}
			rows := []kernel.OwnedRetirementReceipt{old, row}
			if mode == "duplicate" {
				rows = append(rows, row)
			}
			if mode == "missing-current" {
				rows = rows[:1]
			}
			err := kernel.VerifyReturnedRootSettlementFacts(s, s, 4, 4, lease, kernel.ChildExecutionGrant{}, kernel.OwnedPhysicalCallbackCompletion{}, rows, active)
			if mode == "exact" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, kernel.ErrExecutionConflict) {
				t.Fatal("unqualified pending retirement accepted", err)
			}
		})
	}
}

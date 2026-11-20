package kernel_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func settledRootFixture() (kernel.Snapshot, kernel.OwnedRetirementReceipt) {
	run := kernel.Snapshot{Run: kernel.Run{ID: "owned-root", Kind: "agent", Revision: 7, Status: kernel.RunStatusWaitingInput}, State: []byte(`{"model":"known"}`)}
	g := kernel.ChildExecutionGrant{
		ID: "owner-grant", ChildRunID: run.Run.ID, ExpectedRevision: 4,
		ExecutionOwnerID: "owner", ExecutionLeaseID: "lease", ExecutionEpoch: 3,
		AdmissionSourceHash: strings.Repeat("a", 64), AdmissionOperation: "prepare",
	}
	c := kernel.OwnedPhysicalCallbackCompletion{GrantID: g.ID, ReturnRevision: run.Run.Revision, ReturnSourceHash: strings.Repeat("b", 64), HostCompleted: true, LeaseOwned: true, ControlCustody: kernel.OwnedCustodyPrepared}
	r := kernel.OwnedRetirementReceipt{Version: 1, Evidence: kernel.OwnedSettlementEvidence{Version: 1, Grant: g, Snapshot: run, Completion: c, MarkOrigin: "OWNER_RETURN"}, Closures: []kernel.ManagedTransfer{}, Phase: "ACKNOWLEDGED"}
	r.ACKIdentity = r.Identity()
	return run, r
}

func TestVerifiedOwnedRootQuiescenceRequiresExactRetiredCallback(t *testing.T) {
	original, receipt := settledRootFixture()
	if !receipt.Valid() {
		t.Fatal("test requires a valid persisted retirement receipt")
	}
	if err := kernel.VerifyOwnedRootSettlementFacts(original, original, 3, 3, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt}); err != nil {
		t.Fatalf("valid native callback retired with original identity: %v", err)
	}
	cancelled := original
	cancelled.Run.Revision++
	cancelled.Run.Status = kernel.RunStatusCancelled
	cancelled.Run.ErrorCode = "run.cancelled"
	cancelled.EventHead++
	if err := kernel.VerifyOwnedRootSettlementFacts(cancelled, cancelled, 4, 3, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt}); err != nil {
		t.Fatalf("exact atomic Cancel successor denied: %v", err)
	}
	mutated := cancelled
	mutated.EventHead++
	if err := kernel.VerifyOwnedRootSettlementFacts(mutated, mutated, 4, 3, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt}); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("unwitnessed second journal mutation impersonated Cancel: %v", err)
	}
	testcases := []struct {
		name              string
		expected, current kernel.Snapshot
		epoch             uint64
		lease             kernel.ExecutionLease
		active            bool
		receipts          []kernel.OwnedRetirementReceipt
	}{
		{"without receipt", original, original, 3, kernel.ExecutionLease{}, false, nil},
		{"unretired callback", original, original, 3, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{func() kernel.OwnedRetirementReceipt { r := receipt; r.Phase = "RETIRED"; r.ACKIdentity = ""; return r }()}},
		{"different epoch", original, original, 4, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt}},
		{"live physical grant", original, original, 3, kernel.ExecutionLease{}, true, []kernel.OwnedRetirementReceipt{receipt}},
		{"new execution owner", original, original, 3, kernel.ExecutionLease{RunID: "owned-root", OwnerID: "other", LeaseID: "other-lease", Epoch: 3}, false, []kernel.OwnedRetirementReceipt{receipt}},
		{"stale snapshot", original, func() kernel.Snapshot { s := original; s.Run.Revision++; return s }(), 3, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt}},
		{"wrong kind", original, func() kernel.Snapshot { s := original; s.Run.Kind = "a2a"; return s }(), 3, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt}},
		{"duplicate owned receipts", original, original, 3, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt, receipt}},
		{"legacy or opaque custody", original, original, 3, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{func() kernel.OwnedRetirementReceipt {
			r := receipt
			r.Evidence.Completion.ControlCustody = ""
			r.ACKIdentity = r.Identity()
			return r
		}()}},
		{"tampered callback proof", original, original, 3, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{func() kernel.OwnedRetirementReceipt {
			r := receipt
			r.Evidence.Completion.ReturnSourceHash = "bad"
			return r
		}()}},
		{"foreign identity", original, func() kernel.Snapshot { s := original; s.Run.Actor.TenantID = "foreign"; return s }(), 3, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt}},
	}
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			if err := kernel.VerifyOwnedRootSettlementFacts(tc.expected, tc.current, tc.epoch, tc.epoch, tc.lease, tc.active, tc.receipts); !errors.Is(err, kernel.ErrExecutionConflict) {
				t.Fatalf("invalid owned Root obtained cancellation custody: %v", err)
			}
		})
	}
}

func TestOwnedSettlementRequiresLatestPhysicalAdmissionNotLatestLease(t *testing.T) {
	snapshot, receipt := settledRootFixture()
	// Store-proven lease-only generations may advance without dispatching.
	if err := kernel.VerifyOwnedRootSettlementFacts(snapshot, snapshot, 8, 3, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt}); err != nil {
		t.Fatalf("undispatched lease churn lost physical custody: %v", err)
	}
	for _, physicalEpoch := range []uint64{0, 2, 4, 9} {
		if err := kernel.VerifyOwnedRootSettlementFacts(snapshot, snapshot, 8, physicalEpoch, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt}); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("missing/substituted latest physical admission %d borrowed old receipt: %v", physicalEpoch, err)
		}
	}
	// A later no-op callback still owns the latest physical grant even when
	// both callback return snapshots happen to have the same Run revision.
	newer := receipt
	newer.Evidence.Grant.ID = "new-grant"
	newer.Evidence.Grant.ExecutionEpoch = 5
	newer.Evidence.Grant.AdmissionOperation = "resume"
	newer.Evidence.Completion.GrantID = "new-grant"
	newer.Evidence.Completion.ControlCustody = kernel.OwnedCustodyScoped
	newer.ACKIdentity = newer.Identity()
	if err := kernel.VerifyOwnedRootSettlementFacts(snapshot, snapshot, 8, 5, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt, newer}); err != nil {
		t.Fatalf("complete safe latest callback history rejected: %v", err)
	}
	receipt.Evidence.Completion.ControlCustody = ""
	receipt.ACKIdentity = receipt.Identity()
	if err := kernel.VerifyOwnedRootSettlementFacts(snapshot, snapshot, 8, 5, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt, newer}); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("latest safe callback erased earlier opaque effects: %v", err)
	}
}

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
	c := kernel.PhysicalCallbackCompletion{GrantID: g.ID, ReturnRevision: run.Run.Revision, ReturnSourceHash: strings.Repeat("b", 64), HostCompleted: true, LeaseOwned: true, ControlCustody: kernel.OwnedCustodyPrepared}
	r := kernel.OwnedRetirementReceipt{Version: 1, Evidence: kernel.OwnedSettlementEvidence{Version: 1, Grant: g, Snapshot: run, Completion: c}, Closures: []kernel.ManagedTransfer{}}
	r.Digest = r.Identity()
	return run, r
}

func TestOwnedCancelSuccessorClearsOriginalApproval(t *testing.T) {
	run, receipt := settledRootFixture()
	run.Checkpoint = &kernel.Checkpoint{ID: "original-approval", Status: kernel.CheckpointPending}
	receipt.Evidence.Snapshot = run
	receipt.Digest = receipt.Identity()
	next := run
	next.Run.Status, next.Run.ErrorCode = kernel.RunStatusCancelled, "run.cancelled"
	next.Run.Revision++
	next.EventHead++
	next.Checkpoint = nil
	check := func(s kernel.Snapshot) error {
		return kernel.VerifyExecutionCustodyFacts(s, s, 3, 3, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt}, kernel.ChildExecutionGrant{}, kernel.PhysicalCallbackCompletion{}, kernel.CustodyBoundedApproval)
	}
	if err := check(next); err != nil {
		t.Fatal(err)
	}
	next.Checkpoint = run.Checkpoint
	if !errors.Is(check(next), kernel.ErrExecutionConflict) {
		t.Fatal("accepted a checkpoint surviving native Cancel")
	}
}

func TestBoundedToolControlRejectsObservationOnlyCustody(t *testing.T) {
	run, receipt := settledRootFixture()
	receipt.Evidence.Grant.AdmissionOperation = "resume"
	for _, tc := range []struct {
		custody  kernel.OwnedCallbackCustody
		accepted bool
	}{
		{kernel.OwnedBoundedApprovalReturn, true}, {kernel.OwnedToolResultReturn, false}, {kernel.OwnedToolBudgetReturn, false}, {"", false},
	} {
		receipt.Evidence.Completion.ControlCustody = tc.custody
		receipt.Digest = receipt.Identity()
		err := kernel.VerifyExecutionCustodyFacts(run, run, 3, 3, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt}, kernel.ChildExecutionGrant{}, kernel.PhysicalCallbackCompletion{}, kernel.CustodyBoundedApproval)
		if (err == nil) != tc.accepted {
			t.Fatalf("custody %q: %v", tc.custody, err)
		}
	}
}

func TestVerifiedOwnedRootQuiescenceRequiresExactRetiredCallback(t *testing.T) {
	original, receipt := settledRootFixture()
	if !receipt.Valid() {
		t.Fatal("test requires a valid persisted retirement receipt")
	}
	if err := kernel.VerifyExecutionCustodyFacts(original, original, 3, 3, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt}, kernel.ChildExecutionGrant{}, kernel.PhysicalCallbackCompletion{}, kernel.CustodyRetired); err != nil {
		t.Fatalf("valid native callback retired with original identity: %v", err)
	}
	cancelled := original
	cancelled.Run.Revision++
	cancelled.Run.Status = kernel.RunStatusCancelled
	cancelled.Run.ErrorCode = "run.cancelled"
	cancelled.EventHead++
	if err := kernel.VerifyExecutionCustodyFacts(cancelled, cancelled, 4, 3, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt}, kernel.ChildExecutionGrant{}, kernel.PhysicalCallbackCompletion{}, kernel.CustodyRetired); err != nil {
		t.Fatalf("exact atomic Cancel successor denied: %v", err)
	}
	mutated := cancelled
	mutated.EventHead++
	if err := kernel.VerifyExecutionCustodyFacts(mutated, mutated, 4, 3, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt}, kernel.ChildExecutionGrant{}, kernel.PhysicalCallbackCompletion{}, kernel.CustodyRetired); !errors.Is(err, kernel.ErrExecutionConflict) {
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
		{"unretired callback", original, original, 3, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{func() kernel.OwnedRetirementReceipt { r := receipt; r.Digest = ""; return r }()}},
		{"different epoch", original, original, 4, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt}},
		{"live physical grant", original, original, 3, kernel.ExecutionLease{}, true, []kernel.OwnedRetirementReceipt{receipt}},
		{"new execution owner", original, original, 3, kernel.ExecutionLease{RunID: "owned-root", OwnerID: "other", LeaseID: "other-lease", Epoch: 3}, false, []kernel.OwnedRetirementReceipt{receipt}},
		{"stale snapshot", original, func() kernel.Snapshot { s := original; s.Run.Revision++; return s }(), 3, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt}},
		{"wrong kind", original, func() kernel.Snapshot { s := original; s.Run.Kind = "a2a"; return s }(), 3, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt}},
		{"duplicate owned receipts", original, original, 3, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt, receipt}},
		{"legacy or opaque custody", original, original, 3, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{func() kernel.OwnedRetirementReceipt {
			r := receipt
			r.Evidence.Completion.ControlCustody = ""
			r.Digest = r.Identity()
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
			if err := kernel.VerifyExecutionCustodyFacts(tc.expected, tc.current, tc.epoch, tc.epoch, tc.lease, tc.active, tc.receipts, kernel.ChildExecutionGrant{}, kernel.PhysicalCallbackCompletion{}, kernel.CustodyRetired); !errors.Is(err, kernel.ErrExecutionConflict) {
				t.Fatalf("invalid owned Root obtained cancellation custody: %v", err)
			}
		})
	}
}

func TestOwnedSettlementRequiresLatestPhysicalAdmissionNotLatestLease(t *testing.T) {
	snapshot, receipt := settledRootFixture()
	// Store-proven lease-only generations may advance without dispatching.
	if err := kernel.VerifyExecutionCustodyFacts(snapshot, snapshot, 8, 3, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt}, kernel.ChildExecutionGrant{}, kernel.PhysicalCallbackCompletion{}, kernel.CustodyRetired); err != nil {
		t.Fatalf("undispatched lease churn lost physical custody: %v", err)
	}
	for _, physicalEpoch := range []uint64{0, 2, 4, 9} {
		if err := kernel.VerifyExecutionCustodyFacts(snapshot, snapshot, 8, physicalEpoch, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt}, kernel.ChildExecutionGrant{}, kernel.PhysicalCallbackCompletion{}, kernel.CustodyRetired); !errors.Is(err, kernel.ErrExecutionConflict) {
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
	newer.Digest = newer.Identity()
	if err := kernel.VerifyExecutionCustodyFacts(snapshot, snapshot, 8, 5, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt, newer}, kernel.ChildExecutionGrant{}, kernel.PhysicalCallbackCompletion{}, kernel.CustodyRetired); err != nil {
		t.Fatalf("complete safe latest callback history rejected: %v", err)
	}
	receipt.Evidence.Completion.ControlCustody = ""
	receipt.Digest = receipt.Identity()
	if err := kernel.VerifyExecutionCustodyFacts(snapshot, snapshot, 8, 5, kernel.ExecutionLease{}, false, []kernel.OwnedRetirementReceipt{receipt, newer}, kernel.ChildExecutionGrant{}, kernel.PhysicalCallbackCompletion{}, kernel.CustodyRetired); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("latest safe callback erased earlier opaque effects: %v", err)
	}
}

func TestOwnedSettlementPreservesOnlyExactNoEntrySuffix(t *testing.T) {
	source, actual := settledRootFixture()
	noEntry := func(id string, epoch uint64, s kernel.Snapshot) kernel.OwnedRetirementReceipt {
		r := actual
		g := r.Evidence.Grant
		g.ID, g.ExecutionEpoch, g.ExpectedRevision, g.AdmissionOperation = id, epoch, s.Run.Revision, "resume"
		outcome := kernel.PhysicalCallbackOutcome{LeaseOwned: true, ControlCustody: kernel.OwnedCustodyNotEntered}
		r.Evidence = kernel.OwnedSettlementEvidence{Version: 1, Grant: g, Snapshot: s,
			Completion: kernel.PhysicalCallbackCompletion{GrantID: id, ReturnRevision: s.Run.Revision, ReturnSourceHash: kernel.ManagedDigest(kernel.PhysicalCallbackReturnPayload(s, outcome)), LeaseOwned: true, ControlCustody: kernel.OwnedCustodyNotEntered}}
		r.Digest = r.Identity()
		if !r.Valid() {
			t.Fatal("invalid no-entry fixture")
		}
		return r
	}
	one, two := noEntry("no-entry-one", 5, source), noEntry("no-entry-two", 7, source)
	changed := source
	changed.Run.Revision++
	foreign := noEntry("changed-source", 7, changed)
	unack := two
	unack.Digest = ""
	for _, c := range []struct {
		name     string
		receipts []kernel.OwnedRetirementReceipt
		want     bool
	}{
		{"exact suffix unordered", []kernel.OwnedRetirementReceipt{two, actual, one}, true},
		{"no actual", []kernel.OwnedRetirementReceipt{one, two}, false},
		{"missing latest", []kernel.OwnedRetirementReceipt{actual, one}, false},
		{"different source", []kernel.OwnedRetirementReceipt{actual, one, foreign}, false},
		{"unacknowledged", []kernel.OwnedRetirementReceipt{actual, one, unack}, false},
		{"duplicate", []kernel.OwnedRetirementReceipt{actual, one, two, two}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := kernel.VerifyExecutionCustodyFacts(source, source, 8, 7, kernel.ExecutionLease{}, false, c.receipts, kernel.ChildExecutionGrant{}, kernel.PhysicalCallbackCompletion{}, kernel.CustodyRetired)
			if (err == nil) != c.want {
				t.Fatalf("custody mismatch: %v", err)
			}
		})
	}
}

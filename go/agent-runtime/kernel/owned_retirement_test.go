package kernel

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
)

func retirementFixture() (OwnedRetirementReceipt, ExecutionLease) {
	l := ExecutionLease{RunID: "root", OwnerID: "owner", LeaseID: "lease", Epoch: 1}
	g := ChildExecutionGrant{ID: "scope", ChildRunID: "root", ExpectedRevision: 1, ExecutionOwnerID: l.OwnerID, ExecutionLeaseID: l.LeaseID, ExecutionEpoch: l.Epoch, AdmissionSourceHash: strings.Repeat("a", 64), AdmissionOperation: "start"}
	s := Snapshot{Run: Run{ID: "root", Kind: "agent", Status: RunStatusRunning, Revision: 2}, State: json.RawMessage(`{"receipt":"original"}`)}
	c := OwnedPhysicalCallbackCompletion{GrantID: g.ID, ReturnRevision: 2, ReturnSourceHash: strings.Repeat("b", 64), HostCompleted: true, LeaseOwned: true}
	r := OwnedRetirementReceipt{Version: 1, Evidence: OwnedSettlementEvidence{Version: 1, Grant: g, Snapshot: s, Completion: c, MarkOrigin: "CALLBACK_RETURN"}, Closures: []ManagedTransfer{}, Phase: "ACKNOWLEDGED"}
	r.ACKIdentity = r.Identity()
	return r, l
}
func TestOwnedRetirementWireAndExactFinish(t *testing.T) {
	r, l := retirementFixture()
	g, s, c := r.Evidence.Grant, r.Evidence.Snapshot, r.Evidence.Completion
	b, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	var decoded OwnedRetirementReceipt
	if e = json.Unmarshal(b, &decoded); e != nil || !decoded.Valid() || decoded.Identity() != r.Identity() {
		t.Fatalf("wire: %v", e)
	}
	permit := ChildExecutionSettlement{proof: featurecontrol.Seal(s.Run.ID, s.Run.Revision, "agent_owned_physical_settled", g.ID, s.State, s)}
	receipt, e := BuildOwnedRetirementReceipt(g, s, l, c, permit, []ManagedTransfer{})
	if e != nil || !receipt.Valid() || receipt.Identity() != r.Identity() {
		t.Fatalf("finish: %v", e)
	}
	for _, kind := range []string{"missing-permit", "foreign-lease", "changed-source", "unknown-return", "missing-return", "old-intermediate", "invalid-digest", "nil-closures"} {
		t.Run(kind, func(t *testing.T) {
			owner, current, completion, p := l, s, c, permit
			switch kind {
			case "missing-permit":
				p = ChildExecutionSettlement{}
			case "foreign-lease":
				owner.LeaseID = "foreign"
			case "changed-source":
				current.EventHead++
			case "unknown-return":
				completion.HostCompleted = false
			case "missing-return":
				completion = OwnedPhysicalCallbackCompletion{}
			case "old-intermediate":
				old := r
				old.Phase = "RETIRED"
				old.ACKIdentity = ""
				if old.Valid() {
					t.Fatal("old intermediate accepted")
				}
				return
			case "invalid-digest":
				bad := r
				bad.ACKIdentity = "foreign"
				if bad.Valid() {
					t.Fatal("changed digest accepted")
				}
				return
			case "nil-closures":
				bad := r
				bad.Closures = nil
				bad.ACKIdentity = bad.Identity()
				if bad.Valid() {
					t.Fatal("null closure accepted")
				}
				return
			}
			if _, e := BuildOwnedRetirementReceipt(g, current, owner, completion, p, []ManagedTransfer{}); !errors.Is(e, ErrExecutionConflict) {
				t.Fatalf("invalid finish: %v", e)
			}
		})
	}
}

func TestOwnedRetirementRunSelector(t *testing.T) {
	r, l := retirementFixture()
	for _, name := range []string{"unique", "absent", "ambiguous", "corrupt", "source", "epoch", "replacement", "foreign-lease", "old-then-current", "old-only", "borrowed", "borrowed-foreign", "old-intermediate"} {
		t.Run(name, func(t *testing.T) {
			rows := []OwnedRetirementReceipt{r}
			current, epoch, lease, busy := r.Evidence.Snapshot, l.Epoch, ExecutionLease{}, false
			var want error
			switch name {
			case "absent":
				rows = nil
				want = ErrNotFound
			case "ambiguous":
				rows = append(rows, r)
				want = ErrExecutionConflict
			case "corrupt":
				rows[0].ACKIdentity = "bad"
				want = ErrExecutionConflict
			case "source":
				current.EventHead++
				want = ErrNotFound
			case "epoch":
				epoch++
				want = ErrNotFound
			case "replacement":
				busy = true
				want = ErrExecutionConflict
			case "foreign-lease":
				lease = l
				want = ErrNotFound
			case "old-then-current", "old-only":
				old := r
				old.Evidence.Grant.ExecutionEpoch = 2
				old.ACKIdentity = old.Identity()
				rows = []OwnedRetirementReceipt{old, r}
				if name == "old-only" {
					rows = rows[:1]
					want = ErrNotFound
				}
			case "borrowed", "borrowed-foreign":
				rows[0].Evidence.Completion.LeaseOwned = false
				rows[0].ACKIdentity = rows[0].Identity()
				lease = l
				if name == "borrowed-foreign" {
					lease.LeaseID = "other"
					want = ErrNotFound
				}
			case "old-intermediate":
				rows[0].Phase = "RETIRED"
				rows[0].ACKIdentity = ""
				want = ErrExecutionConflict
			}
			before := ManagedDigest(rows)
			got, e := SelectOwnedRetirementForRun("root", rows, current, epoch, lease, busy)
			if !errors.Is(e, want) || ManagedDigest(rows) != before {
				t.Fatalf("selector: %v want %v", e, want)
			}
			if want == nil && got.Evidence.Grant != r.Evidence.Grant {
				t.Fatal("wrong current grant")
			}
		})
	}
}

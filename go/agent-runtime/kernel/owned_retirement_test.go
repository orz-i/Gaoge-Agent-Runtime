package kernel

import (
	"encoding/json"
	"errors"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"strings"
	"testing"
)

func retirementFixture() (OwnedRetirementReceipt, ExecutionLease) {
	l := ExecutionLease{RunID: "root", OwnerID: "owner", LeaseID: "lease", Epoch: 1}
	g := ChildExecutionGrant{ID: "scope", ChildRunID: "root", ExpectedRevision: 1, ExecutionOwnerID: l.OwnerID, ExecutionLeaseID: l.LeaseID, ExecutionEpoch: l.Epoch, AdmissionSourceHash: strings.Repeat("a", 64), AdmissionOperation: "start"}
	s := Snapshot{Run: Run{ID: "root", Kind: "agent", Status: RunStatusRunning, Revision: 2}, State: json.RawMessage(`{"receipt":"original"}`)}
	c := OwnedPhysicalCallbackCompletion{GrantID: g.ID, ReturnRevision: 2, ReturnSourceHash: strings.Repeat("b", 64), HostCompleted: true, LeaseOwned: true}
	return OwnedRetirementReceipt{Version: 1, Evidence: OwnedSettlementEvidence{Version: 1, Grant: g, Snapshot: s, Completion: c, MarkOrigin: "OWNER_RETURN"}, Closures: []ManagedTransfer{}, Phase: "RETIRED"}, l
}
func TestOwnedRetirementWireAndCanonicalACK(t *testing.T) {
	r, l := retirementFixture()
	g := r.Evidence.Grant
	b, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	var decoded OwnedRetirementReceipt
	if e = json.Unmarshal(b, &decoded); e != nil || !decoded.Valid() || decoded.Identity() != r.Identity() {
		t.Fatalf("wire: %v", e)
	}
	proof := OwnedRetirementAcknowledgement{proof: featurecontrol.Seal(g.ChildRunID, r.Evidence.Snapshot.Run.Revision, "agent_owned_retirement_ack", g.ID, OwnedRetirementAcknowledgementPayload(r), nil)}
	for _, kind := range []string{"nil-proof", "epoch", "source", "replacement", "lease"} {
		t.Run(kind, func(t *testing.T) {
			p, current, epoch, lease, busy := proof, r.Evidence.Snapshot, l.Epoch, l, false
			switch kind {
			case "nil-proof":
				p = OwnedRetirementAcknowledgement{}
			case "epoch":
				epoch++
			case "source":
				current.Run.Revision++
			case "replacement":
				busy = true
			case "lease":
				lease.OwnerID = "foreign"
			}
			next, _, release, e := ReduceOwnedRetirementACK(r, g, p, current, epoch, lease, busy)
			if !errors.Is(e, ErrExecutionConflict) || release || ManagedDigest(next) != ManagedDigest(r) {
				t.Fatal("invalid first ACK changed authority")
			}
		})
	}
	ack, result, release, e := ReduceOwnedRetirementACK(r, g, proof, r.Evidence.Snapshot, l.Epoch, l, false)
	if e != nil || !release || result.PreviouslyAcknowledged || !ack.Valid() {
		t.Fatalf("first ACK: %v", e)
	}
	b, e = json.Marshal(ack)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &decoded); e != nil || !decoded.Valid() {
		t.Fatalf("ACK wire: %v", e)
	}
	changed := r.Evidence.Snapshot
	changed.Run.Revision++
	again, result, release, e := ReduceOwnedRetirementACK(ack, g, proof, changed, l.Epoch+1, ExecutionLease{}, true)
	if e != nil || release || !result.PreviouslyAcknowledged || ManagedDigest(again) != ManagedDigest(ack) {
		t.Fatal("historical ACK mutated or borrowed new authority")
	}
	bad := r
	bad.Closures = nil
	if bad.Valid() {
		t.Fatal("null closures accepted")
	}
	bad = r
	bad.Phase = "ACKNOWLEDGED"
	if bad.Valid() {
		t.Fatal("missing ACK identity accepted")
	}
	bad = r
	bad.Evidence.Completion = OwnedPhysicalCallbackCompletion{}
	bad.Evidence.MarkOrigin = "LEGACY_UNKNOWN"
	if !bad.Valid() || proof.Matches(g, bad) {
		t.Fatal("legacy marker fabricated release authority")
	}
}

func TestOwnedRetirementRunSelector(t *testing.T) {
	r, l := retirementFixture()
	for _, name := range []string{"unique", "absent", "acked", "ambiguous", "corrupt", "source", "epoch", "replacement", "foreign-lease"} {
		t.Run(name, func(t *testing.T) {
			rows := []OwnedRetirementReceipt{r}
			current := r.Evidence.Snapshot
			epoch := l.Epoch
			lease := l
			busy := false
			var want error
			switch name {
			case "absent":
				rows = nil
				want = ErrNotFound
			case "acked":
				rows[0].Phase = "ACKNOWLEDGED"
				rows[0].ACKIdentity = rows[0].Identity()
				want = ErrNotFound
			case "ambiguous":
				other := r
				other.Evidence.Grant.ID = "second"
				other.Evidence.Completion.GrantID = "second"
				rows = append(rows, other)
				want = ErrExecutionConflict
			case "corrupt":
				rows[0].Evidence.Completion.ReturnSourceHash = "bad"
				want = ErrExecutionConflict
			case "source":
				current.Run.Revision++
				want = ErrExecutionConflict
			case "epoch":
				epoch++
				want = ErrExecutionConflict
			case "replacement":
				busy = true
				want = ErrExecutionConflict
			case "foreign-lease":
				lease.LeaseID = "other"
				want = ErrExecutionConflict
			}
			selected, e := SelectOwnedRetirementForRun("root", rows, current, epoch, lease, busy)
			if !errors.Is(e, want) {
				t.Fatalf("got %v want %v", e, want)
			}
			if want == nil && selected.Identity() != r.Identity() {
				t.Fatal("selected wrong original scope")
			}
		})
	}
}

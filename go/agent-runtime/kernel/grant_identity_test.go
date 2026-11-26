package kernel

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestManagedGrantIdentitiesPreserveRetainedHistory(t *testing.T) {
	original, s, l, g, a, now := managedReducerFixture()
	parked, e := ManagedParkTransfer(original, ManagedParkRequest{RunID: s.Run.ID, SuspensionID: original.SafePoint.ID, ExpectedRevision: s.Run.Revision}, s, l, g, a, now)
	if e != nil {
		t.Fatal(e)
	}
	r := ManagedHostReservation{ID: "reservation", RecoveryID: parked.SafePoint.RecoveryID, SuspensionID: parked.SafePoint.ID, Binding: parked.SafePoint.Host, Mode: ManagedResume, Revision: 1}
	proof := ManagedSelectionProof{reservation: r, transfer: parked, valid: true}
	enabled, e := ManagedEnabledTransfer(parked, ManagedEnableRequest{RunID: s.Run.ID, SuspensionID: parked.SafePoint.ID, ClaimantID: "claimant", OwnerID: "new-owner", LeaseID: "new-lease", GrantID: "next-grant", ExpectedTransferRevision: parked.Revision, Generation: parked.Generation, LeaseDuration: time.Minute, Selection: proof}, s, l.Epoch, now)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(enabled)
	if e != nil {
		t.Fatal(e)
	}
	ids, e := ManagedGrantIdentities(raw, s.Run.ID)
	if e != nil {
		t.Fatal(e)
	}
	got := map[string]string{}
	for _, id := range ids {
		got[id.GrantID] = id.RunID
	}
	if len(got) != 2 || got[g.ID] != s.Run.ID || got[enabled.SuccessorGrant.ID] != s.Run.ID {
		t.Fatalf("retained identities: %+v", ids)
	}
	if _, e = ManagedGrantIdentities(raw, "foreign"); !errors.Is(e, ErrExecutionConflict) {
		t.Fatalf("foreign history: %v", e)
	}
}

func TestMergeGrantIdentities(t *testing.T) {
	active := []GrantIdentity{
		{GrantID: "g-b", RunID: "run-1"},
		{GrantID: "g-b", RunID: "run-1"}, // ancestor fence replica
		{GrantID: "g-a", RunID: "run-2"},
	}
	retired := []GrantIdentity{
		{GrantID: "g-c", RunID: "run-3"},
	}
	managed := []GrantIdentity{
		{GrantID: "g-b", RunID: "run-1"},
		{GrantID: "g-c", RunID: "run-3"},
		{GrantID: "g-d", RunID: "run-4"},
	}
	merged, err := MergeGrantIdentities(active, retired, managed)
	if err != nil {
		t.Fatalf("MergeGrantIdentities valid merge failed: %v", err)
	}
	want := []GrantIdentity{
		{GrantID: "g-a", RunID: "run-2"},
		{GrantID: "g-b", RunID: "run-1"},
		{GrantID: "g-c", RunID: "run-3"},
		{GrantID: "g-d", RunID: "run-4"},
	}
	if len(merged) != len(want) {
		t.Fatalf("merged len=%d want=%d (%+v)", len(merged), len(want), merged)
	}
	for i := range want {
		if merged[i] != want[i] {
			t.Fatalf("merged[%d]=%+v want=%+v", i, merged[i], want[i])
		}
	}

	conflicts := []struct {
		name    string
		active  []GrantIdentity
		retired []GrantIdentity
		managed []GrantIdentity
	}{
		{
			name:   "active-cross-run",
			active: []GrantIdentity{{GrantID: "dup", RunID: "r1"}, {GrantID: "dup", RunID: "r2"}},
		},
		{
			name:    "active-retired-overlap-same-run",
			active:  []GrantIdentity{{GrantID: "dup", RunID: "r1"}},
			retired: []GrantIdentity{{GrantID: "dup", RunID: "r1"}},
		},
		{
			name:    "retired-cross-run",
			retired: []GrantIdentity{{GrantID: "dup", RunID: "r1"}, {GrantID: "dup", RunID: "r2"}},
		},
		{
			name:    "managed-vs-active-cross-run",
			active:  []GrantIdentity{{GrantID: "dup", RunID: "r1"}},
			managed: []GrantIdentity{{GrantID: "dup", RunID: "r2"}},
		},
		{
			name:    "managed-vs-retired-cross-run",
			retired: []GrantIdentity{{GrantID: "dup", RunID: "r1"}},
			managed: []GrantIdentity{{GrantID: "dup", RunID: "r2"}},
		},
		{
			name:    "managed-cross-run",
			managed: []GrantIdentity{{GrantID: "dup", RunID: "r1"}, {GrantID: "dup", RunID: "r2"}},
		},
		{
			name:   "invalid-empty-active",
			active: []GrantIdentity{{GrantID: "", RunID: "r1"}},
		},
		{
			name:    "invalid-whitespace-retired",
			retired: []GrantIdentity{{GrantID: " g1 ", RunID: "r1"}},
		},
		{
			name:    "invalid-whitespace-managed",
			managed: []GrantIdentity{{GrantID: "g1", RunID: " r1"}},
		},
	}
	for _, tc := range conflicts {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := MergeGrantIdentities(tc.active, tc.retired, tc.managed); !errors.Is(err, ErrExecutionConflict) {
				t.Fatalf("got (%+v, %v), want ErrExecutionConflict", got, err)
			}
		})
	}
}

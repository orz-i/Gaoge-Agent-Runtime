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

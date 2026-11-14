package kernel

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func managedCopyFixture() ManagedTransfer {
	now := time.Now() // Keep the monotonic component: copying is not serialization.
	ended := time.Date(2026, 1, 2, 3, 4, 5, 6, time.FixedZone("fixture", 3600))
	raw := func() json.RawMessage { return json.RawMessage(`{"z":9007199254740993,"a":1}`) }
	snapshot := Snapshot{Run: Run{ID: "run", DeadlineAt: &now, EndedAt: &ended, CreatedAt: now, UpdatedAt: ended},
		State: raw(), Checkpoint: &Checkpoint{ID: "cp", Payload: raw(), Response: raw(), CreatedAt: now, ResolvedAt: &ended},
		Result: &Result{ContentType: "application/json", Content: raw()}, EventHead: 17}
	binding := ManagedHostBinding{RunID: "run", Source: raw()}
	reservation := ManagedHostReservation{Binding: binding, Cancellation: ManagedCancellation{Payload: raw(), PayloadDigest: "original-digest"}}
	grant := ChildExecutionGrant{ID: "grant", ChildRunID: "run", ExpectedRevision: 3, ExecutionOwnerID: "owner", ExecutionLeaseID: "lease", ExecutionEpoch: 4, AdmissionSourceHash: "source", AdmissionOperation: "prepare"}
	lease := ExecutionLease{RunID: "run", OwnerID: "owner", LeaseID: "lease", Epoch: 4, ExpiresAt: now}
	return ManagedTransfer{SafePoint: ManagedSafePoint{Host: binding, OriginalGrant: grant, OriginalLease: lease, Source: snapshot, LifecycleSource: raw()},
		Selection: reservation, SuccessorGrant: grant, SuccessorLease: lease,
		Activations: []ManagedActivation{{Generation: 2, ClaimantID: "claimant", Grant: grant, Lease: lease, Selection: reservation}}, Terminal: &snapshot}
}

func mutateManagedSnapshot(v *Snapshot) {
	v.State[0] = '['
	v.Checkpoint.Payload[0] = '['
	v.Checkpoint.Response[0] = '['
	v.Result.Content[0] = '['
	*v.Run.DeadlineAt = v.Run.DeadlineAt.Add(time.Hour)
	*v.Run.EndedAt = v.Run.EndedAt.Add(time.Hour)
	*v.Checkpoint.ResolvedAt = v.Checkpoint.ResolvedAt.Add(time.Hour)
	v.Checkpoint.ID = "changed"
	v.Result.ContentType = "changed"
}

func mutateManagedPoint(v *ManagedSafePoint) {
	v.Host.Source[0] = '['
	v.LifecycleSource[0] = '['
	mutateManagedSnapshot(&v.Source)
}

func mutateManagedReservation(v *ManagedHostReservation) {
	v.Binding.Source[0] = '['
	v.Cancellation.Payload[0] = '['
}

func checkManagedCopyIsolation[T managedCloneValue](t *testing.T, original T, mutate func(*T)) {
	t.Helper()
	// Independent bytes, not another invocation of the implementation under test.
	baseline, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	copied := managedClone(original)
	if !reflect.DeepEqual(original, copied) || ManagedDigest(original) != ManagedDigest(copied) {
		t.Fatal("copy changed fields, raw bytes, time representation or digest")
	}
	mutate(&copied)
	unchanged, err := json.Marshal(original)
	if err != nil || !bytes.Equal(baseline, unchanged) {
		t.Fatalf("copy mutation reached original: %v", err)
	}
	// Exercise the reverse direction independently, with a fresh copy and byte oracle.
	copied = managedClone(original)
	baseline, err = json.Marshal(copied)
	if err != nil {
		t.Fatal(err)
	}
	mutate(&original)
	unchanged, err = json.Marshal(copied)
	if err != nil || !bytes.Equal(baseline, unchanged) {
		t.Fatalf("original mutation reached copy: %v", err)
	}
}

func TestManagedFiniteCopiesIsolateAllPersistenceLeaves(t *testing.T) {
	t.Run("binding", func(t *testing.T) {
		checkManagedCopyIsolation(t, managedCopyFixture().SafePoint.Host, func(v *ManagedHostBinding) { v.Source[0] = '[' })
	})
	t.Run("point", func(t *testing.T) { checkManagedCopyIsolation(t, managedCopyFixture().SafePoint, mutateManagedPoint) })
	t.Run("reservation", func(t *testing.T) {
		checkManagedCopyIsolation(t, managedCopyFixture().Selection, mutateManagedReservation)
	})
	t.Run("snapshot", func(t *testing.T) {
		checkManagedCopyIsolation(t, managedCopyFixture().SafePoint.Source, mutateManagedSnapshot)
	})
	t.Run("transfer", func(t *testing.T) {
		checkManagedCopyIsolation(t, managedCopyFixture(), func(v *ManagedTransfer) {
			mutateManagedPoint(&v.SafePoint)
			mutateManagedReservation(&v.Selection)
			mutateManagedReservation(&v.Activations[0].Selection)
			v.Activations[0].ClaimantID = "changed"
			mutateManagedSnapshot(v.Terminal)
		})
	})
}

func TestManagedFiniteCopiesPreserveNilEmptyAndMalformedRaw(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, {}, []byte("null"), []byte("{"), []byte(`{"b":9007199254740993,"a":0}`)} {
		binding := ManagedHostBinding{Source: raw}
		reservation := ManagedHostReservation{Binding: binding, Cancellation: ManagedCancellation{Payload: raw}}
		snapshot := Snapshot{State: raw, Checkpoint: &Checkpoint{Payload: raw, Response: raw}, Result: &Result{Content: raw}}
		point := ManagedSafePoint{Host: binding, Source: snapshot, LifecycleSource: raw}
		for _, activations := range [][]ManagedActivation{nil, {}, {{Selection: reservation}}} {
			transfer := ManagedTransfer{SafePoint: point, Selection: reservation, Activations: activations, Terminal: &snapshot}
			if !reflect.DeepEqual(binding, managedClone(binding)) || !reflect.DeepEqual(reservation, managedClone(reservation)) ||
				!reflect.DeepEqual(snapshot, managedClone(snapshot)) || !reflect.DeepEqual(point, managedClone(point)) || !reflect.DeepEqual(transfer, managedClone(transfer)) {
				t.Fatalf("copy normalized nil/empty/malformed evidence: raw=%#v activations=%#v", raw, activations)
			}
		}
	}
	zero := ManagedTransfer{}
	if !reflect.DeepEqual(zero, managedClone(zero)) {
		t.Fatal("copy allocated absent pointers or slices")
	}
}

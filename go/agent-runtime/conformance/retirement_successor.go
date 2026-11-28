package conformance

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func runRetirementSuccessorClosure(t *testing.T, factory func(*testing.T) ManagedRecoveryStoreFixture, kind string) {
	f := retirementSetup(t, factory)
	retirementCallback(t, f, true, true)
	original := retirementFinish(t, f)
	var newLease kernel.ExecutionLease
	var childGrant kernel.ChildExecutionGrant
	switch kind {
	case "acquire":
		var err error
		newLease, err = f.rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: f.source.Run.ID, OwnerID: "successor", LeaseDuration: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
	case "release":
		if err := f.rt.ReleaseExecution(t.Context(), f.lease); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("retired lease released again: %v", err)
		}
	case "leased-apply":
		if _, err := f.bound.Apply(t.Context(), f.source.Run.ID, f.source.Run.Revision,
			kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"forbiddenOldOwnerWrite":true}`)}); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("retired lease mutated its successor source: %v", err)
		}
	case "cancel":
		if _, err := f.rt.Cancel(t.Context(), f.source.Run.ID, f.source.Run.Revision, "stop after finished callback"); err != nil {
			t.Fatal(err)
		}
	case "descendant-grant":
		child, err := f.rt.Create(t.Context(), kernel.CreateRequest{ID: "successor-child", Kind: "agent", ExecutionRequired: true, ParentRunID: f.source.Run.ID,
			Actor: f.source.Run.Actor, Thread: f.source.Run.Thread, Goal: "new child", State: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		childGrant, err = AdmitChildFixture(t.Context(), f.rt, child.Run.ID, child.Run.Revision)
		if err != nil {
			t.Fatal(err)
		}
	case "quarantine":
		if err := f.rt.QuarantineExecution(t.Context(), kernel.ExecutionQuarantine{RunID: f.source.Run.ID, Revision: f.source.Run.Revision, Generation: 1, Reason: "source_invalid"}); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown successor: %s", kind)
	}
	closed, err := f.rt.LoadOwnedRetirement(t.Context(), f.grant.ID)
	if err != nil || !closed.Valid() || closed.Identity() != original.Identity() {
		t.Fatalf("successor orphaned or rewrote original closure: %+v %v", closed, err)
	}
	// Exact replay is historical confirmation, never authority over the new
	// lease, source, grant or quarantine. Invalid old proofs must still fail.
	before, err := f.rt.Load(t.Context(), f.source.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if observed, err := f.rt.LoadOwnedRetirement(t.Context(), f.grant.ID); err != nil || kernel.ManagedDigest(observed) != kernel.ManagedDigest(original) {
		t.Fatalf("original final receipt changed: %v", err)
	}
	after, err := f.rt.Load(t.Context(), f.source.Run.ID)
	if err != nil || kernel.ManagedDigest(before) != kernel.ManagedDigest(after) {
		t.Fatalf("old confirmation changed current source: %v", err)
	}
	if newLease.OwnerID != "" {
		if err = f.store.CheckExecution(t.Context(), f.source.Run.ID, &newLease); err != nil {
			t.Fatalf("old ACK revoked the successor: %v", err)
		}
	}
	if childGrant.ID != "" {
		actual, err := f.rt.LookupChildExecution(t.Context(), childGrant.ChildRunID)
		if err != nil || actual != childGrant {
			t.Fatalf("old ACK changed child dispatch: %v", err)
		}
	}
}

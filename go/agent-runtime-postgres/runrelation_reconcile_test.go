package postgres

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

func TestRunRelationReconciliationQueryFiltersAndPersistsWatermark(t *testing.T) {
	t.Parallel()
	db := openKernelStoreTestDB(t)
	runtime, err := kernel.New(kernel.Dependencies{Store: NewKernelStore(db)})
	if err != nil {
		t.Fatal(err)
	}
	store := NewRunRelationStore(db)
	create := func(id string) kernel.Snapshot {
		t.Helper()
		snapshot, createErr := runtime.Create(t.Context(), kernel.CreateRequest{
			ID: id, Kind: "test", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "test", ID: "thread"},
			Goal:   "test", State: json.RawMessage("{}"),
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return snapshot
	}
	finish := func(child kernel.Snapshot) {
		t.Helper()
		if _, applyErr := runtime.Apply(t.Context(), child.Run.ID, child.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusCompleted, State: child.State,
			Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage("true")},
		}); applyErr != nil {
			t.Fatal(applyErr)
		}
	}
	put := func(parent, child kernel.Snapshot) {
		t.Helper()
		_, _, putErr := store.Put(t.Context(), runrelation.Relation{
			ParentRunID: parent.Run.ID, ChildRunID: child.Run.ID,
			Kind: runrelation.KindTeamMember, OwnerNodeID: "member",
			CreatedAt: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC),
		})
		if putErr != nil {
			t.Fatal(putErr)
		}
	}
	parentA, childA := create("parent-A"), create("child-A")
	parentB, childB := create("parent-B"), create("child-B")
	parentC, childC := create("parent-C"), create("child-C")
	put(parentA, childA)
	put(parentB, childB)
	put(parentC, childC)
	finish(childA)
	finish(childB)
	// C remains nonterminal and must never enter the candidate query.
	first, err := store.ListReconcileCandidates(t.Context(), "", 1)
	if err != nil || len(first) != 1 || first[0].Relation.ChildRunID != childA.Run.ID {
		t.Fatalf("first candidate page: %#v, %v", first, err)
	}
	second, err := store.ListReconcileCandidates(t.Context(), first[0].Relation.ChildRunID, 1)
	if err != nil || len(second) != 1 || second[0].Relation.ChildRunID != childB.Run.ID {
		t.Fatalf("second candidate page: %#v, %v", second, err)
	}
	third, err := store.ListReconcileCandidates(t.Context(), second[0].Relation.ChildRunID, 1)
	if err != nil || len(third) != 0 {
		t.Fatalf("nonterminal child leaked into recovery: %#v, %v", third, err)
	}
	if err := store.MarkEnqueued(t.Context(), childA.Run.ID, parentA.Run.Revision, 2); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.ListReconcileCandidates(t.Context(), "", 4)
	if err != nil || len(candidates) != 1 || candidates[0].Relation.ChildRunID != childB.Run.ID {
		t.Fatalf("completed handoff not filtered: %#v, %v", candidates, err)
	}
	// Reconstruct the adapter: the watermark is persisted, not Scheduler memory.
	restarted := NewRunRelationStore(db)
	candidates, err = restarted.ListReconcileCandidates(t.Context(), "", 4)
	if err != nil || len(candidates) != 1 || candidates[0].Relation.ChildRunID != childB.Run.ID {
		t.Fatalf("watermark not persisted: %#v, %v", candidates, err)
	}
	// A later parent wait commit must make A eligible again.
	advanced, err := runtime.Apply(t.Context(), parentA.Run.ID, parentA.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: parentA.State,
	})
	if err != nil {
		t.Fatal(err)
	}
	candidates, err = restarted.ListReconcileCandidates(t.Context(), "", 4)
	if err != nil || len(candidates) != 2 || candidates[0].Relation.ChildRunID != childA.Run.ID {
		t.Fatalf("parent wait revision not recovered: %#v, %v", candidates, err)
	}
	if err := restarted.MarkEnqueued(t.Context(), childA.Run.ID, advanced.Run.Revision, 2); err != nil {
		t.Fatal(err)
	}
	if err := restarted.MarkEnqueued(t.Context(), childA.Run.ID, 1, 1); err != nil {
		t.Fatal(err)
	}
	candidates, err = restarted.ListReconcileCandidates(t.Context(), "", 4)
	if err != nil || len(candidates) != 1 || candidates[0].Relation.ChildRunID != childB.Run.ID {
		t.Fatalf("out-of-order writer regressed watermark: %#v, %v", candidates, err)
	}
	// Parent B is terminal: it must not be retried regardless of child status.
	if _, err := runtime.Apply(t.Context(), parentB.Run.ID, parentB.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: parentB.State,
		Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage("true")},
	}); err != nil {
		t.Fatal(err)
	}
	candidates, err = restarted.ListReconcileCandidates(t.Context(), "", 4)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("terminal parent leaked into recovery: %#v, %v", candidates, err)
	}
	for _, size := range []int{0, runrelation.MaxReconcilePageSize + 1} {
		if _, err := store.ListReconcileCandidates(t.Context(), "", size); !errors.Is(err, runrelation.ErrInvalidInput) {
			t.Fatalf("invalid page size %d: %v", size, err)
		}
	}
	if err := store.MarkEnqueued(t.Context(), "missing", 1, 1); !errors.Is(err, runrelation.ErrNotFound) {
		t.Fatalf("missing watermark target: %v", err)
	}
	if err := store.MarkEnqueued(t.Context(), "child-A", 0, 1); !errors.Is(err, runrelation.ErrInvalidInput) {
		t.Fatalf("invalid watermark: %v", err)
	}
}

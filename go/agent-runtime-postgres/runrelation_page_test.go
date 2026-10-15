package postgres

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

func TestRunRelationReconcilePageAdvancesPastFilteredHistory(t *testing.T) {
	db := openKernelStoreTestDB(t)
	runtime, err := kernel.New(kernel.Dependencies{Store: NewKernelStore(db)})
	if err != nil {
		t.Fatal(err)
	}
	store := NewRunRelationStore(db)
	const total = runrelation.MaxReconcilePageSize*2 + 1
	for i := 0; i < total; i++ {
		parentID := fmt.Sprintf("scan-parent-%04d", i)
		childID := fmt.Sprintf("scan-child-%04d", i)
		makeRun := func(id string) kernel.Snapshot {
			t.Helper()
			snapshot, createErr := runtime.Create(t.Context(), kernel.CreateRequest{
				ID: id, Kind: "test", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
				Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "test", State: json.RawMessage("{}"),
			})
			if createErr != nil {
				t.Fatal(createErr)
			}
			return snapshot
		}
		parent := makeRun(parentID)
		child := makeRun(childID)
		if _, err := runtime.Apply(t.Context(), childID, child.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusCompleted, State: child.State,
			Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage("true")},
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Put(t.Context(), runrelation.Relation{
			ParentRunID: parentID, ChildRunID: childID,
			Kind: runrelation.KindTeamMember, OwnerNodeID: "member", CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		if i < total-1 {
			if err := store.MarkEnqueued(t.Context(), childID, parent.Run.Revision, child.Run.Revision+1); err != nil {
				t.Fatal(err)
			}
		}
	}
	cursor := ""
	for i := 0; i < 3; i++ {
		page, err := store.ListReconcilePage(t.Context(), cursor, runrelation.MaxReconcilePageSize)
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 && (len(page.Candidates) != 0 || !page.HasMore) {
			t.Fatalf("filtered historical page %d: %#v", i, page)
		}
		if i == 2 && (len(page.Candidates) != 1 || page.HasMore ||
			page.Candidates[0].Relation.ChildRunID != "scan-child-0256") {
			t.Fatalf("eligible candidate behind filtered pages: %#v", page)
		}
		if page.NextCursor <= cursor {
			t.Fatalf("cursor did not advance at page %d: %q", i, page.NextCursor)
		}
		cursor = page.NextCursor
	}
}

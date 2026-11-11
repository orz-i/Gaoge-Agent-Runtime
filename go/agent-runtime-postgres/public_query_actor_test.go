package postgres

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestPublicQueriesSameTenantActorIsolation(t *testing.T) {
	store := NewKernelStore(openPublicKernelDB(t))
	for _, actor := range []string{"owner", "other"} {
		record := snapshotTestRecord(actor)
		record.Run.Actor.ActorID = actor
		if _, err := store.Create(t.Context(), record, []kernel.EventDraft{{Type: "run.created"}}); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := store.ListRuns(t.Context(), RunQuery{TenantID: "tenant", ActorID: "owner", Limit: 10})
	if err != nil || len(runs) != 1 || runs[0].Run.ID != "owner" {
		t.Fatalf("runs=%v err=%v", runs, err)
	}
	rows, total, err := store.ListEventMetadata(t.Context(), EventQuery{TenantID: "tenant", ActorID: "owner", Limit: 10})
	if err != nil || total != 1 || len(rows) != 1 || rows[0].ActorID != "owner" {
		t.Fatalf("rows=%v total=%d err=%v", rows, total, err)
	}
	counts, err := store.CountEventTypes(t.Context(), EventQuery{TenantID: "tenant", ActorID: "owner"})
	if err != nil || len(counts) != 1 || counts[0].Count != 1 {
		t.Fatalf("counts=%v err=%v", counts, err)
	}
}

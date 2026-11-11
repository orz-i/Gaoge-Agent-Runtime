package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestPublicQueriesScopePaginationAndMetadata(t *testing.T) {
	db := openPublicKernelDB(t)
	store := NewKernelStore(db)
	for _, id := range []string{"a", "b", "foreign"} {
		record := snapshotTestRecord(id)
		if id == "foreign" {
			record.Run.Actor.TenantID = "other"
		}
		if _, err := store.Create(t.Context(), record, []kernel.EventDraft{{Type: "run.created", Message: "secret prompt"}}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.ListRuns(t.Context(), RunQuery{TenantID: "tenant", Limit: 1})
	if err != nil || len(page) != 1 || page[0].Run.ID != "a" {
		t.Fatalf("page=%v err=%v", page, err)
	}
	page, err = store.ListRuns(t.Context(), RunQuery{TenantID: "tenant", AfterID: "a", Limit: 1})
	if err != nil || len(page) != 1 || page[0].Run.ID != "b" {
		t.Fatalf("page=%v err=%v", page, err)
	}
	if _, err = store.ListRuns(t.Context(), RunQuery{Limit: 1}); !errors.Is(err, kernel.ErrInvalidInput) {
		t.Fatal(err)
	}
	events, total, err := store.ListEventMetadata(t.Context(), EventQuery{TenantID: "tenant", Limit: 1, Ascending: true})
	if err != nil || total != 2 || len(events) != 1 || events[0].Type != "run.created" {
		t.Fatalf("events=%v total=%d err=%v", events, total, err)
	}
	_, total, err = store.ListEventMetadata(t.Context(), EventQuery{TenantID: "tenant", Limit: 10, RunIDContains: "%"})
	if err != nil || total != 0 {
		t.Fatalf("wildcard was not literal: %d %v", total, err)
	}
	counts, err := store.CountEventTypes(t.Context(), EventQuery{TenantID: "tenant", RunKind: "agent"})
	if err != nil || len(counts) != 1 || counts[0].Count != 2 {
		t.Fatalf("counts=%v err=%v", counts, err)
	}
	before := time.Now().Add(-time.Hour)
	_, total, err = store.ListEventMetadata(t.Context(), EventQuery{TenantID: "tenant", Limit: 10, CreatedTo: &before})
	if err != nil || total != 0 {
		t.Fatalf("time filter=%d %v", total, err)
	}
}

func TestPublicEventInclusiveAndExclusiveBoundary(t *testing.T) {
	db := openPublicKernelDB(t)
	store := NewKernelStore(db)
	record := snapshotTestRecord("boundary")
	if _, err := store.Create(t.Context(), record, []kernel.EventDraft{{Type: "run.created"}}); err != nil {
		t.Fatal(err)
	}
	rows, _, err := store.ListEventMetadata(t.Context(), EventQuery{TenantID: "tenant", Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("initial rows=%v err=%v", rows, err)
	}
	boundary := rows[0].CreatedAt
	_, inclusive, err := store.ListEventMetadata(t.Context(), EventQuery{TenantID: "tenant", CreatedThrough: &boundary, Limit: 10})
	if err != nil || inclusive != 1 {
		t.Fatalf("inclusive=%d err=%v", inclusive, err)
	}
	_, exclusive, err := store.ListEventMetadata(t.Context(), EventQuery{TenantID: "tenant", CreatedTo: &boundary, Limit: 10})
	if err != nil || exclusive != 0 {
		t.Fatalf("exclusive=%d err=%v", exclusive, err)
	}
	if _, _, err = store.ListEventMetadata(t.Context(), EventQuery{TenantID: "tenant", CreatedTo: &boundary, CreatedThrough: &boundary, Limit: 10}); !errors.Is(err, kernel.ErrInvalidInput) {
		t.Fatalf("conflicting bounds=%v", err)
	}
}

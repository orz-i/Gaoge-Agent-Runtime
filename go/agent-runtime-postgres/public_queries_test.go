package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
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

func TestPublicActorErasureRequiresDrainAndIsScoped(t *testing.T) {
	db := openPublicKernelDB(t)
	store := NewKernelStore(db)
	for _, id := range []string{"owned", "foreign"} {
		record := snapshotTestRecord(id)
		record.Run.Status = kernel.RunStatusCompleted
		if id == "foreign" {
			record.Run.Actor.TenantID = "other"
		}
		if _, err := store.Create(t.Context(), record, []kernel.EventDraft{{Type: "run.completed"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.EraseActorData(t.Context(), "tenant", "actor"); !errors.Is(err, ErrActorActive) {
		t.Fatalf("outbox not protected: %v", err)
	}
	if err := db.Where("run_id = ?", "owned").Delete(&models.KernelTransitionOutboxRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.KernelRunRecord{}).Where("run_id = ?", "owned").Update("status", "paused_budget").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.EraseActorData(t.Context(), "tenant", "actor"); !errors.Is(err, ErrActorActive) {
		t.Fatalf("paused not protected: %v", err)
	}
	if err := db.Model(&models.KernelRunRecord{}).Where("run_id = ?", "owned").Update("status", "completed").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.EraseActorData(t.Context(), "tenant", "actor"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(t.Context(), "owned"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("owned remains: %v", err)
	}
	if _, err := store.Load(t.Context(), "foreign"); err != nil {
		t.Fatalf("foreign removed: %v", err)
	}
	var n int64
	db.Model(&models.KernelEventRecord{}).Where("run_id = ?", "owned").Count(&n)
	if n != 0 {
		t.Fatal("orphan events")
	}
}

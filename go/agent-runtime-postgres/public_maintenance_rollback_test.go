package postgres

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
)

func TestPublicActorErasureOuterRollbackAndSameTenantIsolation(t *testing.T) {
	db := openPublicKernelDB(t)
	store := NewKernelStore(db)
	for _, id := range []string{"owned", "child", "other-actor"} {
		record := snapshotTestRecord(id)
		record.Run.Status = kernel.RunStatusCompleted
		if id == "other-actor" {
			record.Run.Actor.ActorID = "other"
		}
		if _, err := store.Create(t.Context(), record, []kernel.EventDraft{{Type: "run.completed"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Where("run_id IN ?", []string{"owned", "child"}).Delete(&models.KernelTransitionOutboxRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.RunRelationRecord{ParentRunID: "owned", ChildRunID: "child", Kind: "workflow", OwnerNodeID: "step"}).Error; err != nil {
		t.Fatal(err)
	}
	abort := errors.New("product account deletion failed")
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := NewKernelStore(tx).EraseActorData(t.Context(), "tenant", "actor"); err != nil {
			return err
		}
		var n int64
		if err := tx.Model(&models.KernelRunRecord{}).Where("actor_id = ?", "actor").Count(&n).Error; err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("owned rows still visible inside erasure transaction")
		}
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatalf("outer rollback=%v", err)
	}
	for _, entry := range []struct {
		row  any
		want int64
	}{
		{&models.KernelRunRecord{}, 3}, {&models.KernelEventRecord{}, 3}, {&models.RunRelationRecord{}, 1}, {&models.KernelTransitionOutboxRecord{}, 1},
	} {
		var n int64
		if err := db.Model(entry.row).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != entry.want {
			t.Fatalf("%T after rollback=%d want %d", entry.row, n, entry.want)
		}
	}
	runs, err := store.ListRuns(t.Context(), RunQuery{TenantID: "tenant", ActorID: "actor", Limit: 10})
	if err != nil || len(runs) != 2 {
		t.Fatalf("actor query=%v %v", runs, err)
	}
	if err = store.EraseActorData(t.Context(), "tenant", "actor"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Load(t.Context(), "other-actor"); err != nil {
		t.Fatalf("same-tenant foreign actor erased: %v", err)
	}
	events, total, err := store.ListEventMetadata(t.Context(), EventQuery{TenantID: "tenant", ActorID: "other", Limit: 10})
	if err != nil || total != 1 || len(events) != 1 {
		t.Fatalf("foreign events=%v total=%d err=%v", events, total, err)
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

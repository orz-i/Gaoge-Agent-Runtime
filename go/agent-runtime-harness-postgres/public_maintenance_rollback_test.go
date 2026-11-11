package harnesspostgres

import (
	"errors"
	"testing"

	"gorm.io/gorm"
)

func TestPublicHarnessErasureOuterRollbackAndSameTenantIsolation(t *testing.T) {
	store, db := publicQueryStore(t)
	for _, actor := range []string{"owner", "other"} {
		for _, row := range []any{
			&sessionRecord{ID: actor, TenantID: "tenant", ActorID: actor, HostThreadKind: "conversation", HostThreadID: actor},
			&turnRecord{ID: "turn-" + actor, SessionID: actor, HostTurnKind: "turn", HostTurnID: actor, Status: "completed", ConfigSnapshotID: "config-" + actor},
			&configRecord{ID: "config-" + actor, TurnID: "turn-" + actor, ContentHash: actor, PayloadJSON: `{"id":"config-` + actor + `","turnID":"turn-` + actor + `"}`},
			&invocationRecord{ID: "inv-" + actor, TurnID: "turn-" + actor, Status: "completed"},
			&interactionRecord{ID: "interaction-" + actor, TurnID: "turn-" + actor, InvocationID: "inv-" + actor, Status: "resolved"},
			&contextCheckpointRecord{ID: "checkpoint-" + actor, ScopeID: actor},
			&contextHeadRecord{ScopeID: actor, CheckpointID: "checkpoint-" + actor},
			&contextArtifactRecord{ID: "artifact-" + actor, ScopeID: actor},
			&itemRecord{ID: "item-" + actor, TurnID: "turn-" + actor, Seq: 1},
			&budgetRecord{ID: "turn-" + actor, PayloadJSON: `{"id":"turn-` + actor + `"}`},
		} {
			if err := db.Create(row).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	abort := errors.New("product account deletion failed")
	err := db.Transaction(func(tx *gorm.DB) error {
		transactional, err := New(tx)
		if err != nil {
			return err
		}
		if err = transactional.EraseActorData(t.Context(), "tenant", "owner"); err != nil {
			return err
		}
		var n int64
		if err = tx.Model(&sessionRecord{}).Where("actor_id = ?", "owner").Count(&n).Error; err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("owner remains inside erasure transaction")
		}
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatalf("outer rollback=%v", err)
	}
	ownedTables := []any{&sessionRecord{}, &turnRecord{}, &configRecord{}, &invocationRecord{}, &interactionRecord{}, &contextCheckpointRecord{}, &contextHeadRecord{}, &contextArtifactRecord{}, &itemRecord{}, &budgetRecord{}}
	for _, row := range ownedTables {
		var n int64
		if err = db.Model(row).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Fatalf("%T rollback count=%d want 2", row, n)
		}
	}
	projected, err := store.ListTurnConfigs(t.Context(), TurnConfigQuery{TenantID: "tenant", ActorID: "owner", TurnIDs: []string{"turn-owner", "turn-other"}})
	if err != nil || len(projected) != 1 || projected[0].TurnID != "turn-owner" {
		t.Fatalf("scope=%v err=%v", projected, err)
	}
	if err = store.EraseActorData(t.Context(), "tenant", "owner"); err != nil {
		t.Fatal(err)
	}
	for _, row := range ownedTables {
		var n int64
		if err = db.Model(row).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("%T foreign preservation count=%d want 1", row, n)
		}
	}
	var remaining sessionRecord
	if err = db.First(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining.ActorID != "other" {
		t.Fatalf("wrong surviving actor=%s", remaining.ActorID)
	}
}

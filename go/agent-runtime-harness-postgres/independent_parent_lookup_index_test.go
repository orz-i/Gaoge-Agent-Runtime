package harnesspostgres_test

import (
	"strings"
	"testing"

	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
)

// The original persisted parent identity is globally unique, and cross-Turn
// corruption forces readers to query by parent_item_id alone. Without an
// index this required correctness check scans every stored Item across
// unrelated Turns, unbounded by its LIMIT 2 return count.
func TestIndependentGlobalParentLookupIsIndexedForAllStores(t *testing.T) {
	_, db := newStoreWithDB(t)
	if !db.Migrator().HasIndex("agent_harness_items", "idx_harness_item_parent") {
		t.Fatal("original-identity global parent lookup has no database index")
	}
	var plans []struct {
		Detail string `gorm:"column:detail"`
	}
	if err := db.Raw(`EXPLAIN QUERY PLAN SELECT id, turn_id, kind, status FROM agent_harness_items WHERE parent_item_id = ? LIMIT 2`, "first-party-source").Scan(&plans).Error; err != nil {
		t.Fatal(err)
	}
	usedIndex := false
	for _, plan := range plans {
		if strings.Contains(plan.Detail, "idx_harness_item_parent") && !strings.Contains(plan.Detail, "SCAN agent_harness_items") {
			usedIndex = true
		}
	}
	if !usedIndex {
		t.Fatalf("global sibling query does not seek the original parent index: %+v", plans)
	}
}

// Existing deployments may have the old item table without this index.
// Migrate must backfill the missing index without deleting existing Items.
func TestIndependentGlobalParentIndexMigrationUpgradesLegacyStore(t *testing.T) {
	store, db := newStoreWithDB(t)
	if err := db.Migrator().DropIndex("agent_harness_items", "idx_harness_item_parent"); err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasIndex("agent_harness_items", "idx_harness_item_parent") {
		t.Fatal("pre-upgrade index not actually missing")
	}
	if err := db.Exec(`INSERT INTO agent_harness_items
		(id,turn_id,seq,kind,status,run_id,invocation_id,parent_item_id,payload_json,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		"pre-upgrade-sibling", "legacy-item-turn", 1, "artifact", "completed", "", "",
		"pre-upgrade-source", `{}`, "2026-11-10 00:00:00", "2026-11-10 00:00:00").Error; err != nil {
		t.Fatal(err)
	}
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatalf("legacy migration could not install global parent index: %v", err)
	}
	if !db.Migrator().HasIndex("agent_harness_items", "idx_harness_item_parent") {
		t.Fatal("new parent index absent after existing-store migration")
	}
	if original, err := store.GetItem(t.Context(), "pre-upgrade-sibling"); err != nil || original.ParentItemID != "pre-upgrade-source" {
		t.Fatalf("legacy immutable Item was lost/changed after migration: %+v err=%v", original, err)
	}
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatalf("second migration was not idempotent: %v", err)
	}
}

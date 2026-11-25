package postgres

import (
	"errors"
	"os"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
)

func verifyGrantIdentityMigration(t *testing.T, db *gorm.DB) {
	t.Helper()
	if e := db.Migrator().DropTable(&models.KernelGrantIdentityRecord{}); e != nil {
		t.Fatal(e)
	}
	// Old schema: ancestor replicas share one grant; a second child with the
	// same ID is invalid. Migration must not silently pick an owner.
	rows := []models.KernelChildEffectGrantRecord{
		{GrantID: "borrowed", ParentRunID: "parent", ChildRunID: "child", StartRevision: 1},
		{GrantID: "borrowed", ParentRunID: "child", ChildRunID: "child", StartRevision: 1},
		{GrantID: "borrowed", ParentRunID: "foreign", ChildRunID: "foreign", StartRevision: 1},
	}
	if e := db.Create(&rows).Error; e != nil {
		t.Fatal(e)
	}
	if e := Migrate(db); !errors.Is(e, kernel.ErrExecutionConflict) || db.Migrator().HasTable(&models.KernelGrantIdentityRecord{}) {
		t.Fatalf("ambiguous original identity was silently migrated or partial DDL committed: err=%v", e)
	}
	var count int64
	if e := db.Model(&models.KernelChildEffectGrantRecord{}).Count(&count).Error; e != nil || count != 3 {
		t.Fatalf("migration failure rewrote original fences: %d %v", count, e)
	}
	if e := db.Where("grant_id=? AND parent_run_id=?", "borrowed", "foreign").Delete(&models.KernelChildEffectGrantRecord{}).Error; e != nil {
		t.Fatal(e)
	}
	// The registry records retained identity only, never the historical proof.
	retired := models.KernelOwnedRetirementRecord{GrantID: "retired", RunID: "old-root", SettledRevision: 1, Version: 1, Phase: "ACKNOWLEDGED", Payload: "{}"}
	if e := db.Create(&retired).Error; e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if e := Migrate(db); e != nil {
			t.Fatal(e)
		}
	}
	var ids []models.KernelGrantIdentityRecord
	if e := db.Order("grant_id").Find(&ids).Error; e != nil || len(ids) != 2 || ids[0].GrantID != "borrowed" || ids[0].RunID != "child" || ids[1].GrantID != "retired" || ids[1].RunID != "old-root" {
		t.Fatalf("identity backfill: %+v %v", ids, e)
	}

	// Pre-created empty identity table (e.g. via external AutoMigrate(Models()...))
	// must not cause Migrate to skip backfill.
	if e := db.Migrator().DropTable(&models.KernelGrantIdentityRecord{}); e != nil {
		t.Fatal(e)
	}
	if e := db.AutoMigrate(&models.KernelGrantIdentityRecord{}); e != nil {
		t.Fatal(e)
	}
	if e := Migrate(db); e != nil {
		t.Fatal(e)
	}
	ids = nil
	if e := db.Order("grant_id").Find(&ids).Error; e != nil || len(ids) != 2 || ids[0].GrantID != "borrowed" || ids[1].GrantID != "retired" {
		t.Fatalf("precreated table skipped backfill: %+v %v", ids, e)
	}

	// Active and retired rows with the same GrantID must abort with ErrExecutionConflict.
	overlap := models.KernelOwnedRetirementRecord{GrantID: "borrowed", RunID: "child", SettledRevision: 1, Version: 1, Phase: "ACKNOWLEDGED", Payload: "{}"}
	if e := db.Create(&overlap).Error; e != nil {
		t.Fatal(e)
	}
	if e := Migrate(db); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("active/retired overlap was not rejected: %v", e)
	}
	if e := db.Where("grant_id = ?", "borrowed").Delete(&models.KernelOwnedRetirementRecord{}).Error; e != nil {
		t.Fatal(e)
	}

	// Whitespace-padded grant_id must abort with ErrExecutionConflict.
	badID := models.KernelOwnedRetirementRecord{GrantID: " bad-id ", RunID: "old-root", SettledRevision: 1, Version: 1, Phase: "ACKNOWLEDGED", Payload: "{}"}
	if e := db.Create(&badID).Error; e != nil {
		t.Fatal(e)
	}
	if e := Migrate(db); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("whitespace grant_id was not rejected: %v", e)
	}
	if e := db.Where("grant_id = ?", " bad-id ").Delete(&models.KernelOwnedRetirementRecord{}).Error; e != nil {
		t.Fatal(e)
	}
	if e := Migrate(db); e != nil {
		t.Fatal(e)
	}
}

func TestGrantIdentityMigrationGORM(t *testing.T) {
	verifyGrantIdentityMigration(t, openKernelStoreTestDB(t))
}

func TestRealPostgresGrantIdentityMigration(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	db, _ := openIsolatedRealPostgres(t, dsn)
	if e := Migrate(db); e != nil {
		t.Fatal(e)
	}
	verifyGrantIdentityMigration(t, db)
}

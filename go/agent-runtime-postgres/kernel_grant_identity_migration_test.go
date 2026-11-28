package postgres

import (
	"errors"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"gorm.io/gorm"
	"os"
	"testing"
)

func verifyLegacyEffectRejection(t *testing.T, db *gorm.DB) {
	for _, table := range []string{"agent_kernel_child_effect_grants", "agent_kernel_grant_identities", "agent_kernel_owned_retirements", "agent_kernel_managed_transfers"} {
		if e := db.Exec("CREATE TABLE " + table + " (identity text PRIMARY KEY, payload text NOT NULL)").Error; e != nil {
			t.Fatal(e)
		}
		if e := db.Exec("INSERT INTO "+table+" VALUES (?,?)", "original", `{"unknown":"retain"}`).Error; e != nil {
			t.Fatal(e)
		}
		if e := Migrate(db); !errors.Is(e, ErrUnsupportedSchema) {
			t.Fatalf("old schema accepted: %v", e)
		}
		var original string
		if e := db.Raw("SELECT payload FROM " + table + " WHERE identity='original'").Scan(&original).Error; e != nil || original != `{"unknown":"retain"}` {
			t.Fatalf("old fact changed: %q %v", original, e)
		}
		if e := db.Exec("DROP TABLE " + table).Error; e != nil {
			t.Fatal(e)
		}
	}
	// Current identity remains permanent through idempotent initialization.
	row := models.KernelEffectRecord{GrantID: "used", ChildRunID: "original"}
	if e := db.Create(&row).Error; e != nil {
		t.Fatal(e)
	}
	if e := Migrate(db); e != nil {
		t.Fatal(e)
	}
	var observed models.KernelEffectRecord
	if e := db.First(&observed, "grant_id=?", "used").Error; e != nil || observed != row {
		t.Fatalf("identity changed: %+v %v", observed, e)
	}
}
func TestLegacyEffectProfileRejectionGORM(t *testing.T) {
	verifyLegacyEffectRejection(t, openKernelStoreTestDB(t))
}
func TestRealPostgresLegacyEffectProfileRejection(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	db, _ := openIsolatedRealPostgres(t, dsn)
	if e := Migrate(db); e != nil {
		t.Fatal(e)
	}
	verifyLegacyEffectRejection(t, db)
}

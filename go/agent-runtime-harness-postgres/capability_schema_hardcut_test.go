package harnesspostgres_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestCapabilitySchemaHardCutCannotReadOrAutoMigrateLegacyRows(t *testing.T) {
	_, db := newStoreWithDB(t)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE agent_harness_invocations ADD COLUMN capability_key TEXT NOT NULL DEFAULT 'runtime.agent'").Error; err != nil {
		t.Fatal(err)
	}
	if err := harnesspostgres.Migrate(db); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("old Capability column was silently migrated into executable Agent records: %v", err)
	}
	if _, err := harnesspostgres.New(db); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("old Capability column was silently loaded as Agent: %v", err)
	}
	if err := db.Exec("ALTER TABLE agent_harness_invocations DROP COLUMN capability_key").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE agent_harness_invocations ADD COLUMN definition_version TEXT NOT NULL DEFAULT 'v1'").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := harnesspostgres.New(db); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("old definition version was silently loaded as Agent: %v", err)
	}
}

// Run the exact manual SQL file on an isolated real PostgreSQL schema. A
// category flag is not enough to prove an old Host Start admission hash: even
// a former runtime.agent row must be retired, never silently resumed. Failure
// must preserve both old columns and original rows transactionally.
func TestCapabilityShellManualCutoverRejectsOldRowsAndAppliesAtomically(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("requires TEST_POSTGRES_DSN")
	}
	db, isolatedDSN := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the stopped historical writer's table for the historical
	// manual SQL. Fresh native Migrate must not create this retired table.
	if err := db.Exec("CREATE TABLE agent_harness_managed_recovery (id TEXT PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"capability_key TEXT NOT NULL DEFAULT 'runtime.agent'", "definition_version TEXT NOT NULL DEFAULT 'v1'"} {
		if err := db.Exec("ALTER TABLE agent_harness_invocations ADD COLUMN " + column).Error; err != nil {
			t.Fatal(err)
		}
	}
	original, err := os.ReadFile(filepath.Join("..", "..", "docs", "migrations", "retire-harness-capability-shell.sql"))
	if err != nil {
		t.Fatal(err)
	}
	// PostgreSQL's simple query protocol supports a transactional script with
	// a DO $$...$$ block; no SQL fragments are reconstructed in the test.
	script, err := gorm.Open(postgres.New(postgres.Config{DSN: isolatedDSN, PreferSimpleProtocol: true}), realHarnessPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	sqlConn, err := script.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlConn.Close() })
	add := func(category string) {
		t.Helper()
		err = db.Exec(`INSERT INTO agent_harness_invocations
            (id,turn_id,capability_key,definition_version,status,attempt,revision,created_at,updated_at)
            VALUES(?, ?, ?, 'v1', 'failed',1,1, NOW(),NOW())`, "old-row", "old-turn", category).Error
		if err != nil {
			t.Fatal(err)
		}
	}
	remove := func() {
		t.Helper()
		if err := db.Exec(`DELETE FROM agent_harness_invocations WHERE id='old-row'`).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, category := range []string{"app.legacy", "runtime.agent"} {
		add(category)
		if err := script.Exec(string(original)).Error; err == nil {
			t.Fatalf("old %s execution evidence was promoted into Agent by SQL cutover", category)
		}
		var n int64
		if err := db.Table("agent_harness_invocations").Where("id=?", "old-row").Count(&n).Error; err != nil || n != 1 {
			t.Fatalf("failed cutover erased original row: count=%d err=%v", n, err)
		}
		if !db.Migrator().HasColumn("agent_harness_invocations", "capability_key") {
			t.Fatal("failed migration partially dropped original identity")
		}
		remove()
	}
	// Even after the old Invocation row is removed, orphaned Host execution
	// facts must not be interpreted as a new Agent's terminal receipt.
	if err := db.Exec(`INSERT INTO agent_harness_items
            (id,turn_id,seq,kind,status,created_at,updated_at)
            VALUES('obsolete-invocation-fact','old-turn',1,'capability_invocation','completed',NOW(),NOW())`).Error; err != nil {
		t.Fatal(err)
	}
	if err := script.Exec(string(original)).Error; err == nil {
		t.Fatal("legacy orphaned Item allowed original execution proof to cross Agent cutover")
	}
	if !db.Migrator().HasColumn("agent_harness_invocations", "capability_key") {
		t.Fatal("orphan rejection partially dropped original SQL columns")
	}
	if err := db.Exec(`DELETE FROM agent_harness_items WHERE id='obsolete-invocation-fact'`).Error; err != nil {
		t.Fatal(err)
	}
	if err := script.Exec(string(original)).Error; err != nil {
		t.Fatalf("empty retired evidence could not complete exact Agent hard cut: %v", err)
	}
	for _, column := range []string{"capability_key", "definition_version"} {
		if db.Migrator().HasColumn("agent_harness_invocations", column) {
			t.Fatalf("retired column %s remains", column)
		}
	}
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatalf("native new-schema Migrate: %v", err)
	}
	if _, err := harnesspostgres.New(db); err != nil {
		t.Fatalf("native new-schema Open: %v", err)
	}
}

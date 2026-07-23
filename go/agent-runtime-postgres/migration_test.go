package postgres

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const (
	migrationContextSnapshot = "snapshot"
	migrationContextArtifact = "artifact"
)

func TestMigrateCreatesOnlyAgentRuntimeV1Tables(t *testing.T) {
	db := openRuntimeMigrationDB(t, "empty")
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{
		"agent_runs", "agent_run_events", "agent_run_steps", "agent_plans", "agent_interactions", "agent_checkpoints",
		"agent_context_records", "agent_output_identities", "agent_output_refs", "agent_evidence", "agent_queue_items",
		"agent_workbench_projections", "agent_phase_projections",
	} {
		if !db.Migrator().HasTable(table) {
			t.Errorf("Agent Runtime migration did not create %s", table)
		}
	}
}

func TestMigrateAgentTablesContainNoHostDatabaseKeys(t *testing.T) {
	db := openRuntimeMigrationDB(t, "no_host_keys")
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{
		"agent_runs", "agent_run_events", "agent_run_steps", "agent_plans", "agent_interactions", "agent_checkpoints",
		"agent_context_records", "agent_output_identities", "agent_output_refs", "agent_evidence", "agent_queue_items",
		"agent_workbench_projections", "agent_phase_projections",
	} {
		for _, column := range []string{"user_id", "conversation_id", "message_id", "attachment_id", "project_id", "environment_profile_id", "upstream_id", "upstream_model_id"} {
			if db.Migrator().HasColumn(table, column) {
				t.Errorf("%s leaked host database key %s", table, column)
			}
		}
	}
}

func TestMigrateRejectsEveryLegacyRuntimeTableWithCleanupHint(t *testing.T) {
	for _, table := range legacyRuntimeTables {
		t.Run(table, func(t *testing.T) {
			db := openRuntimeMigrationDB(t, table)
			if err := db.Exec("CREATE TABLE " + table + " (id integer)").Error; err != nil {
				t.Fatal(err)
			}
			err := Migrate(db)
			if !errors.Is(err, ErrLegacyRuntimeSchema) || !strings.Contains(err.Error(), table) || !strings.Contains(err.Error(), "delete the local database or Docker volume") {
				t.Fatalf("legacy table error = %v", err)
			}
		})
	}
}

func TestMigrateRejectsLegacyRuntimeContextRows(t *testing.T) {
	db := openRuntimeMigrationDB(t, "context")
	if err := db.Exec("CREATE TABLE chat_context_records (id integer, record_type text)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO chat_context_records (record_type) VALUES (?)", "text_run_snapshot").Error; err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); !errors.Is(err, ErrLegacyRuntimeSchema) || !strings.Contains(err.Error(), "chat_context_records") {
		t.Fatalf("legacy context error = %v", err)
	}
}

func TestMigrateRepairsContextArtifactUniqueIndex(t *testing.T) {
	testMigrateRepairsContextArtifactUniqueIndex(t, openRuntimeMigrationDB(t, "context_artifact_index"))
}

func TestMigrateRepairsContextArtifactUniqueIndexPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	testMigrateRepairsContextArtifactUniqueIndex(t, openConversationPostgresContractDB(t, dsn))
}

func testMigrateRepairsContextArtifactUniqueIndex(t *testing.T, db *gorm.DB) {
	t.Helper()
	requirePostgresTestNoError(t, Migrate(db))
	requirePostgresTestNoError(t, db.Exec("DROP INDEX IF EXISTS "+contextArtifactUniqueIndex).Error)
	requirePostgresTestNoError(t, db.Exec("CREATE UNIQUE INDEX "+contextArtifactUniqueIndex+" ON agent_context_records (artifact_id)").Error)
	first := models.ContextRecord{RecordType: migrationContextSnapshot, SnapshotID: "snapshot_existing", RunID: "run_existing"}
	requirePostgresTestNoError(t, db.Create(&first).Error)
	requirePostgresTestNoError(t, Migrate(db))
	requirePostgresTestNoError(t, Migrate(db))
	second := models.ContextRecord{RecordType: migrationContextSnapshot, SnapshotID: "snapshot_next", RunID: "run_next"}
	requirePostgresTestNoError(t, db.Create(&second).Error)
	artifact := models.ContextRecord{RecordType: migrationContextArtifact, SnapshotID: first.SnapshotID, ArtifactID: "artifact_unique", RunID: first.RunID}
	requirePostgresTestNoError(t, db.Create(&artifact).Error)
	duplicate := models.ContextRecord{RecordType: migrationContextArtifact, SnapshotID: second.SnapshotID, ArtifactID: artifact.ArtifactID, RunID: second.RunID}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate artifact ID must remain rejected")
	}
	var snapshots int64
	requirePostgresTestNoError(t, db.Model(&models.ContextRecord{}).Where("record_type = ?", migrationContextSnapshot).Count(&snapshots).Error)
	if snapshots != 2 {
		t.Fatalf("snapshot count = %d, want 2", snapshots)
	}
}

func openRuntimeMigrationDB(t *testing.T, suffix string) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name() + "_" + suffix)
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

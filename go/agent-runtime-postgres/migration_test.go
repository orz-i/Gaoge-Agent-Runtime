package postgres

import (
	"errors"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
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

func openRuntimeMigrationDB(t *testing.T, suffix string) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name() + "_" + suffix)
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

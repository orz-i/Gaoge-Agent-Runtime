package postgres

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const (
	migrationContextSnapshot  = "snapshot"
	migrationContextArtifact  = "artifact"
	migrationOutputProjection = "output"
	migrationStepDone         = "done"
	migrationStepStarted      = "step.started"
	migrationStepStartedID    = "step_started"
	migrationStepCompleted    = "step.completed"
	migrationRunCompleted     = "run.completed"
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

func TestMigrateReplaysHistoricalRunAndStepProjection(t *testing.T) {
	testMigrateReplaysHistoricalRunAndStepProjection(t, openRuntimeMigrationDB(t, "run_projection"))
}

func TestMigrateReplaysHistoricalRunAndStepProjectionPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	testMigrateReplaysHistoricalRunAndStepProjection(t, openConversationPostgresContractDB(t, dsn))
}

func testMigrateReplaysHistoricalRunAndStepProjection(t *testing.T, db *gorm.DB) {
	t.Helper()
	requirePostgresTestNoError(t, Migrate(db))
	started := time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC)
	ended := started.Add(3 * time.Second)
	run := models.RunRecord{
		RunID: "run_historical", TenantID: "default", ActorID: "1",
		ThreadKind: expiryTestThreadConversation, ThreadID: "thread_historical",
		InputProjectionKind: projectionKindMessage, InputProjectionID: "input",
		OutputProjectionKind: projectionKindMessage, OutputProjectionID: migrationOutputProjection,
		Status: domain.RunStatusCompleted, StartedAt: started, EndedAt: &ended,
	}
	requirePostgresTestNoError(t, db.Create(&run).Error)
	step := models.RunStep{RunID: run.RunID, StepID: "step_root", Status: domain.RunStatusQueued}
	requirePostgresTestNoError(t, db.Create(&step).Error)
	for _, event := range historicalProjectionEvents(run, step, started, ended) {
		requirePostgresTestNoError(t, db.Create(&event).Error)
	}

	requirePostgresTestNoError(t, Migrate(db))
	requirePostgresTestNoError(t, Migrate(db))
	var repaired models.RunRecord
	requirePostgresTestNoError(t, db.Where("run_id = ?", run.RunID).Take(&repaired).Error)
	if repaired.StateProjectionVersion != currentStateProjectionVersion || repaired.Status != domain.RunStatusCompleted ||
		repaired.OutputTokens != 256 || repaired.LLMCallsCount != 1 || repaired.TotalLatencyMS != 3000 {
		t.Fatalf("repaired run = %#v", repaired)
	}
	var repairedStep models.RunStep
	requirePostgresTestNoError(t, db.Where("run_id = ? AND step_id = ?", run.RunID, step.StepID).Take(&repairedStep).Error)
	if repairedStep.Status != domain.RunStatusCompleted || repairedStep.ResultSummary != migrationStepDone || repairedStep.EndedAt == nil {
		t.Fatalf("repaired step = %#v", repairedStep)
	}
}

func historicalProjectionEvents(run models.RunRecord, step models.RunStep, started, ended time.Time) []models.EventRecord {
	base := models.EventRecord{
		RunID: run.RunID, TenantID: run.TenantID, ActorID: run.ActorID,
		ThreadKind: run.ThreadKind, ThreadID: run.ThreadID, EventScope: runEventScope,
		StepID: step.StepID, Visibility: visibilityUser,
	}
	startedEvent := base
	startedEvent.EventID, startedEvent.EventType, startedEvent.Seq, startedEvent.StartedAt = migrationStepStartedID, migrationStepStarted, 1, started
	usageEvent := base
	usageEvent.EventID, usageEvent.EventType, usageEvent.Seq = "usage", "usage.updated", 2
	usageEvent.PayloadJSON, usageEvent.StartedAt = `{"outputTokens":256}`, started.Add(time.Second)
	stepEvent := base
	stepEvent.EventID, stepEvent.EventType, stepEvent.Seq = "step_completed", migrationStepCompleted, 3
	stepEvent.Summary, stepEvent.StartedAt, stepEvent.EndedAt = migrationStepDone, ended, &ended
	runEvent := base
	runEvent.EventID, runEvent.EventType, runEvent.Seq = "run_completed", migrationRunCompleted, 4
	runEvent.Summary, runEvent.StartedAt, runEvent.EndedAt = "complete", ended, &ended
	return []models.EventRecord{startedEvent, usageEvent, stepEvent, runEvent}
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

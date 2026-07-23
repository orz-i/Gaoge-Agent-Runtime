package postgres

import (
	"errors"
	"fmt"
	"strings"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	"gorm.io/gorm"
)

var ErrLegacyRuntimeSchema = errors.New("legacy Conversation Text Runtime schema detected; delete the local database or Docker volume before starting")
var ErrNilDatabase = errors.New("agent runtime postgres migrate: nil database")

const contextArtifactUniqueIndex = "uk_agent_context_records_artifact"

var legacyRuntimeTables = []string{
	"chat_runs", "chat_run_events", "text_run_steps", "text_run_plans",
	"text_run_interactions", "text_run_checkpoints", "output_identities", "output_refs",
	"text_run_workbench_projections", "text_run_phase_projections", "evidence_selections",
	"text_run_queue_items",
}

func Models() []interface{} {
	return []interface{}{
		&models.RunRecord{}, &models.EventRecord{}, &models.RunStep{},
		&models.RuntimePlanRecord{}, &models.RunInteraction{}, &models.RunCheckpoint{},
		&models.RuntimeOutputIdentityRecord{}, &models.RuntimeOutputRefRecord{},
		&models.RuntimeWorkbenchProjectionRecord{}, &models.RuntimePhaseProjectionRecord{},
		&models.EvidenceSelection{}, &models.RunQueueItemRecord{}, &models.ContextRecord{},
	}
}

func Migrate(db *gorm.DB, _ ...bool) error {
	if db == nil {
		return ErrNilDatabase
	}
	var found []string
	for _, table := range legacyRuntimeTables {
		if db.Migrator().HasTable(table) {
			found = append(found, table)
		}
	}
	if len(found) > 0 {
		return fmt.Errorf("%w: %s", ErrLegacyRuntimeSchema, strings.Join(found, ", "))
	}
	if err := rejectLegacyContextRows(db); err != nil {
		return err
	}
	if err := db.AutoMigrate(Models()...); err != nil {
		return err
	}
	if err := ensureContextArtifactUniqueIndex(db); err != nil {
		return err
	}
	return reconcileHistoricalRunState(db)
}

func ensureContextArtifactUniqueIndex(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DROP INDEX IF EXISTS " + contextArtifactUniqueIndex).Error; err != nil {
			return err
		}
		return tx.Exec("CREATE UNIQUE INDEX " + contextArtifactUniqueIndex + " ON agent_context_records (artifact_id) WHERE record_type = 'artifact'").Error
	})
}

func rejectLegacyContextRows(db *gorm.DB) error {
	if !db.Migrator().HasTable("chat_context_records") {
		return nil
	}
	if !db.Migrator().HasColumn("chat_context_records", "record_type") {
		return nil
	}
	var count int64
	if err := db.Table("chat_context_records").Where("record_type IN ?", []string{"text_run_snapshot", "text_run_artifact", "run_snapshot", "run_artifact"}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("%w: chat_context_records contains %d runtime rows", ErrLegacyRuntimeSchema, count)
	}
	return nil
}

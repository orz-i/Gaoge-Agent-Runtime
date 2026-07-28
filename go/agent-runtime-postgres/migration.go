package postgres

import (
	"errors"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	"gorm.io/gorm"
)

var ErrNilDatabase = errors.New("agent runtime postgres migrate: nil database")

const contextArtifactUniqueIndex = "uk_agent_context_records_artifact"

func Models() []interface{} {
	return []interface{}{
		&models.RunRecord{}, &models.EventRecord{}, &models.RunStep{},
		&models.RuntimePlanRecord{}, &models.RunInteraction{}, &models.RunCheckpoint{},
		&models.RuntimeOutputIdentityRecord{}, &models.RuntimeOutputRefRecord{},
		&models.RuntimeWorkbenchProjectionRecord{}, &models.RuntimePhaseProjectionRecord{},
		&models.EvidenceSelection{}, &models.RunQueueItemRecord{}, &models.ContinuationJobRecord{}, &models.ContextRecord{},
		&models.AgentManifestRevisionRecord{}, &models.RunHandoffRecord{}, &models.RunHandoffJoinRecord{},
		&models.WorkflowDefinitionRevisionRecord{}, &models.WorkflowExecutionRecord{},
		&models.RunResultRecord{}, &models.WorkflowCacheEntryRecord{},
	}
}

func Migrate(db *gorm.DB, _ ...bool) error {
	if db == nil {
		return ErrNilDatabase
	}
	if err := db.AutoMigrate(Models()...); err != nil {
		return err
	}
	if err := db.Model(&models.RunRecord{}).Where("runtime_kind IS NULL OR runtime_kind = ''").Update("runtime_kind", "text").Error; err != nil {
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

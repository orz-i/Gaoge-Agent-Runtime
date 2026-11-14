package postgres

import (
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrNilDatabase = errors.New("postgres kernel store database is nil")

func Models() []interface{} {
	return []interface{}{
		&models.KernelRunRecord{}, &models.KernelEventRecord{}, &models.KernelTransitionOutboxRecord{},
		&models.KernelChildEffectGrantRecord{},
		&models.KernelOwnedRetirementRecord{},
		&models.KernelManagedAuthorityRecord{}, &models.KernelManagedTransferRecord{}, &models.KernelManagedHistoryRecord{},
		&models.RunRelationRecord{},
		&models.WorkflowDefinitionRevisionRecord{}, &models.WorkflowDefinitionHeadRecord{},
	}
}

func Migrate(db *gorm.DB) error {
	if db == nil {
		return ErrNilDatabase
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(Models()...); err != nil {
			return err
		}
		var namespace [32]byte
		if _, err := rand.Read(namespace[:]); err != nil {
			return err
		}
		row := models.KernelManagedAuthorityRecord{Key: "managed-recovery-authority/v1", Namespace: hex.EncodeToString(namespace[:]), Generation: 1, Version: 1}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
	})
}

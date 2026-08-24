package postgres

import (
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"gorm.io/gorm"
)

var ErrNilDatabase = errors.New("postgres kernel store database is nil")

func Models() []interface{} {
	return []interface{}{
		&models.KernelRunRecord{}, &models.KernelEventRecord{}, &models.RunRelationRecord{},
	}
}

func Migrate(db *gorm.DB) error {
	if db == nil {
		return ErrNilDatabase
	}
	return db.AutoMigrate(Models()...)
}

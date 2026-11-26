package postgres

import (
	"context"
	"encoding/json"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget/sqlledger"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func pgClaimRunIdentity(tx *gorm.DB, id, payload string) (bool, string, error) {
	row := models.KernelRunIdentityRecord{RunID: id, CancellationJSON: payload}
	result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if result.Error != nil {
		return false, "", translateKernelError(result.Error)
	}
	if result.RowsAffected == 1 {
		return true, payload, nil
	}
	if err := tx.Where("run_id=?", id).Take(&row).Error; err != nil {
		return false, "", translateKernelError(err)
	}
	return false, row.CancellationJSON, nil
}

func pgReserveRunIdentity(tx *gorm.DB, id string) error {
	fresh, prior, err := pgClaimRunIdentity(tx, id, "")
	if err != nil || fresh {
		return err
	}
	if _, err = kernel.DecodeCreationCancellation(id, prior); err != nil {
		return err
	}
	return kernel.ErrCreationCancelled
}

func (s *KernelStore) CancelBeforeCreate(ctx context.Context, q kernel.RunCreationCancellation) (kernel.RunCreationCancellation, error) {
	var zero, result kernel.RunCreationCancellation
	if s == nil || s.db == nil || ctx == nil || !q.Valid() {
		return zero, kernel.ErrInvalidInput
	}
	raw, err := json.Marshal(q)
	if err != nil {
		return zero, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		fresh, prior, e := pgClaimRunIdentity(tx, q.RunID, string(raw))
		if e != nil {
			return e
		}
		var existing int64
		if e = tx.Model(&models.KernelRunRecord{}).Where("run_id=?", q.RunID).Count(&existing).Error; e != nil {
			return translateKernelError(e)
		}
		if existing != 0 {
			return kernel.ErrAlreadyExists
		}
		result, e = kernel.DecodeCreationCancellation(q.RunID, prior)
		if e != nil {
			return e
		}
		if !result.MatchesSelection(q) {
			return kernel.ErrConflict
		}
		if !fresh {
			return nil
		}
		return budget.CancelBeforeEnrollment(ctx, sqlledger.Store{DB: tx.Statement.ConnPool, Lock: tx.Name() == "postgres"}, q.RunID, q.Budget)
	})
	if err != nil {
		return zero, err
	}
	return result, nil
}

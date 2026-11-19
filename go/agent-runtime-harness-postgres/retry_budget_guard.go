package harnesspostgres

import (
	"context"
	"encoding/json"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"gorm.io/gorm"
)

// retryBudgetRequiredTx is a read of the ORIGINAL Turn's budget authority
// inside the very same Turn-locked Retry transaction. Both its immutable
// frozen shared-budget request and any existing durable ledger require the
// trusted Runner's private admission witness. Otherwise a direct Store Retry
// can rotate Attempt while leaving the cancelled ledger permanently closed.
func retryBudgetRequiredTx(ctx context.Context, tx *gorm.DB, turnID string) (bool, error) {
	var bound int64
	if err := tx.WithContext(ctx).Model(&budgetRecord{}).
		Where("id = ?", turnID).Count(&bound).Error; err != nil {
		return false, err
	}
	if bound != 0 {
		return true, nil
	}
	var turns []turnRecord
	if err := tx.WithContext(ctx).Where("id = ?", turnID).Limit(1).Find(&turns).Error; err != nil {
		return false, err
	}
	if len(turns) == 0 {
		return false, harness.ErrConflict
	}
	var configs []configRecord
	if err := tx.WithContext(ctx).Where("id = ?", turns[0].ConfigSnapshotID).
		Limit(1).Find(&configs).Error; err != nil {
		return false, err
	}
	// Standalone Store conformance may persist Turns with arbitrary external
	// Config IDs. Such unbudgeted pre-admission fixtures have no ledger to
	// skip. A real sealed Turn must still be checked by the Harness Runner.
	if len(configs) == 0 {
		return false, nil
	}
	var config harness.ConfigSnapshot
	if err := json.Unmarshal([]byte(configs[0].PayloadJSON), &config); err != nil ||
		config.ID != turns[0].ConfigSnapshotID || config.TurnID != turnID {
		return false, harness.ErrConflict
	}
	return config.SharedBudget != nil, nil
}

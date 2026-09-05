package harnesspostgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type budgetRecord struct {
	ID          string `gorm:"primaryKey;size:64"`
	Revision    uint64 `gorm:"not null"`
	PayloadJSON string `gorm:"type:text;not null"`
}

func (budgetRecord) TableName() string { return "agent_harness_budgets" }

func (store *Store) LoadBudget(ctx context.Context, id string) (budget.Ledger, error) {
	var record budgetRecord
	err := store.db.WithContext(ctx).Where("id = ?", id).Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return budget.Ledger{}, budget.ErrLedgerNotFound
	}
	if err != nil {
		return budget.Ledger{}, err
	}
	var ledger budget.Ledger
	if err = json.Unmarshal([]byte(record.PayloadJSON), &ledger); err != nil {
		return budget.Ledger{}, err
	}
	if ledger.ID != record.ID || ledger.Revision != record.Revision {
		return budget.Ledger{}, budget.ErrLedgerConflict
	}
	return ledger, nil
}

func (store *Store) SaveBudget(ctx context.Context, ledger budget.Ledger, revision uint64) error {
	if ledger.ID == "" || ledger.Revision != revision+1 || !budget.ValidLimits(ledger.Limits) {
		return budget.ErrInvalidUsage
	}
	raw, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	record := budgetRecord{ID: ledger.ID, Revision: ledger.Revision, PayloadJSON: string(raw)}
	var result *gorm.DB
	if revision == 0 {
		result = store.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	} else {
		result = store.db.WithContext(ctx).Model(&budgetRecord{}).Where("id = ? AND revision = ?", ledger.ID, revision).
			Updates(map[string]any{"revision": ledger.Revision, "payload_json": string(raw)})
	}
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return budget.ErrLedgerConflict
	}
	return nil
}

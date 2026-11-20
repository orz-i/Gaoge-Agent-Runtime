package harnesspostgres

import (
	"errors"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"gorm.io/gorm"
)

// retryChildEffectFenceTx runs under the owning Turn row lock, shared by
// physical grant admission, cancellation and Accepted->Running Host claims.
// Do not read children before that lock: a completed Start grant is not an
// authoritative terminal receipt for its underlying Native/remote child.
func retryChildEffectFenceTx(tx *gorm.DB, turnID string, rotating harness.Invocation) error {
	var rows []invocationRecord
	if err := tx.Where("turn_id = ? AND id <> ? AND parent_item_id <> ?",
		turnID, rotating.ID, "").Find(&rows).Error; err != nil {
		return err
	}
	children := make([]harness.Invocation, 0, len(rows))
	for _, row := range rows {
		child, err := invocationFromRecord(row)
		if err != nil {
			return err
		}
		children = append(children, child)
	}
	// Use the SAME complete source as terminal CAS: original Started and
	// globally indexed terminal siblings, grants/returns, immutable Attempt
	// transitions and capacity Waiting observations. Dispatch-return alone
	// neither proves child completion nor permits another physical execution.
	items, err := terminalHandoffChildEvidenceTx(tx, turnID, children)
	if err != nil {
		return err
	}
	var ledger *budget.Ledger
	loaded, err := (&Store{db: tx}).LoadBudget(tx.Statement.Context, turnID)
	if err != nil && !errors.Is(err, budget.ErrLedgerNotFound) {
		return err
	}
	if err == nil {
		ledger = &loaded
	}
	return harness.RetryChildEffectFenceWithBudget(children, items, rotating, ledger)
}

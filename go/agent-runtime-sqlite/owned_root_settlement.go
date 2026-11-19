package sqlite

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var (
	_ kernel.PreparedRootSettlementStore = (*Store)(nil)
	_ kernel.OwnedRootSettlementStore    = (*Store)(nil)
)

func (s *Store) VerifyPreparedRootSettlement(ctx context.Context, expected kernel.Snapshot) error {
	return s.verifyRootSettlement(ctx, expected, true)
}

func (s *Store) VerifyOwnedRootSettlement(ctx context.Context, expected kernel.Snapshot) error {
	return s.verifyRootSettlement(ctx, expected, false)
}

// SQLite's immediate writer transaction is the same authority boundary used by
// physical admission, retirement, execution takeover and Cancel. TTL alone
// never supplies a callback return or upgrades historical Host custody.
func (s *Store) verifyRootSettlement(ctx context.Context, expected kernel.Snapshot, prepared bool) error {
	if s == nil || s.db == nil || ctx == nil || expected.Run.ID == "" {
		return kernel.ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = sqliteManagedGate(ctx, tx, expected.Run.ID); err != nil {
		return err
	}
	current, err := loadTx(ctx, tx, expected.Run.ID)
	if err != nil {
		return err
	}
	lease, quarantine, err := sqliteManagedLease(ctx, tx, expected.Run.ID)
	if err != nil {
		return err
	}
	if quarantine != 0 {
		return kernel.ErrExecutionConflict
	}
	epoch := lease.Epoch
	now, err := sqliteExecutionNow(ctx, tx)
	if err != nil {
		return err
	}
	if lease.ExpiresAt.UnixMilli() <= now {
		lease = kernel.ExecutionLease{}
	}
	var physicalEpoch uint64
	if err = tx.QueryRowContext(ctx, `SELECT execution_physical_epoch FROM runtime_runs WHERE run_id=?`, expected.Run.ID).Scan(&physicalEpoch); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT grant_id FROM runtime_owned_retirements WHERE run_id=?`, expected.Run.ID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	receipts := make([]kernel.OwnedRetirementReceipt, 0, len(ids))
	for _, id := range ids {
		r, err := sqliteLoadOwnedRetirement(ctx, tx, id)
		if err != nil {
			return err
		}
		receipts = append(receipts, r)
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM runtime_child_effect_grants WHERE parent_run_id=? OR child_run_id=?`, expected.Run.ID, expected.Run.ID).Scan(&active); err != nil {
		return err
	}
	if prepared {
		return kernel.VerifyPreparedRootSettlementFacts(expected, current, epoch, physicalEpoch, lease, active != 0, receipts)
	}
	return kernel.VerifyOwnedRootSettlementFacts(expected, current, epoch, physicalEpoch, lease, active != 0, receipts)
}

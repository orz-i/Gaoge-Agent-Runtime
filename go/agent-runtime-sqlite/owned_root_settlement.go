package sqlite

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var _ kernel.ExecutionCustodyStore = (*Store)(nil)

// The SQLite immediate transaction reads the same original authority as
// Memory and PostgreSQL; lease expiry never supplies a physical return.
func (s *Store) VerifyExecutionCustody(ctx context.Context, expected kernel.Snapshot, requirement kernel.ExecutionCustody) error {
	if s == nil || s.db == nil || ctx == nil || expected.Run.ID == "" || !requirement.Valid() {
		return kernel.ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = sqliteNativeEntryGate(ctx, tx, expected.Run.ID); err != nil {
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
	originalLease := lease
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
	if requirement == kernel.CustodyReturned {
		grant, _, err := loadPhysicalGrant(ctx, tx, expected.Run.ID)
		var completion kernel.PhysicalCallbackCompletion
		if err == nil {
			completion, err = loadPhysicalCallbackReturn(ctx, tx, grant)
			if err != nil && !errors.Is(err, kernel.ErrNotFound) {
				return err
			}
		} else if !errors.Is(err, kernel.ErrNotFound) {
			return err
		}
		return kernel.VerifyExecutionCustodyFacts(expected, current, epoch, physicalEpoch, originalLease, active != 0, receipts, grant, completion, requirement)
	}
	return kernel.VerifyExecutionCustodyFacts(expected, current, epoch, physicalEpoch, lease, active != 0, receipts, kernel.ChildExecutionGrant{}, kernel.PhysicalCallbackCompletion{}, requirement)
}

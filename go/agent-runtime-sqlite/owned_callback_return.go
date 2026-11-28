package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// loadPhysicalCallbackReturn is only used inside the same transaction that checks
// the immutable active grant. A callback return is not a reusable Run receipt.
func loadPhysicalCallbackReturn(ctx context.Context, q schemaQuery, g kernel.ChildExecutionGrant) (kernel.PhysicalCallbackCompletion, error) {
	var c kernel.PhysicalCallbackCompletion
	query := "SELECT DISTINCT callback_return_revision,callback_return_source_hash,callback_host_completed,callback_lease_owned,callback_control_custody,callback_tool_returns FROM runtime_child_effect_grants WHERE grant_id=? AND child_run_id=?"
	args := []any{g.ID, g.ChildRunID}
	if g.IsOwned() {
		query += " AND parent_run_id=?"
		args = append(args, g.ChildRunID)
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return c, conflictOrDatabaseError(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if err = rows.Scan(&c.ReturnRevision, &c.ReturnSourceHash, &c.HostCompleted, &c.LeaseOwned, &c.ControlCustody, &c.ToolReturns); err != nil {
			return c, conflictOrDatabaseError(err)
		}
	}
	if err = rows.Err(); err != nil {
		return c, conflictOrDatabaseError(err)
	}
	if count == 0 {
		return c, kernel.ErrNotFound
	}
	if count != 1 {
		return kernel.PhysicalCallbackCompletion{}, kernel.ErrExecutionConflict
	}
	if c != (kernel.PhysicalCallbackCompletion{}) {
		c.GrantID = g.ID
	}
	if err = kernel.ValidatePhysicalCompletion(g, c); err != nil {
		return kernel.PhysicalCallbackCompletion{}, err
	}
	return c, nil
}

// Owned native Grants are single self-scoped physical rows, unlike the
// replicated ancestor rows of a non-native child Grant.
func loadOwnedPhysicalGrant(ctx context.Context, q schemaQuery, g kernel.ChildExecutionGrant) (uint64, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+physicalGrantColumns+",parent_run_id FROM runtime_child_effect_grants WHERE grant_id=? OR child_run_id=? LIMIT 2", g.ID, g.ChildRunID)
	if err != nil {
		return 0, conflictOrDatabaseError(err)
	}
	defer func() { _ = rows.Close() }()
	var original kernel.ChildExecutionGrant
	var parent string
	var marked uint64
	count := 0
	for rows.Next() {
		count++
		if err = rows.Scan(&original.ID, &original.ChildRunID, &original.ExpectedRevision, &original.ExecutionOwnerID, &original.ExecutionLeaseID, &original.ExecutionEpoch, &original.AdmissionSourceHash, &original.AdmissionOperation, &marked, &parent); err != nil {
			return 0, err
		}
	}
	if err = rows.Err(); err != nil {
		return 0, conflictOrDatabaseError(err)
	}
	if count == 0 {
		return 0, kernel.ErrNotFound
	}
	if count != 1 || original != g || parent != g.ChildRunID {
		return 0, kernel.ErrExecutionConflict
	}
	return marked, nil
}

func (s *Store) CheckOwnedPhysicalEntry(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, expected kernel.Snapshot) (kernel.OwnedPhysicalEntryWindow, error) {
	if s == nil || s.db == nil || ctx == nil || kernel.ValidatePhysicalAdmissionRequest(g, l) != nil {
		return kernel.OwnedPhysicalEntryWindow{}, kernel.ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return kernel.OwnedPhysicalEntryWindow{}, conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := loadTx(ctx, tx, g.ChildRunID)
	if err != nil {
		return kernel.OwnedPhysicalEntryWindow{}, err
	}
	if _, err = sqliteValidateLiveRunOwners(ctx, tx, current.Run); err != nil {
		return kernel.OwnedPhysicalEntryWindow{}, err
	}
	marked, err := loadOwnedPhysicalGrant(ctx, tx, g)
	if err != nil {
		return kernel.OwnedPhysicalEntryWindow{}, err
	}
	var active int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM runtime_child_effect_grants WHERE parent_run_id=?", g.ChildRunID).Scan(&active); err != nil {
		return kernel.OwnedPhysicalEntryWindow{}, conflictOrDatabaseError(err)
	}
	if active != 1 {
		return kernel.OwnedPhysicalEntryWindow{}, kernel.ErrExecutionConflict
	}
	completion, err := loadPhysicalCallbackReturn(ctx, tx, g)
	if err != nil && !errors.Is(err, kernel.ErrNotFound) {
		return kernel.OwnedPhysicalEntryWindow{}, err
	}
	native, quarantine, err := sqliteManagedLease(ctx, tx, g.ChildRunID)
	if err != nil {
		return kernel.OwnedPhysicalEntryWindow{}, err
	}
	var physicalEpoch uint64
	if err = tx.QueryRowContext(ctx, "SELECT execution_physical_epoch FROM runtime_runs WHERE run_id=?", g.ChildRunID).Scan(&physicalEpoch); err != nil {
		return kernel.OwnedPhysicalEntryWindow{}, conflictOrDatabaseError(err)
	}
	var transfer kernel.ManagedTransfer
	if g.AdmissionOperation == "managed_resume" {
		transfer, err = sqliteLoadManaged(ctx, tx, g.ChildRunID)
		if err != nil {
			return kernel.OwnedPhysicalEntryWindow{}, err
		}
	}
	now, err := sqliteExecutionNow(ctx, tx)
	if err != nil {
		return kernel.OwnedPhysicalEntryWindow{}, err
	}
	window, err := kernel.ValidateOwnedPhysicalEntry(g, expected, current, l, native, time.UnixMilli(now), quarantine != 0, physicalEpoch, marked, completion, transfer)
	if err != nil {
		return kernel.OwnedPhysicalEntryWindow{}, err
	}
	if g.AdmissionOperation == "managed_resume" {
		a, e := sqliteManagedAuthority(ctx, tx)
		if e != nil {
			return kernel.OwnedPhysicalEntryWindow{}, e
		}
		if transfer.SafePoint.Host.KernelAuthority != a {
			return kernel.OwnedPhysicalEntryWindow{}, kernel.ErrExecutionConflict
		}
	}
	if err = advanceOwnedEntryEpoch(ctx, tx, g); err != nil {
		return kernel.OwnedPhysicalEntryWindow{}, err
	}
	if err = tx.Commit(); err != nil {
		return kernel.OwnedPhysicalEntryWindow{}, conflictOrDatabaseError(err)
	}
	return window, nil
}

// A successful entry and a sealed original no-entry return consume the
// same epoch watermark, in their respective original transactions.
func advanceOwnedEntryEpoch(ctx context.Context, tx *sql.Tx, g kernel.ChildExecutionGrant) error {
	result, e := tx.ExecContext(ctx, "UPDATE runtime_runs SET execution_physical_epoch=? WHERE run_id=? AND execution_epoch=? AND execution_physical_epoch<?", g.ExecutionEpoch, g.ChildRunID, g.ExecutionEpoch, g.ExecutionEpoch)
	if e != nil {
		return conflictOrDatabaseError(e)
	}
	n, e := result.RowsAffected()
	if e != nil || n != 1 {
		return errors.Join(kernel.ErrExecutionConflict, e)
	}
	return nil
}

func (s *Store) RecordPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, revision uint64, outcome kernel.PhysicalCallbackOutcome, permit kernel.PhysicalCallbackReturn) error {
	if s == nil || s.db == nil || ctx == nil || !g.Identity().Valid() || g.ExpectedRevision == 0 || revision == 0 {
		return kernel.ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var marked uint64
	count := 1
	if g.IsOwned() {
		marked, err = loadOwnedPhysicalGrant(ctx, tx, g)
	} else {
		if err = sqliteNativeEntryGate(ctx, tx, g.ChildRunID); err != nil {
			return err
		}
		var original kernel.ChildExecutionGrant
		original, marked, err = loadPhysicalGrant(ctx, tx, g.ChildRunID)
		if err == nil && original != g {
			return kernel.ErrExecutionConflict
		}
		if err == nil {
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM runtime_child_effect_grants WHERE grant_id=?", g.ID).Scan(&count); err != nil {
				return err
			}
		}
	}
	if errors.Is(err, kernel.ErrNotFound) {
		return kernel.ErrExecutionConflict
	}
	if err != nil {
		return err
	}
	current, err := loadTx(ctx, tx, g.ChildRunID)
	if err != nil {
		return err
	}
	native, _, err := sqliteManagedLease(ctx, tx, g.ChildRunID)
	if err != nil {
		return err
	}
	existing, err := loadPhysicalCallbackReturn(ctx, tx, g)
	if err != nil && !errors.Is(err, kernel.ErrNotFound) {
		return err
	}
	var physicalEpoch uint64
	if err = tx.QueryRowContext(ctx, "SELECT execution_physical_epoch FROM runtime_runs WHERE run_id=?", g.ChildRunID).Scan(&physicalEpoch); err != nil {
		return conflictOrDatabaseError(err)
	}
	completion, write, err := kernel.ReducePhysicalCallbackReturn(g, current, native, revision, outcome, permit, existing, marked, physicalEpoch)
	if err != nil || !write {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE runtime_child_effect_grants SET callback_return_revision=?,callback_return_source_hash=?,callback_host_completed=?,callback_lease_owned=?,callback_control_custody=?,callback_tool_returns=? WHERE grant_id=? AND child_run_id=? AND start_revision=? AND settled_revision=0 AND callback_return_revision=0`, completion.ReturnRevision, completion.ReturnSourceHash, completion.HostCompleted, completion.LeaseOwned, string(completion.ControlCustody), string(completion.ToolReturns), g.ID, g.ChildRunID, g.ExpectedRevision)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != int64(count) {
		return kernel.ErrExecutionConflict
	}
	if g.IsOwned() && physicalEpoch < g.ExecutionEpoch {
		if err = advanceOwnedEntryEpoch(ctx, tx, g); err != nil {
			return err
		}
	}
	return conflictOrDatabaseError(tx.Commit())
}

func (s *Store) LookupPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant) (kernel.PhysicalCallbackCompletion, error) {
	if s == nil || s.db == nil || ctx == nil || !g.Identity().Valid() || g.ExpectedRevision == 0 {
		return kernel.PhysicalCallbackCompletion{}, kernel.ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return kernel.PhysicalCallbackCompletion{}, conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if g.IsOwned() {
		if _, err = loadOwnedPhysicalGrant(ctx, tx, g); err != nil {
			return kernel.PhysicalCallbackCompletion{}, err
		}
	} else {
		original, marked, e := loadPhysicalGrant(ctx, tx, g.ChildRunID)
		if e != nil {
			return kernel.PhysicalCallbackCompletion{}, e
		}
		if original != g || marked != 0 {
			return kernel.PhysicalCallbackCompletion{}, kernel.ErrExecutionConflict
		}
	}
	// A matching orphan is an integrity conflict, not an original return.
	var sourceID string
	if err = tx.QueryRowContext(ctx, "SELECT run_id FROM runtime_runs WHERE run_id=?", g.ChildRunID).Scan(&sourceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return kernel.PhysicalCallbackCompletion{}, kernel.ErrExecutionConflict
		}
		return kernel.PhysicalCallbackCompletion{}, conflictOrDatabaseError(err)
	}
	return loadPhysicalCallbackReturn(ctx, tx, g)
}

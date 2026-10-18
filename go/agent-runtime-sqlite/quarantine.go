package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// QuarantinedTransition is an intact, non-ACKed projection with a stable
// diagnosis. This is a **transport-only** Stage 1 isolation mechanism;
// it does not authorize direct Agent.Resume or implement the Stage 2
// authoritative Run Store quarantine fence.
type QuarantinedTransition struct {
	WorkID        string
	RunID         string
	FailureCode   string
	QuarantinedAt time.Time
}

func quarantineReady(ctx context.Context, tx *sql.Tx, workID, code string, atNS int64) error {
	result, err := tx.ExecContext(ctx,
		"UPDATE runtime_ready_work SET quarantine_code=?,quarantined_ns=?,worker_id='',lease_id='',lease_until_ns=0 WHERE work_id=? AND quarantine_code=''",
		code, atNS, workID)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return kernel.ErrConflict
	}
	return nil
}

// ListQuarantinedTransitions exposes a bounded diagnostic view without
// deleting or replaying any underlying ready-work. Repair requires a later
// verified Feature/Run state protocol, not blind SQL replay.
func (s *Store) ListQuarantinedTransitions(ctx context.Context, limit int) ([]QuarantinedTransition, error) {
	if s == nil || s.db == nil || ctx == nil || limit < 1 || limit > 1000 {
		return nil, kernel.ErrInvalidInput
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT work_id,run_id,quarantine_code,quarantined_ns FROM runtime_ready_work WHERE quarantine_code<>'' ORDER BY quarantined_ns,work_id LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	result := make([]QuarantinedTransition, 0)
	for rows.Next() {
		var item QuarantinedTransition
		var ns int64
		if err = rows.Scan(&item.WorkID, &item.RunID, &item.FailureCode, &ns); err != nil {
			break
		}
		item.QuarantinedAt = time.Unix(0, ns).UTC()
		result = append(result, item)
	}
	if scanErr := rows.Err(); err == nil {
		err = scanErr
	}
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	return result, nil
}

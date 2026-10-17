package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// ClaimTransitions exposes the durable ready_work ledger through the current
// Kernel TransitionOutbox port. In Stage 1 this is a single-executor recovery
// aid, not a distributed lease/Run execution-authority guarantee.
func (s *Store) ClaimTransitions(ctx context.Context, request kernel.TransitionClaimRequest) ([]kernel.TransitionClaim, error) {
	if s == nil || s.db == nil || ctx == nil || strings.TrimSpace(request.WorkerID) == "" ||
		request.Limit <= 0 || request.LeaseDuration <= 0 || request.Now.IsZero() {
		return nil, kernel.ErrInvalidInput
	}
	now, err := storedNanoseconds(request.Now)
	if err != nil {
		return nil, err
	}
	leaseUntil := request.Now.UTC().Add(request.LeaseDuration)
	leaseNS, err := storedNanoseconds(leaseUntil)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx,
		`SELECT work_id,run_id,kind,status,revision,events_json,payload_hash,committed_ns,attempts
		  FROM runtime_ready_work WHERE quarantine_code='' AND available_ns<=? AND
		  (lease_until_ns=0 OR lease_until_ns<=?)
		  ORDER BY available_ns,committed_ns,work_id LIMIT ?`, now, now, request.Limit)
	if err != nil {
		return nil, conflictOrDatabaseError(err)
	}
	type candidate struct {
		id, runID, kind, status, fingerprint string
		revision, committedNS, attempts      int64
		events                               []byte
	}
	var candidates []candidate
	for rows.Next() {
		var entry candidate
		if err = rows.Scan(&entry.id, &entry.runID, &entry.kind, &entry.status, &entry.revision,
			&entry.events, &entry.fingerprint, &entry.committedNS, &entry.attempts); err != nil {
			break
		}
		candidates = append(candidates, entry)
	}
	if rowsErr := rows.Err(); err == nil {
		err = rowsErr
	}
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	claims := make([]kernel.TransitionClaim, 0, len(candidates))
	for _, entry := range candidates {
		if entry.attempts < 0 || entry.attempts >= int64(^uint32(0)) || entry.revision < 1 {
			if err := quarantineReady(ctx, tx, entry.id, "invalid_metadata", now); err != nil {
				return nil, err
			}
			continue
		}
		events, err := decodeEvents(entry.events)
		if err != nil {
			// Invalid JSON remains diagnosable without ACK/delete.
			if err = quarantineReady(ctx, tx, entry.id, "invalid_events", now); err != nil {
				return nil, err
			}
			continue
		}
		if entry.fingerprint != readyPayloadFingerprint(entry.id, entry.runID, entry.kind, entry.status,
			uint64(entry.revision), entry.committedNS, entry.events) {
			if err = quarantineReady(ctx, tx, entry.id, "invalid_payload_integrity", now); err != nil {
				return nil, err
			}
			continue
		}
		if !validProjectedTransition(entry.id, entry.runID, uint64(entry.revision),
			kernel.RunStatus(entry.status), events) {
			if err = quarantineReady(ctx, tx, entry.id, "invalid_projection", now); err != nil {
				return nil, err
			}
			continue
		}
		attempts := entry.attempts + 1
		leaseID := fmt.Sprintf("%s:%s:%d", strings.TrimSpace(request.WorkerID), entry.id, attempts)
		result, err := tx.ExecContext(ctx,
			`UPDATE runtime_ready_work SET attempts=?, worker_id=?,lease_id=?,lease_until_ns=?
			  WHERE work_id=? AND attempts=? AND available_ns<=?
			  AND quarantine_code='' AND (lease_until_ns=0 OR lease_until_ns<=?)`,
			attempts, strings.TrimSpace(request.WorkerID), leaseID, leaseNS,
			entry.id, entry.attempts, now, now)
		if err != nil {
			return nil, conflictOrDatabaseError(err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if affected != 1 {
			return nil, kernel.ErrConflict
		}
		claims = append(claims, kernel.TransitionClaim{
			Transition: kernel.CommittedTransition{
				ID: entry.id, RunID: entry.runID, Kind: kernel.RunKind(entry.kind),
				Status: kernel.RunStatus(entry.status), Revision: uint64(entry.revision),
				Events: events, CommittedAt: time.Unix(0, entry.committedNS).UTC(),
				Attempts: uint32(attempts),
			},
			LeaseID: leaseID, WorkerID: strings.TrimSpace(request.WorkerID), LeaseUntil: leaseUntil,
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claims, nil
}

func validTransitionLease(request kernel.TransitionLeaseRequest) bool {
	return strings.TrimSpace(request.TransitionID) != "" &&
		strings.TrimSpace(request.LeaseID) != "" && strings.TrimSpace(request.WorkerID) != ""
}

func (s *Store) transitionMissingOrConflict(ctx context.Context, id string) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM runtime_ready_work WHERE work_id=?`, id).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return kernel.ErrNotFound
	}
	return kernel.ErrConflict
}

// AckTransition deletes only a successfully projected, exactly leased
// committed transition. A stale lease cannot consume another worker's claim.
func (s *Store) AckTransition(ctx context.Context, request kernel.TransitionLeaseRequest) error {
	if s == nil || s.db == nil || ctx == nil || !validTransitionLease(request) {
		return kernel.ErrInvalidInput
	}
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM runtime_ready_work WHERE work_id=? AND lease_id=? AND worker_id=? AND quarantine_code=''`,
		request.TransitionID, request.LeaseID, request.WorkerID)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	return s.transitionMissingOrConflict(ctx, request.TransitionID)
}

// RetryTransition releases a failed projection with its next availability.
// Poison handling and authoritative quarantine belong to Stage 2.
func (s *Store) RetryTransition(ctx context.Context, request kernel.TransitionRetryRequest) error {
	if s == nil || s.db == nil || ctx == nil || !validTransitionLease(request.TransitionLeaseRequest) ||
		request.AvailableAt.IsZero() {
		return kernel.ErrInvalidInput
	}
	atNS, err := storedNanoseconds(request.AvailableAt)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE runtime_ready_work SET available_ns=?,worker_id='',lease_id='',lease_until_ns=0
		  WHERE work_id=? AND lease_id=? AND worker_id=? AND quarantine_code=''`,
		atNS, request.TransitionID, request.LeaseID, request.WorkerID)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	return s.transitionMissingOrConflict(ctx, request.TransitionID)
}

package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type encodedRecord struct {
	run        []byte
	state      []byte
	checkpoint []byte
	result     []byte
}

func encodeRecord(record kernel.Record) (encodedRecord, error) {
	if record.Run.ID == "" || record.Run.Revision == 0 || !json.Valid(record.State) {
		return encodedRecord{}, ErrInvalidRecord
	}
	for _, value := range []time.Time{record.Run.CreatedAt, record.Run.UpdatedAt} {
		if _, err := storedNanoseconds(value); err != nil {
			return encodedRecord{}, err
		}
	}
	for _, optional := range []*time.Time{record.Run.DeadlineAt, record.Run.EndedAt} {
		if optional != nil {
			if _, err := storedNanoseconds(*optional); err != nil {
				return encodedRecord{}, err
			}
		}
	}
	run, err := json.Marshal(record.Run)
	if err != nil {
		return encodedRecord{}, err
	}
	value := encodedRecord{run: run, state: append([]byte(nil), record.State...)}
	if record.Checkpoint != nil {
		value.checkpoint, err = json.Marshal(record.Checkpoint)
		if err != nil {
			return encodedRecord{}, err
		}
	}
	if record.Result != nil {
		value.result, err = json.Marshal(record.Result)
		if err != nil {
			return encodedRecord{}, err
		}
	}
	return value, nil
}

type persistedEvent struct {
	Type             string          `json:"type"`
	Message          string          `json:"message,omitempty"`
	Data             json.RawMessage `json:"data,omitempty"`
	Wakeup           bool            `json:"wakeup,omitempty"`
	WakeupAt         *time.Time      `json:"wakeupAt,omitempty"`
	WakeupGeneration uint64          `json:"wakeupGeneration,omitempty"`
}

func encodeEvents(drafts []kernel.EventDraft) ([]byte, error) {
	values := make([]persistedEvent, len(drafts))
	for i, draft := range drafts {
		if len(draft.Data) != 0 && !json.Valid(draft.Data) {
			return nil, ErrInvalidRecord
		}
		if draft.WakeupAt != nil {
			if _, err := storedNanoseconds(*draft.WakeupAt); err != nil {
				return nil, err
			}
		}
		values[i] = persistedEvent{
			Type: draft.Type, Message: draft.Message,
			Data:   append(json.RawMessage(nil), draft.Data...),
			Wakeup: draft.Wakeup, WakeupAt: draft.WakeupAt,
			WakeupGeneration: draft.WakeupGeneration,
		}
	}
	return json.Marshal(values)
}

func decodeEvents(raw []byte) ([]kernel.EventDraft, error) {
	var events []persistedEvent
	if err := json.Unmarshal(raw, &events); err != nil {
		return nil, fmt.Errorf("%w: persisted transition events: %w", ErrInvalidRecord, err)
	}
	result := make([]kernel.EventDraft, 0, len(events))
	for _, event := range events {
		if len(event.Data) != 0 && !json.Valid(event.Data) {
			return nil, ErrInvalidRecord
		}
		result = append(result, kernel.EventDraft{
			Type: event.Type, Message: event.Message,
			Data:   append(json.RawMessage(nil), event.Data...),
			Wakeup: event.Wakeup, WakeupAt: event.WakeupAt,
			WakeupGeneration: event.WakeupGeneration,
		})
	}
	return result, nil
}

func commitFingerprint(encoded encodedRecord, transitionEvents []byte) string {
	digest := sha256.New()
	// Length-prefixed parts exclude concatenation ambiguity.
	for _, part := range [][]byte{encoded.run, encoded.state, encoded.checkpoint, encoded.result, transitionEvents} {
		_, _ = fmt.Fprintf(digest, "%d:", len(part))
		_, _ = digest.Write(part)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func writeEvents(ctx context.Context, tx *sql.Tx, runID string, firstSeq int64, at time.Time, events []kernel.EventDraft) error {
	timestamp, err := storedNanoseconds(at)
	if err != nil {
		return err
	}
	for i, e := range events {
		var data []byte
		if len(e.Data) > 0 {
			data = e.Data
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO runtime_events(run_id,seq,event_type,message,data_json,created_ns)
			  VALUES(?,?,?,?,?,?)`, runID, firstSeq+int64(i)+1, e.Type, e.Message, data, timestamp)
		if err != nil {
			return err
		}
	}
	return nil
}

func writeReady(ctx context.Context, tx *sql.Tx, run kernel.Run, events []kernel.EventDraft, encodedEvents []byte) error {
	timestamp, err := storedNanoseconds(run.UpdatedAt)
	if err != nil {
		return err
	}
	// Every committed wakeup and terminal transition remains an unconsumed
	// fact until its own projector has ACKed it. A later Run revision is NOT
	// proof that a previous projection was delivered to a parent/host.
	if !kernel.NeedsTransitionProjection(run.Status, events) {
		return nil
	}
	workID := transitionID(run.ID, run.Revision)
	fingerprint := readyPayloadFingerprint(workID, run.ID, string(run.Kind), string(run.Status),
		run.Revision, timestamp, encodedEvents)
	_, err = tx.ExecContext(ctx,
		`INSERT INTO runtime_ready_work(work_id,run_id,kind,status,revision,events_json,payload_hash,committed_ns,available_ns)
          VALUES(?,?,?,?,?,?,?,?,?)`,
		workID, run.ID, string(run.Kind), string(run.Status), run.Revision, encodedEvents, fingerprint, timestamp, timestamp)
	return err
}

func writeCommitProof(ctx context.Context, tx *sql.Tx, run kernel.Run, fingerprint string) error {
	timestamp, err := storedNanoseconds(run.UpdatedAt)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO runtime_commit_proofs(run_id,revision,fingerprint,committed_ns)
		  VALUES(?,?,?,?)`, run.ID, run.Revision, fingerprint, timestamp)
	return err
}

func transitionID(runID string, revision uint64) string {
	return runID + ":" + strconv.FormatUint(revision, 10)
}

type queryRow interface{ Scan(...any) error }

func decodeSnapshot(row queryRow) (kernel.Snapshot, error) {
	var rawRun, state, checkpoint, result []byte
	var revision int64
	var head int64
	if err := row.Scan(&rawRun, &state, &checkpoint, &result, &revision, &head); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return kernel.Snapshot{}, kernel.ErrNotFound
		}
		return kernel.Snapshot{}, err
	}
	var run kernel.Run
	if err := json.Unmarshal(rawRun, &run); err != nil {
		return kernel.Snapshot{}, fmt.Errorf("%w: run JSON: %w", ErrInvalidRecord, err)
	}
	if !json.Valid(state) || run.Revision != uint64(revision) || run.ID == "" {
		return kernel.Snapshot{}, ErrInvalidRecord
	}
	snapshot := kernel.Snapshot{Run: run, State: append(json.RawMessage(nil), state...), EventHead: head}
	if len(checkpoint) > 0 {
		var value kernel.Checkpoint
		if err := json.Unmarshal(checkpoint, &value); err != nil {
			return kernel.Snapshot{}, fmt.Errorf("%w: checkpoint: %w", ErrInvalidRecord, err)
		}
		snapshot.Checkpoint = &value
	}
	if len(result) > 0 {
		var value kernel.Result
		if err := json.Unmarshal(result, &value); err != nil {
			return kernel.Snapshot{}, fmt.Errorf("%w: result: %w", ErrInvalidRecord, err)
		}
		snapshot.Result = &value
	}
	return snapshot, nil
}

const readSnapshotSQL = `SELECT run_json,state_json,checkpoint_json,result_json,revision,event_head
	FROM runtime_runs WHERE run_id=?`

func loadTx(ctx context.Context, tx *sql.Tx, runID string) (kernel.Snapshot, error) {
	return decodeSnapshot(tx.QueryRowContext(ctx, readSnapshotSQL, runID))
}

// Create commits the Run, event journal, transition-based local ready-work
// projection and one revision-keyed commit proof in a single SQLite transaction.
func (s *Store) Create(ctx context.Context, record kernel.Record, drafts []kernel.EventDraft) (kernel.Snapshot, error) {
	if s == nil || s.db == nil || ctx == nil || strings.TrimSpace(record.Run.ID) == "" {
		return kernel.Snapshot{}, kernel.ErrInvalidInput
	}
	encoded, err := encodeRecord(record)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	events, err := encodeEvents(drafts)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return kernel.Snapshot{}, conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx,
		`INSERT INTO runtime_runs(run_id,scope_id,kind,status,revision,run_json,state_json,checkpoint_json,result_json,event_head)
		  VALUES(?,?,?,?,?,?,?,?,?,?)`,
		record.Run.ID, record.Run.Actor.TenantID, string(record.Run.Kind), string(record.Run.Status), record.Run.Revision,
		encoded.run, encoded.state, encoded.checkpoint, encoded.result, len(drafts))
	if err != nil {
		if constraintError(err) {
			return kernel.Snapshot{}, fmt.Errorf("%w: %w", kernel.ErrAlreadyExists, err)
		}
		return kernel.Snapshot{}, conflictOrDatabaseError(err)
	}
	if err = writeEvents(ctx, tx, record.Run.ID, 0, record.Run.UpdatedAt, drafts); err != nil {
		return kernel.Snapshot{}, err
	}
	if err = writeReady(ctx, tx, record.Run, drafts, events); err != nil {
		return kernel.Snapshot{}, err
	}
	if err = writeCommitProof(ctx, tx, record.Run, commitFingerprint(encoded, events)); err != nil {
		return kernel.Snapshot{}, err
	}
	snapshot, err := loadTx(ctx, tx, record.Run.ID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if err = tx.Commit(); err != nil {
		return kernel.Snapshot{}, err
	}
	return snapshot, nil
}

// Load reads the authoritative aggregate without paging the event journal.
func (s *Store) Load(ctx context.Context, runID string) (kernel.Snapshot, error) {
	if s == nil || s.db == nil || ctx == nil || strings.TrimSpace(runID) == "" {
		return kernel.Snapshot{}, kernel.ErrInvalidInput
	}
	return decodeSnapshot(s.db.QueryRowContext(ctx, readSnapshotSQL, strings.TrimSpace(runID)))
}

// Apply performs one single-Run CAS; it never does a post-commit Load which
// might instead observe a different worker's next revision.
func (s *Store) Apply(ctx context.Context, runID string, expectedRevision uint64, mutation kernel.StoreMutation) (kernel.Snapshot, error) {
	return s.applyWithExecution(ctx, nil, runID, expectedRevision, mutation)
}

// applyWithExecution atomically combines Run CAS and Store-owned execution
// authority with the same short transaction as journal/ReadyWork/proof.
func (s *Store) applyWithExecution(ctx context.Context, lease *kernel.ExecutionLease, runID string, expectedRevision uint64, mutation kernel.StoreMutation) (kernel.Snapshot, error) {
	if s == nil || s.db == nil || ctx == nil || strings.TrimSpace(runID) == "" || expectedRevision == 0 {
		return kernel.Snapshot{}, kernel.ErrInvalidInput
	}
	if mutation.Record.Run.ID != runID || mutation.Record.Run.Revision != expectedRevision+1 {
		return kernel.Snapshot{}, kernel.ErrConflict
	}
	encoded, err := encodeRecord(mutation.Record)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	events, err := encodeEvents(mutation.Events)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return kernel.Snapshot{}, conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	// The entire predicate is evaluated by SQLite while holding the same
	// writer transaction, so a takeover cannot race between lease verification
	// and the revision update. Claims/renewals also hold this DB writer lock.
	updateSQL := `UPDATE runtime_runs SET scope_id=?,kind=?,status=?,revision=?,run_json=?,state_json=?,
      checkpoint_json=?,result_json=?,event_head=event_head+?
      WHERE run_id=? AND revision=?`
	args := []any{mutation.Record.Run.Actor.TenantID, string(mutation.Record.Run.Kind),
		string(mutation.Record.Run.Status), mutation.Record.Run.Revision,
		encoded.run, encoded.state, encoded.checkpoint, encoded.result, len(mutation.Events),
		runID, expectedRevision}
	if lease == nil {
		updateSQL += ` AND execution_epoch=0`
	} else {
		nowMS, clockErr := sqliteExecutionNow(ctx, tx)
		if clockErr != nil {
			return kernel.Snapshot{}, clockErr
		}
		updateSQL += ` AND execution_epoch=? AND execution_owner=?
          AND execution_lease_id=? AND execution_expiry_ms>?`
		args = append(args, lease.Epoch, lease.OwnerID, lease.LeaseID, nowMS)
	}
	updated, err := tx.ExecContext(ctx, updateSQL, args...)
	if err != nil {
		return kernel.Snapshot{}, conflictOrDatabaseError(err)
	}
	affected, err := updated.RowsAffected()
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if affected != 1 {
		var actualRevision int64
		var authorityEpoch int64
		err = tx.QueryRowContext(ctx,
			`SELECT revision,execution_epoch FROM runtime_runs WHERE run_id=?`, runID).Scan(&actualRevision, &authorityEpoch)
		if errors.Is(err, sql.ErrNoRows) {
			return kernel.Snapshot{}, kernel.ErrNotFound
		}
		if err != nil {
			return kernel.Snapshot{}, err
		}
		// Never turn a stale execution token into a plain revision conflict.
		// This matters when a replacement owner has not changed revision.
		if lease == nil && authorityEpoch != 0 {
			return kernel.Snapshot{}, kernel.ErrExecutionRequired
		}
		if lease != nil {
			// A different revision can be a legitimate same-owner CAS race,
			// but checking the token first preserves lease-fencing semantics.
			var epoch int64
			var owner, id string
			var expiry int64
			err = tx.QueryRowContext(ctx, `SELECT execution_epoch,execution_owner,execution_lease_id,execution_expiry_ms FROM runtime_runs WHERE run_id=?`, runID).Scan(&epoch, &owner, &id, &expiry)
			if err != nil {
				return kernel.Snapshot{}, err
			}
			nowMS, clockErr := sqliteExecutionNow(ctx, tx)
			if clockErr != nil {
				return kernel.Snapshot{}, clockErr
			}
			if epoch != int64(lease.Epoch) || owner != lease.OwnerID || id != lease.LeaseID || expiry <= nowMS {
				return kernel.Snapshot{}, kernel.ErrExecutionConflict
			}
		}
		return kernel.Snapshot{}, kernel.ErrConflict
	}
	snapshot, err := loadTx(ctx, tx, runID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if err = writeEvents(ctx, tx, runID, snapshot.EventHead-int64(len(mutation.Events)), mutation.Record.Run.UpdatedAt, mutation.Events); err != nil {
		return kernel.Snapshot{}, err
	}
	if err = writeReady(ctx, tx, mutation.Record.Run, mutation.Events, events); err != nil {
		return kernel.Snapshot{}, err
	}
	if err = writeCommitProof(ctx, tx, mutation.Record.Run, commitFingerprint(encoded, events)); err != nil {
		return kernel.Snapshot{}, err
	}
	if err = tx.Commit(); err != nil {
		return kernel.Snapshot{}, err
	}
	return snapshot, nil
}

func (s *Store) ListEvents(ctx context.Context, runID string, afterSeq int64, limit int) ([]kernel.Event, error) {
	if s == nil || s.db == nil || ctx == nil || strings.TrimSpace(runID) == "" || afterSeq < 0 || limit < 1 || limit > 10_000 {
		return nil, kernel.ErrInvalidInput
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq,event_type,message,data_json,created_ns FROM runtime_events
		  WHERE run_id=? AND seq>? ORDER BY seq LIMIT ?`, strings.TrimSpace(runID), afterSeq, limit)
	if err != nil {
		return nil, err
	}
	result := make([]kernel.Event, 0)
	for rows.Next() {
		var event kernel.Event
		var data []byte
		var timestamp int64
		if err = rows.Scan(&event.Seq, &event.Type, &event.Message, &data, &timestamp); err != nil {
			break
		}
		if len(data) > 0 && !json.Valid(data) {
			err = ErrInvalidRecord
			break
		}
		event.Data = append(json.RawMessage(nil), data...)
		event.CreatedAt = time.Unix(0, timestamp).UTC()
		result = append(result, event)
	}
	if scanErr := rows.Err(); err == nil {
		err = scanErr
	}
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM runtime_runs WHERE run_id=?`, runID).Scan(&count); err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, kernel.ErrNotFound
		}
	}
	return result, nil
}

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// KernelStore persists only the feature-neutral Kernel aggregate.
type KernelStore struct {
	db *gorm.DB
}

func (store *KernelStore) kernelTransitionMissingOrConflict(db *gorm.DB, transitionID string) error {
	var count int64
	if err := db.Model(&models.KernelTransitionOutboxRecord{}).Where("id = ?", transitionID).Count(&count).Error; err != nil {
		return translateKernelError(err)
	}
	if count == 0 {
		return kernel.ErrNotFound
	}
	return kernel.ErrConflict
}

func isKernelUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") ||
		strings.Contains(message, "duplicate key") ||
		strings.Contains(message, "unique failed")
}

func (store *KernelStore) applyKernelMutation(
	db *gorm.DB,
	runID string,
	expectedRevision uint64,
	updatedModel models.KernelRunRecord,
	mutation kernel.StoreMutation,
) error {
	result := db.Model(&models.KernelRunRecord{}).
		Where("run_id = ? AND revision = ?", runID, expectedRevision).
		Updates(kernelRunUpdates(updatedModel, len(mutation.Events)))
	if result.Error != nil {
		return translateKernelApplyError(result.Error)
	}
	if result.RowsAffected != 1 {
		return store.kernelMissingOrConflict(db, runID)
	}
	var persisted models.KernelRunRecord
	if err := db.Where("run_id = ?", runID).First(&persisted).Error; err != nil {
		return translateKernelError(err)
	}
	firstSeq := persisted.LastEventSeq - int64(len(mutation.Events))
	if err := createKernelEvents(db, runID, firstSeq, mutation.Record.Run.UpdatedAt, mutation.Events); err != nil {
		return err
	}
	return createKernelTransition(db, mutation.Record.Run, mutation.Events)
}

const (
	kernelColumnTenantID     = "tenant_id"
	kernelColumnErrorDetail  = "error_detail"
	kernelColumnLastEventSeq = "last_event_seq"
	kernelColumnStatus       = "status"
	kernelColumnRevision     = "revision"
	kernelColumnErrorCode    = "error_code"
	kernelColumnEndedAt      = "ended_at"
	kernelColumnUpdatedAt    = "updated_at"
)

var (
	errKernelStateJSON          = errors.New("kernel state is invalid JSON")
	errKernelEventJSON          = errors.New("kernel event data is invalid JSON")
	errPersistedKernelStateJSON = errors.New("persisted kernel state is invalid JSON")
	errPersistedKernelEventJSON = errors.New("persisted kernel event data is invalid JSON")
)

var _ kernel.Store = (*KernelStore)(nil)

// NewKernelStore creates the PostgreSQL Kernel adapter without exposing the legacy Store.
func NewKernelStore(db *gorm.DB) *KernelStore {
	return &KernelStore{db: db}
}

// Create atomically persists the initial Run and its Event stream.
func (store *KernelStore) Create(
	ctx context.Context,
	record kernel.Record,
	events []kernel.EventDraft,
) (kernel.Snapshot, error) {
	if store == nil || store.db == nil || record.Run.ID == "" {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	model, err := kernelRunRecordFrom(record, int64(len(events)))
	if err != nil {
		return kernel.Snapshot{}, err
	}
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if createErr := tx.Create(&model).Error; createErr != nil {
			return translateKernelError(createErr)
		}
		if createErr := createKernelEvents(tx, record.Run.ID, 0, record.Run.UpdatedAt, events); createErr != nil {
			return createErr
		}
		return createKernelTransition(tx, record.Run, events)
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return store.Load(ctx, record.Run.ID)
}

// Load returns one isolated Kernel Snapshot with monotonic Events.
func (store *KernelStore) Load(ctx context.Context, runID string) (kernel.Snapshot, error) {
	if store == nil || store.db == nil {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	db := store.db.WithContext(ctx)
	var runRecord models.KernelRunRecord
	if err := db.Where("run_id = ?", runID).First(&runRecord).Error; err != nil {
		return kernel.Snapshot{}, translateKernelError(err)
	}
	var eventRecords []models.KernelEventRecord
	if err := db.Where("run_id = ?", runID).Order("seq ASC").Find(&eventRecords).Error; err != nil {
		return kernel.Snapshot{}, translateKernelError(err)
	}
	return kernelSnapshotFrom(runRecord, eventRecords)
}

// Apply atomically commits one revision-CAS transition and its Event append.
func (store *KernelStore) Apply(
	ctx context.Context,
	runID string,
	expectedRevision uint64,
	mutation kernel.StoreMutation,
) (kernel.Snapshot, error) {
	if store == nil || store.db == nil || strings.TrimSpace(runID) == "" {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	if mutation.Record.Run.ID != runID || mutation.Record.Run.Revision != expectedRevision+1 {
		return kernel.Snapshot{}, kernel.ErrConflict
	}
	updatedModel, err := kernelRunRecordFrom(mutation.Record, 0)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return store.applyKernelMutation(tx, runID, expectedRevision, updatedModel, mutation)
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return store.Load(ctx, runID)
}

// ClaimTransitions leases committed transitions to one projector. PostgreSQL
// uses row locks with SKIP LOCKED so multiple projector workers can drain the
// same outbox without serializing unrelated rows; SQLite conformance tests use
// the same transaction without the PostgreSQL-only locking clause.
func (store *KernelStore) ClaimTransitions(
	ctx context.Context,
	request kernel.TransitionClaimRequest,
) ([]kernel.TransitionClaim, error) {
	if store == nil || store.db == nil || strings.TrimSpace(request.WorkerID) == "" || request.Limit <= 0 ||
		request.LeaseDuration <= 0 || request.Now.IsZero() {
		return nil, kernel.ErrInvalidInput
	}
	var claims []kernel.TransitionClaim
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Where("available_at <= ? AND (lease_until IS NULL OR lease_until <= ?)", request.Now.UTC(), request.Now.UTC()).
			Order("available_at ASC, committed_at ASC, id ASC").Limit(request.Limit)
		if tx.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
		}
		var records []models.KernelTransitionOutboxRecord
		if err := query.Find(&records).Error; err != nil {
			return translateKernelError(err)
		}
		claims = make([]kernel.TransitionClaim, 0, len(records))
		for _, record := range records {
			record.Attempts++
			record.WorkerID = strings.TrimSpace(request.WorkerID)
			record.LeaseID = fmt.Sprintf("%s:%s:%d", record.WorkerID, record.ID, record.Attempts)
			leaseUntil := request.Now.UTC().Add(request.LeaseDuration)
			record.LeaseUntil = &leaseUntil
			result := tx.Model(&models.KernelTransitionOutboxRecord{}).
				Where("id = ? AND attempts = ?", record.ID, record.Attempts-1).
				Updates(map[string]interface{}{
					"attempts": record.Attempts, "worker_id": record.WorkerID,
					"lease_id": record.LeaseID, "lease_until": leaseUntil,
				})
			if result.Error != nil {
				return translateKernelApplyError(result.Error)
			}
			if result.RowsAffected != 1 {
				return kernel.ErrConflict
			}
			transition, err := kernelTransitionFrom(record)
			if err != nil {
				return err
			}
			claims = append(claims, kernel.TransitionClaim{
				Transition: transition, LeaseID: record.LeaseID, WorkerID: record.WorkerID, LeaseUntil: leaseUntil,
			})
		}
		return nil
	})
	return claims, err
}

// AckTransition removes one successfully projected committed transition.
func (store *KernelStore) AckTransition(ctx context.Context, request kernel.TransitionLeaseRequest) error {
	if store == nil || store.db == nil || !validKernelTransitionLease(request) {
		return kernel.ErrInvalidInput
	}
	result := store.db.WithContext(ctx).Where(
		"id = ? AND lease_id = ? AND worker_id = ?", request.TransitionID, request.LeaseID, request.WorkerID,
	).Delete(&models.KernelTransitionOutboxRecord{})
	if result.Error != nil {
		return translateKernelError(result.Error)
	}
	if result.RowsAffected == 1 {
		return nil
	}
	return store.kernelTransitionMissingOrConflict(store.db.WithContext(ctx), request.TransitionID)
}

// RetryTransition releases a failed projection for later retry.
func (store *KernelStore) RetryTransition(ctx context.Context, request kernel.TransitionRetryRequest) error {
	if store == nil || store.db == nil || !validKernelTransitionLease(request.TransitionLeaseRequest) {
		return kernel.ErrInvalidInput
	}
	result := store.db.WithContext(ctx).Model(&models.KernelTransitionOutboxRecord{}).
		Where("id = ? AND lease_id = ? AND worker_id = ?", request.TransitionID, request.LeaseID, request.WorkerID).
		Updates(map[string]interface{}{
			"available_at": request.AvailableAt.UTC(), "lease_id": "", "worker_id": "", "lease_until": nil,
		})
	if result.Error != nil {
		return translateKernelError(result.Error)
	}
	if result.RowsAffected == 1 {
		return nil
	}
	return store.kernelTransitionMissingOrConflict(store.db.WithContext(ctx), request.TransitionID)
}

func (store *KernelStore) kernelMissingOrConflict(db *gorm.DB, runID string) error {
	var count int64
	if err := db.Model(&models.KernelRunRecord{}).Where("run_id = ?", runID).Count(&count).Error; err != nil {
		return translateKernelError(err)
	}
	if count == 0 {
		return kernel.ErrNotFound
	}
	return kernel.ErrConflict
}

func kernelRunRecordFrom(record kernel.Record, lastEventSeq int64) (models.KernelRunRecord, error) {
	checkpointJSON, err := marshalOptional(record.Checkpoint)
	if err != nil {
		return models.KernelRunRecord{}, err
	}
	resultJSON, err := marshalOptional(record.Result)
	if err != nil {
		return models.KernelRunRecord{}, err
	}
	if !json.Valid(record.State) {
		return models.KernelRunRecord{}, errKernelStateJSON
	}
	return models.KernelRunRecord{
		RunID: record.Run.ID, RequestID: record.Run.RequestID, Kind: string(record.Run.Kind),
		TenantID: record.Run.Actor.TenantID, ActorID: record.Run.Actor.ActorID,
		ThreadKind: record.Run.Thread.Kind, ThreadID: record.Run.Thread.ID,
		Goal: record.Run.Goal, Status: string(record.Run.Status), Revision: record.Run.Revision,
		ErrorCode: record.Run.ErrorCode, ErrorDetail: record.Run.ErrorDetail,
		DeadlineAt: cloneTime(record.Run.DeadlineAt), EndedAt: cloneTime(record.Run.EndedAt),
		StateJSON: string(record.State), CheckpointJSON: checkpointJSON, ResultJSON: resultJSON,
		LastEventSeq: lastEventSeq, CreatedAt: record.Run.CreatedAt.UTC(), UpdatedAt: record.Run.UpdatedAt.UTC(),
	}, nil
}

func kernelRunUpdates(record models.KernelRunRecord, eventCount int) map[string]interface{} {
	return map[string]interface{}{
		"request_id": record.RequestID, "kind": record.Kind,
		kernelColumnTenantID: record.TenantID, "actor_id": record.ActorID,
		"thread_kind": record.ThreadKind, "thread_id": record.ThreadID,
		"goal": record.Goal, kernelColumnStatus: record.Status, kernelColumnRevision: record.Revision,
		kernelColumnErrorCode: record.ErrorCode, kernelColumnErrorDetail: record.ErrorDetail,
		"deadline_at": record.DeadlineAt, kernelColumnEndedAt: record.EndedAt,
		"state_json": record.StateJSON, "checkpoint_json": record.CheckpointJSON,
		"result_json":            record.ResultJSON,
		kernelColumnLastEventSeq: gorm.Expr(kernelColumnLastEventSeq+" + ?", eventCount),
		"created_at":             record.CreatedAt, kernelColumnUpdatedAt: record.UpdatedAt,
	}
}

func createKernelEvents(
	db *gorm.DB,
	runID string,
	currentSeq int64,
	createdAt time.Time,
	drafts []kernel.EventDraft,
) error {
	if len(drafts) == 0 {
		return nil
	}
	records := make([]models.KernelEventRecord, 0, len(drafts))
	for index, draft := range drafts {
		data := ""
		if len(draft.Data) > 0 {
			if !json.Valid(draft.Data) {
				return errKernelEventJSON
			}
			data = string(draft.Data)
		}
		records = append(records, models.KernelEventRecord{
			RunID: runID, Seq: currentSeq + int64(index) + 1,
			Type: draft.Type, Message: draft.Message, DataJSON: data, CreatedAt: createdAt.UTC(),
		})
	}
	return translateKernelError(db.Create(&records).Error)
}

func createKernelTransition(db *gorm.DB, run kernel.Run, drafts []kernel.EventDraft) error {
	if !kernel.NeedsTransitionProjection(run.Status, drafts) {
		return nil
	}
	encoded, err := json.Marshal(drafts)
	if err != nil {
		return errKernelEventJSON
	}
	record := models.KernelTransitionOutboxRecord{
		ID: fmt.Sprintf("%s:%d", run.ID, run.Revision), RunID: run.ID,
		Kind: string(run.Kind), Status: string(run.Status), Revision: run.Revision,
		EventsJSON: string(encoded), CommittedAt: run.UpdatedAt.UTC(), AvailableAt: run.UpdatedAt.UTC(),
	}
	return translateKernelError(db.Create(&record).Error)
}

func kernelTransitionFrom(record models.KernelTransitionOutboxRecord) (kernel.CommittedTransition, error) {
	var drafts []kernel.EventDraft
	if err := json.Unmarshal([]byte(record.EventsJSON), &drafts); err != nil {
		return kernel.CommittedTransition{}, errPersistedKernelEventJSON
	}
	return kernel.CommittedTransition{
		ID: record.ID, RunID: record.RunID, Kind: kernel.RunKind(record.Kind),
		Status: kernel.RunStatus(record.Status), Revision: record.Revision,
		Events: drafts, CommittedAt: record.CommittedAt.UTC(), Attempts: record.Attempts,
	}, nil
}

func validKernelTransitionLease(request kernel.TransitionLeaseRequest) bool {
	return strings.TrimSpace(request.TransitionID) != "" && strings.TrimSpace(request.LeaseID) != "" &&
		strings.TrimSpace(request.WorkerID) != ""
}

func kernelSnapshotFrom(
	runRecord models.KernelRunRecord,
	eventRecords []models.KernelEventRecord,
) (kernel.Snapshot, error) {
	checkpoint, hasCheckpoint, err := unmarshalCheckpoint(runRecord.CheckpointJSON)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	result, hasResult, err := unmarshalResult(runRecord.ResultJSON)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	state := json.RawMessage(runRecord.StateJSON)
	if !json.Valid(state) {
		return kernel.Snapshot{}, errPersistedKernelStateJSON
	}
	events := make([]kernel.Event, 0, len(eventRecords))
	for _, record := range eventRecords {
		data := json.RawMessage(nil)
		if record.DataJSON != "" {
			data = json.RawMessage(record.DataJSON)
			if !json.Valid(data) {
				return kernel.Snapshot{}, errPersistedKernelEventJSON
			}
		}
		events = append(events, kernel.Event{
			Seq: record.Seq, Type: record.Type, Message: record.Message,
			Data: append(json.RawMessage(nil), data...), CreatedAt: record.CreatedAt.UTC(),
		})
	}
	snapshot := kernel.Snapshot{
		Run: kernel.Run{
			ID: runRecord.RunID, Kind: kernel.RunKind(runRecord.Kind),
			Actor:     kernel.ActorRef{TenantID: runRecord.TenantID, ActorID: runRecord.ActorID},
			Thread:    kernel.ThreadRef{Kind: runRecord.ThreadKind, ID: runRecord.ThreadID},
			RequestID: runRecord.RequestID, Goal: runRecord.Goal,
			Status: kernel.RunStatus(runRecord.Status), Revision: runRecord.Revision,
			ErrorCode: runRecord.ErrorCode, ErrorDetail: runRecord.ErrorDetail,
			DeadlineAt: cloneTime(runRecord.DeadlineAt), EndedAt: cloneTime(runRecord.EndedAt),
			CreatedAt: runRecord.CreatedAt.UTC(), UpdatedAt: runRecord.UpdatedAt.UTC(),
		},
		State: append(json.RawMessage(nil), state...), Events: events,
	}
	if hasCheckpoint {
		snapshot.Checkpoint = &checkpoint
	}
	if hasResult {
		snapshot.Result = &result
	}
	return snapshot, nil
}

func marshalOptional(value interface{}) (string, error) {
	if value == nil {
		return "", nil
	}
	switch typed := value.(type) {
	case *kernel.Checkpoint:
		if typed == nil {
			return "", nil
		}
	case *kernel.Result:
		if typed == nil {
			return "", nil
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func unmarshalCheckpoint(value string) (kernel.Checkpoint, bool, error) {
	if value == "" || value == "null" {
		return kernel.Checkpoint{}, false, nil
	}
	var checkpoint kernel.Checkpoint
	if err := json.Unmarshal([]byte(value), &checkpoint); err != nil {
		return kernel.Checkpoint{}, false, err
	}
	return checkpoint, true, nil
}

func unmarshalResult(value string) (kernel.Result, bool, error) {
	if value == "" || value == "null" {
		return kernel.Result{}, false, nil
	}
	var result kernel.Result
	if err := json.Unmarshal([]byte(value), &result); err != nil {
		return kernel.Result{}, false, err
	}
	return result, true, nil
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := value.UTC()
	return &cloned
}

func translateKernelError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "database table is locked") || strings.Contains(message, "database is locked") {
		return kernel.ErrConflict
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return kernel.ErrNotFound
	}
	if isKernelUniqueConstraint(err) {
		return kernel.ErrAlreadyExists
	}
	return err
}

func translateKernelApplyError(err error) error {
	return translateKernelError(err)
}

package harnesspostgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	harness "github.com/orz-i/Gaoge/sdk/go/agent-runtime-harness"
	runtimecontext "github.com/orz-i/Gaoge/sdk/go/agent-runtime/context"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrNilDatabase = errors.New("harness postgres database is nil")

const rowLockStrengthUpdate = "UPDATE"

// Store persists Harness Session/Turn/Invocation/Config/Item state in PostgreSQL-compatible GORM databases.
type Store struct{ db *gorm.DB }

// New creates a durable Harness Store. Schema migration remains an explicit host operation.
func New(db *gorm.DB) (*Store, error) {
	if db == nil {
		return nil, ErrNilDatabase
	}
	return &Store{db: db}, nil
}

func sameInvocation(left, right harness.Invocation) bool {
	return left.ID == right.ID && left.TurnID == right.TurnID && left.ParentItemID == right.ParentItemID &&
		left.CapabilityKey == right.CapabilityKey && left.DefinitionVersion == right.DefinitionVersion &&
		left.ExecutionClass == right.ExecutionClass && string(left.Input) == string(right.Input) &&
		left.InputHash == right.InputHash && left.Attempt == right.Attempt
}

func invocationToRecord(value harness.Invocation) (invocationRecord, error) {
	if value.ID == "" || value.TurnID == "" || value.CapabilityKey == "" || value.Attempt <= 0 || value.Revision == 0 {
		return invocationRecord{}, harness.ErrInvalidRequest
	}
	outputRefs, err := json.Marshal(value.OutputRefs)
	if err != nil {
		return invocationRecord{}, err
	}
	return invocationRecord{
		ID: value.ID, TurnID: value.TurnID, ParentItemID: value.ParentItemID,
		CapabilityKey: value.CapabilityKey, DefinitionVersion: value.DefinitionVersion,
		ExecutionClass: string(value.ExecutionClass), InputJSON: string(value.Input), InputHash: value.InputHash, ExecutionRefID: value.ExecutionRefID,
		Status: string(value.Status), Attempt: value.Attempt, OutputRefsJSON: string(outputRefs),
		ErrorCode: value.ErrorCode, ErrorDetail: value.ErrorDetail, Revision: value.Revision,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}, nil
}

func invocationFromRecord(value invocationRecord) (harness.Invocation, error) {
	var outputRefs []harness.HostRef
	if err := json.Unmarshal([]byte(value.OutputRefsJSON), &outputRefs); err != nil {
		return harness.Invocation{}, harness.ErrConflict
	}
	return harness.Invocation{
		ID: value.ID, TurnID: value.TurnID, ParentItemID: value.ParentItemID,
		CapabilityKey: value.CapabilityKey, DefinitionVersion: value.DefinitionVersion,
		ExecutionClass: harness.ExecutionClass(value.ExecutionClass), Input: json.RawMessage(value.InputJSON), InputHash: value.InputHash,
		ExecutionRefID: value.ExecutionRefID, Status: harness.InvocationStatus(value.Status), Attempt: value.Attempt,
		OutputRefs: outputRefs, ErrorCode: value.ErrorCode, ErrorDetail: value.ErrorDetail, Revision: value.Revision,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}, nil
}

func (store *Store) CreateInvocation(ctx context.Context, value harness.Invocation) (harness.Invocation, bool, error) {
	record, err := invocationToRecord(value)
	if err != nil {
		return harness.Invocation{}, false, err
	}
	return createOrReplay(ctx, store.db, &record, value,
		func() (harness.Invocation, error) { return store.GetInvocation(ctx, value.ID) }, sameInvocation)
}

func (store *Store) GetInvocation(ctx context.Context, id string) (harness.Invocation, error) {
	var record invocationRecord
	if err := store.db.WithContext(ctx).Where("id = ?", strings.TrimSpace(id)).Take(&record).Error; err != nil {
		return harness.Invocation{}, mapError(err)
	}
	return invocationFromRecord(record)
}

func (store *Store) GetInvocationByExecutionRefID(ctx context.Context, executionRefID string) (harness.Invocation, error) {
	executionRefID = strings.TrimSpace(executionRefID)
	if executionRefID == "" {
		return harness.Invocation{}, harness.ErrInvalidRequest
	}
	var record invocationRecord
	if err := store.db.WithContext(ctx).Where("execution_ref_id = ?", executionRefID).Take(&record).Error; err != nil {
		return harness.Invocation{}, mapError(err)
	}
	return invocationFromRecord(record)
}

func (store *Store) UpdateInvocation(ctx context.Context, value harness.Invocation, expectedRevision uint64) (harness.Invocation, error) {
	if strings.TrimSpace(value.ID) == "" || expectedRevision == 0 {
		return harness.Invocation{}, harness.ErrInvalidRequest
	}
	record, err := invocationToRecord(value)
	if err != nil {
		return harness.Invocation{}, err
	}
	record.Revision = expectedRevision + 1
	result := store.db.WithContext(ctx).Model(&invocationRecord{}).
		Where("id = ? AND revision = ? AND turn_id = ? AND parent_item_id = ? AND capability_key = ? AND definition_version = ? AND execution_class = ? AND input_json = ? AND input_hash = ? AND attempt = ? AND execution_ref_id = ?",
			value.ID, expectedRevision, value.TurnID, value.ParentItemID, value.CapabilityKey, value.DefinitionVersion,
			string(value.ExecutionClass), record.InputJSON, value.InputHash, value.Attempt, value.ExecutionRefID).
		Updates(map[string]any{
			"status": record.Status, "output_refs_json": record.OutputRefsJSON,
			"error_code": record.ErrorCode, "error_detail": record.ErrorDetail,
			"revision": record.Revision, "updated_at": record.UpdatedAt,
		})
	if result.Error != nil {
		return harness.Invocation{}, result.Error
	}
	if result.RowsAffected != 1 {
		return harness.Invocation{}, harness.ErrConflict
	}
	value.Revision = record.Revision
	return value, nil
}

func (store *Store) RetryInvocation(
	ctx context.Context,
	invocationID string,
	expectedRevision uint64,
	nextExecutionRefID string,
	now time.Time,
) (harness.Invocation, error) {
	invocationID = strings.TrimSpace(invocationID)
	nextExecutionRefID = strings.TrimSpace(nextExecutionRefID)
	if invalidInvocationRetryRequest(invocationID, expectedRevision, nextExecutionRefID, now) {
		return harness.Invocation{}, harness.ErrInvalidRequest
	}
	current, err := store.GetInvocation(ctx, invocationID)
	if err != nil {
		return harness.Invocation{}, err
	}
	if !validStoredInvocationRetry(current, expectedRevision) {
		return harness.Invocation{}, harness.ErrConflict
	}
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if strings.TrimSpace(current.ParentItemID) == "" {
			result := tx.Model(&turnRecord{}).
				Where("id = ? AND status IN ?", current.TurnID,
					[]string{string(harness.TurnFailed), string(harness.TurnCancelled)}).
				Updates(map[string]any{
					"status": string(harness.TurnRunning), "error_code": "", "error_detail": "",
					"revision": gorm.Expr("revision + ?", 1), "updated_at": now.UTC(),
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return harness.ErrConflict
			}
		}
		return rotateInvocationAttempt(tx, current, expectedRevision, nextExecutionRefID, now)
	})
	if err != nil {
		return harness.Invocation{}, err
	}
	return store.GetInvocation(ctx, current.ID)
}

func invalidInvocationRetryRequest(
	invocationID string,
	expectedRevision uint64,
	nextExecutionRefID string,
	now time.Time,
) bool {
	return invocationID == "" || expectedRevision == 0 || nextExecutionRefID == "" || now.IsZero()
}

func validStoredInvocationRetry(current harness.Invocation, expectedRevision uint64) bool {
	return current.Revision == expectedRevision && retryableInvocationStatus(current.Status) && len(current.Input) > 0
}

func rotateInvocationAttempt(
	db *gorm.DB,
	current harness.Invocation,
	expectedRevision uint64,
	nextExecutionRefID string,
	now time.Time,
) error {
	result := db.Model(&invocationRecord{}).
		Where("id = ? AND revision = ? AND attempt = ? AND execution_ref_id = ? AND status IN ?",
			current.ID, expectedRevision, current.Attempt, current.ExecutionRefID,
			[]string{string(harness.InvocationFailed), string(harness.InvocationCancelled)}).
		Updates(map[string]any{
			"execution_ref_id": nextExecutionRefID, "status": string(harness.InvocationAccepted),
			"attempt": current.Attempt + 1, "output_refs_json": "[]", "error_code": "", "error_detail": "",
			"revision": expectedRevision + 1, "updated_at": now.UTC(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return harness.ErrConflict
	}
	return nil
}

func retryableInvocationStatus(status harness.InvocationStatus) bool {
	return status == harness.InvocationFailed || status == harness.InvocationCancelled
}

func (store *Store) ListInvocations(ctx context.Context, turnID string) ([]harness.Invocation, error) {
	return listTurnRecords(ctx, store.db, turnID, invocationFromRecord)
}

func (store *Store) CreateInteraction(
	ctx context.Context,
	value harness.Interaction,
	expectedTurnRevision uint64,
	expectedInvocationRevision uint64,
) (harness.Interaction, bool, error) {
	record, err := interactionToRecord(value)
	if err != nil || expectedTurnRevision == 0 || expectedInvocationRevision == 0 {
		if err == nil {
			err = harness.ErrInvalidRequest
		}
		return harness.Interaction{}, false, err
	}
	var created harness.Interaction
	fresh := false
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		created, fresh, err = createInteractionTransaction(
			tx, record, value, expectedTurnRevision, expectedInvocationRevision,
		)
		return err
	})
	if err != nil {
		return harness.Interaction{}, false, err
	}
	return created, fresh, nil
}

func createInteractionTransaction(
	tx *gorm.DB,
	record interactionRecord,
	value harness.Interaction,
	expectedTurnRevision uint64,
	expectedInvocationRevision uint64,
) (harness.Interaction, bool, error) {
	existing, replayed, err := replayInteractionRecord(tx, record.ID, value)
	if err != nil || replayed {
		return existing, false, err
	}
	if err = lockInteractionOwners(tx, value, expectedTurnRevision, expectedInvocationRevision); err != nil {
		return harness.Interaction{}, false, err
	}
	if err = ensureNoWaitingInteraction(tx, value.TurnID); err != nil {
		return harness.Interaction{}, false, err
	}
	if err = tx.Create(&record).Error; err != nil {
		return harness.Interaction{}, false, err
	}
	created, err := interactionFromRecord(record)
	return created, err == nil, err
}

func replayInteractionRecord(
	tx *gorm.DB,
	interactionID string,
	candidate harness.Interaction,
) (harness.Interaction, bool, error) {
	var record interactionRecord
	err := tx.Clauses(clause.Locking{Strength: rowLockStrengthUpdate}).Where("id = ?", interactionID).Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return harness.Interaction{}, false, nil
	}
	if err != nil {
		return harness.Interaction{}, false, err
	}
	existing, err := interactionFromRecord(record)
	if err != nil {
		return harness.Interaction{}, true, err
	}
	if !sameInteraction(existing, candidate) {
		return harness.Interaction{}, true, harness.ErrConflict
	}
	return existing, true, nil
}

func lockInteractionOwners(
	tx *gorm.DB,
	value harness.Interaction,
	expectedTurnRevision uint64,
	expectedInvocationRevision uint64,
) error {
	var turn turnRecord
	err := tx.Clauses(clause.Locking{Strength: rowLockStrengthUpdate}).
		Where("id = ? AND revision = ? AND status = ?", value.TurnID, expectedTurnRevision, string(harness.TurnRunning)).
		Take(&turn).Error
	if err = interactionOwnerLockError(err); err != nil {
		return err
	}
	var invocation invocationRecord
	err = tx.Clauses(clause.Locking{Strength: rowLockStrengthUpdate}).
		Where("id = ? AND turn_id = ? AND revision = ? AND status IN ?", value.InvocationID, value.TurnID, expectedInvocationRevision,
			[]string{string(harness.InvocationAccepted), string(harness.InvocationRunning)}).
		Take(&invocation).Error
	return interactionOwnerLockError(err)
}

func interactionOwnerLockError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return harness.ErrConflict
	}
	return err
}

func ensureNoWaitingInteraction(tx *gorm.DB, turnID string) error {
	var waiting interactionRecord
	err := tx.Where("turn_id = ? AND status = ?", turnID, string(harness.InteractionWaiting)).Take(&waiting).Error
	if err == nil {
		return harness.ErrConflict
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	return err
}

func (store *Store) GetInteraction(ctx context.Context, id string) (harness.Interaction, error) {
	var record interactionRecord
	if err := store.db.WithContext(ctx).Where("id = ?", strings.TrimSpace(id)).Take(&record).Error; err != nil {
		return harness.Interaction{}, mapError(err)
	}
	return interactionFromRecord(record)
}

func (store *Store) UpdateInteraction(
	ctx context.Context,
	value harness.Interaction,
	expectedRevision uint64,
) (harness.Interaction, error) {
	if strings.TrimSpace(value.ID) == "" || expectedRevision == 0 {
		return harness.Interaction{}, harness.ErrInvalidRequest
	}
	record, err := interactionToRecord(value)
	if err != nil {
		return harness.Interaction{}, err
	}
	record.Revision = expectedRevision + 1
	result := store.db.WithContext(ctx).Model(&interactionRecord{}).
		Where("id = ? AND revision = ? AND turn_id = ? AND invocation_id = ? AND parent_item_id = ? AND key = ? AND kind = ? AND schema_json = ? AND presentation_json = ?",
			value.ID, expectedRevision, value.TurnID, value.InvocationID, value.ParentItemID, value.Key, string(value.Kind),
			record.SchemaJSON, record.PresentationJSON).
		Updates(map[string]any{
			"status": record.Status, "response_json": record.ResponseJSON,
			"revision": record.Revision, "updated_at": record.UpdatedAt,
		})
	if result.Error != nil {
		return harness.Interaction{}, result.Error
	}
	if result.RowsAffected != 1 {
		return harness.Interaction{}, harness.ErrConflict
	}
	value.Revision = record.Revision
	return value, nil
}

func (store *Store) ListInteractions(ctx context.Context, turnID string) ([]harness.Interaction, error) {
	return listTurnRecords(ctx, store.db, turnID, interactionFromRecord)
}

func listTurnRecords[Record any, Value any](
	ctx context.Context,
	db *gorm.DB,
	turnID string,
	convert func(Record) (Value, error),
) ([]Value, error) {
	turnID = strings.TrimSpace(turnID)
	if db == nil || turnID == "" {
		return nil, harness.ErrInvalidRequest
	}
	var records []Record
	if err := db.WithContext(ctx).Where("turn_id = ?", turnID).Order("created_at ASC, id ASC").Find(&records).Error; err != nil {
		return nil, err
	}
	result := make([]Value, len(records))
	for index, record := range records {
		value, err := convert(record)
		if err != nil {
			return nil, err
		}
		result[index] = value
	}
	return result, nil
}

func sameInteraction(left, right harness.Interaction) bool {
	return left.ID == right.ID && left.TurnID == right.TurnID && left.InvocationID == right.InvocationID &&
		left.ParentItemID == right.ParentItemID && left.Key == right.Key && left.Kind == right.Kind &&
		string(left.Schema) == string(right.Schema) && string(left.Presentation) == string(right.Presentation)
}

func interactionToRecord(value harness.Interaction) (interactionRecord, error) {
	if value.ID == "" || value.TurnID == "" || value.InvocationID == "" || value.Key == "" ||
		len(value.Schema) == 0 || !json.Valid(value.Schema) || value.Revision == 0 {
		return interactionRecord{}, harness.ErrInvalidRequest
	}
	return interactionRecord{
		ID: value.ID, TurnID: value.TurnID, InvocationID: value.InvocationID, ParentItemID: value.ParentItemID,
		Key: value.Key, Kind: string(value.Kind), SchemaJSON: string(value.Schema),
		PresentationJSON: string(value.Presentation), Status: string(value.Status), ResponseJSON: string(value.Response),
		Revision: value.Revision, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}, nil
}

func interactionFromRecord(value interactionRecord) (harness.Interaction, error) {
	if !json.Valid([]byte(value.SchemaJSON)) || value.PresentationJSON != "" && !json.Valid([]byte(value.PresentationJSON)) ||
		value.ResponseJSON != "" && !json.Valid([]byte(value.ResponseJSON)) {
		return harness.Interaction{}, harness.ErrConflict
	}
	return harness.Interaction{
		ID: value.ID, TurnID: value.TurnID, InvocationID: value.InvocationID, ParentItemID: value.ParentItemID,
		Key: value.Key, Kind: harness.InteractionKind(value.Kind), Schema: json.RawMessage(value.SchemaJSON),
		Presentation: json.RawMessage(value.PresentationJSON), Status: harness.InteractionStatus(value.Status),
		Response: json.RawMessage(value.ResponseJSON), Revision: value.Revision,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}, nil
}

func createOrReplay[T any](
	ctx context.Context,
	db *gorm.DB,
	record any,
	value T,
	load func() (T, error),
	same func(T, T) bool,
) (T, bool, error) {
	if err := db.WithContext(ctx).Create(record).Error; err == nil {
		return value, true, nil
	}
	existing, err := load()
	if err != nil || !same(existing, value) {
		var zero T
		return zero, false, harness.ErrConflict
	}
	return existing, false, nil
}

// Models returns the complete Harness persistence model set.
func Models() []any {
	return []any{&sessionRecord{}, &turnRecord{}, &invocationRecord{}, &interactionRecord{}, &configRecord{}, &contextSnapshotRecord{}, &itemRecord{}}
}

// Migrate creates or updates only Harness-owned persistence tables.
func Migrate(db *gorm.DB) error {
	if db == nil {
		return ErrNilDatabase
	}
	return db.AutoMigrate(Models()...)
}

func (store *Store) CreateSession(ctx context.Context, value harness.Session) (harness.Session, bool, error) {
	record := sessionToRecord(value)
	if record.ID == "" {
		return harness.Session{}, false, harness.ErrInvalidRequest
	}
	return createOrReplay(ctx, store.db, &record, value,
		func() (harness.Session, error) { return store.GetSession(ctx, value.ID) }, sameSession)
}

func (store *Store) GetSession(ctx context.Context, id string) (harness.Session, error) {
	var record sessionRecord
	if err := store.db.WithContext(ctx).Where("id = ?", strings.TrimSpace(id)).Take(&record).Error; err != nil {
		return harness.Session{}, mapError(err)
	}
	return sessionFromRecord(record), nil
}

func (store *Store) CreateTurn(ctx context.Context, value harness.Turn) (harness.Turn, bool, error) {
	record := turnToRecord(value)
	if record.ID == "" {
		return harness.Turn{}, false, harness.ErrInvalidRequest
	}
	return createOrReplay(ctx, store.db, &record, value,
		func() (harness.Turn, error) { return store.GetTurn(ctx, value.ID) }, sameTurn)
}

func (store *Store) GetTurn(ctx context.Context, id string) (harness.Turn, error) {
	var record turnRecord
	if err := store.db.WithContext(ctx).Where("id = ?", strings.TrimSpace(id)).Take(&record).Error; err != nil {
		return harness.Turn{}, mapError(err)
	}
	return turnFromRecord(record), nil
}

func (store *Store) UpdateTurn(ctx context.Context, value harness.Turn, expectedRevision uint64) (harness.Turn, error) {
	if strings.TrimSpace(value.ID) == "" || expectedRevision == 0 {
		return harness.Turn{}, harness.ErrInvalidRequest
	}
	record := turnToRecord(value)
	record.Revision = expectedRevision + 1
	result := store.db.WithContext(ctx).Model(&turnRecord{}).
		Where("id = ? AND revision = ? AND session_id = ? AND host_turn_kind = ? AND host_turn_id = ? AND config_snapshot_id = ?",
			value.ID, expectedRevision, value.SessionID, value.HostTurn.Kind, value.HostTurn.ID, value.ConfigSnapshotID).
		Updates(map[string]any{
			"context_snapshot_id": record.ContextSnapshotID, "context_ref_id": record.ContextRefID,
			"context_ref_revision": record.ContextRefRevision, "context_path_hash": record.ContextPathHash,
			"context_content_hash": record.ContextContentHash,
			"status":               record.Status, "revision": record.Revision, "error_code": record.ErrorCode,
			"error_detail": record.ErrorDetail, "updated_at": record.UpdatedAt,
		})
	if result.Error != nil {
		return harness.Turn{}, result.Error
	}
	if result.RowsAffected != 1 {
		return harness.Turn{}, harness.ErrConflict
	}
	value.Revision = record.Revision
	return value, nil
}

func (store *Store) PutConfigSnapshot(ctx context.Context, value harness.ConfigSnapshot) (harness.ConfigSnapshot, bool, error) {
	record, err := configToRecord(value)
	if err != nil {
		return harness.ConfigSnapshot{}, false, err
	}
	if err = store.db.WithContext(ctx).Create(&record).Error; err == nil {
		return value, true, nil
	}
	existing, loadErr := store.GetConfigSnapshot(ctx, value.ID)
	if loadErr != nil || existing.TurnID != value.TurnID || existing.ContentHash != value.ContentHash {
		return harness.ConfigSnapshot{}, false, harness.ErrConflict
	}
	return existing, false, nil
}

func (store *Store) GetConfigSnapshot(ctx context.Context, id string) (harness.ConfigSnapshot, error) {
	var record configRecord
	if err := store.db.WithContext(ctx).Where("id = ?", strings.TrimSpace(id)).Take(&record).Error; err != nil {
		return harness.ConfigSnapshot{}, mapError(err)
	}
	var value harness.ConfigSnapshot
	if err := json.Unmarshal([]byte(record.PayloadJSON), &value); err != nil {
		return harness.ConfigSnapshot{}, harness.ErrConflict
	}
	return value, nil
}

func (store *Store) PutContextSnapshot(
	ctx context.Context,
	value runtimecontext.Snapshot,
) (runtimecontext.Snapshot, bool, error) {
	if !validContextSnapshot(value) {
		return runtimecontext.Snapshot{}, false, harness.ErrInvalidRequest
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return runtimecontext.Snapshot{}, false, err
	}
	record := contextSnapshotRecord{ID: value.ID, RunID: value.RunID, PayloadJSON: string(payload)}
	if err = store.db.WithContext(ctx).Create(&record).Error; err == nil {
		return value, true, nil
	}
	existing, loadErr := store.GetContextSnapshot(ctx, value.ID)
	if loadErr != nil || !reflect.DeepEqual(existing, value) {
		return runtimecontext.Snapshot{}, false, harness.ErrConflict
	}
	return existing, false, nil
}

func validContextSnapshot(value runtimecontext.Snapshot) bool {
	return strings.TrimSpace(value.ID) != "" && strings.TrimSpace(value.RunID) != "" && value.Revision > 0 &&
		strings.TrimSpace(value.ThreadPathHash) != "" && strings.TrimSpace(value.ContentHash) != "" &&
		len(value.Content) > 0 && json.Valid(value.Content)
}

func (store *Store) GetContextSnapshot(ctx context.Context, id string) (runtimecontext.Snapshot, error) {
	var record contextSnapshotRecord
	if err := store.db.WithContext(ctx).Where("id = ?", strings.TrimSpace(id)).Take(&record).Error; err != nil {
		return runtimecontext.Snapshot{}, mapError(err)
	}
	var value runtimecontext.Snapshot
	if err := json.Unmarshal([]byte(record.PayloadJSON), &value); err != nil || value.ID != record.ID || value.RunID != record.RunID {
		return runtimecontext.Snapshot{}, harness.ErrConflict
	}
	return value, nil
}

func (store *Store) AppendItem(ctx context.Context, value harness.Item) (harness.Item, bool, error) {
	if strings.TrimSpace(value.ID) == "" || strings.TrimSpace(value.TurnID) == "" {
		return harness.Item{}, false, harness.ErrInvalidRequest
	}
	outcome := itemAppendOutcome{}
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return appendItemTx(tx, value, &outcome)
	})
	return outcome.item, outcome.created, err
}

type itemAppendOutcome struct {
	item    harness.Item
	created bool
}

func appendItemTx(tx *gorm.DB, value harness.Item, outcome *itemAppendOutcome) error {
	existing, found, err := findItem(tx, value.ID)
	if err != nil {
		return err
	}
	if found {
		if !sameItem(existing, value) {
			return harness.ErrConflict
		}
		outcome.item = existing
		return nil
	}
	if err = lockTurn(tx, value.TurnID); err != nil {
		return err
	}
	seq, err := nextItemSeq(tx, value.TurnID)
	if err != nil {
		return err
	}
	value.Seq = seq
	record, err := itemToRecord(value)
	if err != nil {
		return err
	}
	if err = tx.Create(&record).Error; err != nil {
		return err
	}
	outcome.item, outcome.created = value, true
	return nil
}

func (store *Store) ListItems(ctx context.Context, turnID string, afterSeq uint64, limit int) ([]harness.Item, error) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" || limit < 0 {
		return nil, harness.ErrInvalidRequest
	}
	if limit == 0 || limit > 500 {
		limit = 500
	}
	var records []itemRecord
	if err := store.db.WithContext(ctx).Where("turn_id = ? AND seq > ?", turnID, afterSeq).
		Order("seq ASC").Limit(limit).Find(&records).Error; err != nil {
		return nil, err
	}
	result := make([]harness.Item, len(records))
	for index, record := range records {
		value, err := itemFromRecord(record)
		if err != nil {
			return nil, err
		}
		result[index] = value
	}
	return result, nil
}

func lockTurn(tx *gorm.DB, turnID string) error {
	var record turnRecord
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id = ?", turnID).Take(&record).Error
	return mapError(err)
}

func nextItemSeq(tx *gorm.DB, turnID string) (uint64, error) {
	var maxSeq struct{ Value uint64 }
	err := tx.Model(&itemRecord{}).Select("COALESCE(MAX(seq), 0) AS value").Where("turn_id = ?", turnID).Scan(&maxSeq).Error
	return maxSeq.Value + 1, err
}

func findItem(tx *gorm.DB, id string) (harness.Item, bool, error) {
	var record itemRecord
	err := tx.Where("id = ?", id).Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return harness.Item{}, false, nil
	}
	if err != nil {
		return harness.Item{}, false, err
	}
	value, err := itemFromRecord(record)
	return value, err == nil, err
}

func sessionToRecord(value harness.Session) sessionRecord {
	return sessionRecord{
		ID: value.ID, HostThreadKind: value.HostThread.Kind, HostThreadID: value.HostThread.ID,
		TenantID: value.Actor.TenantID, ActorID: value.Actor.ActorID, Revision: value.Revision,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func sessionFromRecord(value sessionRecord) harness.Session {
	return harness.Session{
		ID: value.ID, HostThread: harness.HostRef{Kind: value.HostThreadKind, ID: value.HostThreadID},
		Actor: kernel.ActorRef{TenantID: value.TenantID, ActorID: value.ActorID}, Revision: value.Revision,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func turnToRecord(value harness.Turn) turnRecord {
	return turnRecord{
		ID: value.ID, SessionID: value.SessionID, HostTurnKind: value.HostTurn.Kind, HostTurnID: value.HostTurn.ID,
		ConfigSnapshotID: value.ConfigSnapshotID, ContextSnapshotID: value.ContextSnapshotID,
		ContextRefID: value.ContextRef.ID, ContextRefRevision: value.ContextRef.Revision,
		ContextPathHash: value.ContextRef.ThreadPathHash, ContextContentHash: value.ContextRef.ContentHash,
		Status: string(value.Status), Revision: value.Revision, ErrorCode: value.ErrorCode, ErrorDetail: value.ErrorDetail,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func turnFromRecord(value turnRecord) harness.Turn {
	return harness.Turn{
		ID: value.ID, SessionID: value.SessionID, HostTurn: harness.HostRef{Kind: value.HostTurnKind, ID: value.HostTurnID},
		ConfigSnapshotID: value.ConfigSnapshotID, ContextSnapshotID: value.ContextSnapshotID,
		ContextRef: harness.ContextRef{ID: value.ContextRefID, Revision: value.ContextRefRevision,
			ThreadPathHash: value.ContextPathHash, ContentHash: value.ContextContentHash},
		Status: harness.TurnStatus(value.Status), Revision: value.Revision, ErrorCode: value.ErrorCode, ErrorDetail: value.ErrorDetail,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func configToRecord(value harness.ConfigSnapshot) (configRecord, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return configRecord{}, err
	}
	if value.ID == "" || value.TurnID == "" || value.ContentHash == "" {
		return configRecord{}, harness.ErrInvalidRequest
	}
	return configRecord{ID: value.ID, TurnID: value.TurnID, ContentHash: value.ContentHash, PayloadJSON: string(raw), CreatedAt: value.CreatedAt}, nil
}

func itemToRecord(value harness.Item) (itemRecord, error) {
	if value.ID == "" || value.TurnID == "" || value.Seq == 0 {
		return itemRecord{}, harness.ErrInvalidRequest
	}
	hostKind, hostID := "", ""
	if value.HostRef != nil {
		hostKind, hostID = value.HostRef.Kind, value.HostRef.ID
	}
	payload := strings.TrimSpace(string(value.Payload))
	if payload == "" {
		payload = `{}`
	}
	if !json.Valid([]byte(payload)) {
		return itemRecord{}, harness.ErrInvalidRequest
	}
	return itemRecord{
		ID: value.ID, TurnID: value.TurnID, Seq: value.Seq, Kind: string(value.Kind), Status: string(value.Status),
		HostRefKind: hostKind, HostRefID: hostID, RunID: value.RunID, InvocationID: value.InvocationID, ParentItemID: value.ParentItemID,
		PayloadJSON: payload, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}, nil
}

func itemFromRecord(value itemRecord) (harness.Item, error) {
	result := harness.Item{
		ID: value.ID, TurnID: value.TurnID, Seq: value.Seq, Kind: harness.ItemKind(value.Kind), Status: harness.ItemStatus(value.Status),
		RunID: value.RunID, InvocationID: value.InvocationID, ParentItemID: value.ParentItemID, Payload: json.RawMessage(value.PayloadJSON),
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
	if value.HostRefKind != "" || value.HostRefID != "" {
		result.HostRef = &harness.HostRef{Kind: value.HostRefKind, ID: value.HostRefID}
	}
	if !json.Valid(result.Payload) {
		return harness.Item{}, harness.ErrConflict
	}
	return result, nil
}

func sameSession(left, right harness.Session) bool {
	return left.ID == right.ID && left.HostThread == right.HostThread && left.Actor == right.Actor
}

func sameTurn(left, right harness.Turn) bool {
	return left.ID == right.ID && left.SessionID == right.SessionID && left.HostTurn == right.HostTurn && left.ConfigSnapshotID == right.ConfigSnapshotID
}

func sameItem(left, right harness.Item) bool {
	return left.ID == right.ID && left.TurnID == right.TurnID && left.Kind == right.Kind && left.Status == right.Status &&
		left.RunID == right.RunID && left.InvocationID == right.InvocationID && left.ParentItemID == right.ParentItemID && string(left.Payload) == string(right.Payload) &&
		sameItemHostRef(left.HostRef, right.HostRef)
}

func sameItemHostRef(left, right *harness.HostRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func mapError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return harness.ErrNotFound
	}
	return err
}

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

func (store *Store) ResolveInteraction(
	ctx context.Context,
	value harness.Interaction,
	expectedRevision uint64,
) (harness.InteractionResolution, error) {
	record, err := interactionToRecord(value)
	if err != nil || expectedRevision == 0 || value.Status != harness.InteractionResolved ||
		len(value.Response) == 0 || !json.Valid(value.Response) {
		if err == nil {
			err = harness.ErrInvalidRequest
		}
		return harness.InteractionResolution{}, err
	}
	var resolution harness.InteractionResolution
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		resolution, err = resolveInteractionTransaction(tx, value, record, expectedRevision)
		return err
	})
	return resolution, err
}

func resolveInteractionTransaction(
	tx *gorm.DB,
	value harness.Interaction,
	record interactionRecord,
	expectedRevision uint64,
) (harness.InteractionResolution, error) {
	turnRow, invocationRow, currentRow, err := lockInteractionResolutionState(
		tx, value, expectedRevision,
	)
	if err != nil {
		return harness.InteractionResolution{}, err
	}
	current, err := interactionFromRecord(currentRow)
	if err != nil || current.Status != harness.InteractionWaiting || !sameInteraction(current, value) {
		return harness.InteractionResolution{}, errors.Join(harness.ErrConflict, err)
	}
	if err = persistInteractionResolution(tx, record, currentRow); err != nil {
		return harness.InteractionResolution{}, err
	}
	value.Revision = expectedRevision + 1
	invocation, err := invocationFromRecord(invocationRow)
	if err != nil {
		return harness.InteractionResolution{}, err
	}
	turn := turnFromRecord(turnRow)
	return harness.InteractionResolution{Interaction: value, Invocation: invocation, Turn: turn}, nil
}

func lockInteractionResolutionState(
	tx *gorm.DB,
	value harness.Interaction,
	expectedRevision uint64,
) (turnRecord, invocationRecord, interactionRecord, error) {
	var turn turnRecord
	err := tx.Clauses(clause.Locking{Strength: rowLockStrengthUpdate}).
		Where("id = ? AND status = ?", value.TurnID, string(harness.TurnWaitingInput)).Take(&turn).Error
	if err = interactionOwnerLockError(err); err != nil {
		return turnRecord{}, invocationRecord{}, interactionRecord{}, err
	}
	var invocation invocationRecord
	err = tx.Clauses(clause.Locking{Strength: rowLockStrengthUpdate}).
		Where("id = ? AND turn_id = ? AND status = ?", value.InvocationID, value.TurnID, string(harness.InvocationWaitingInput)).
		Take(&invocation).Error
	if err = interactionOwnerLockError(err); err != nil {
		return turnRecord{}, invocationRecord{}, interactionRecord{}, err
	}
	var interaction interactionRecord
	err = tx.Clauses(clause.Locking{Strength: rowLockStrengthUpdate}).
		Where("id = ? AND revision = ? AND turn_id = ? AND invocation_id = ? AND status = ?",
			value.ID, expectedRevision, value.TurnID, value.InvocationID, string(harness.InteractionWaiting)).
		Take(&interaction).Error
	if err = interactionOwnerLockError(err); err != nil {
		return turnRecord{}, invocationRecord{}, interactionRecord{}, err
	}
	return turn, invocation, interaction, nil
}

func persistInteractionResolution(
	tx *gorm.DB,
	resolved interactionRecord,
	current interactionRecord,
) error {
	interactionUpdate := tx.Model(&interactionRecord{}).
		Where("id = ? AND revision = ? AND status = ?", current.ID, current.Revision, string(harness.InteractionWaiting)).
		Updates(map[string]any{
			"status": resolved.Status, "response_json": resolved.ResponseJSON,
			"revision": current.Revision + 1, "updated_at": resolved.UpdatedAt,
		})
	return singleInteractionResolutionUpdate(interactionUpdate)
}

func singleInteractionResolutionUpdate(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return harness.ErrConflict
	}
	return nil
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
		reflect.DeepEqual(left.ApplicationRef, right.ApplicationRef) && reflect.DeepEqual(left.ArtifactRefs, right.ArtifactRefs) &&
		string(left.Schema) == string(right.Schema) && string(left.Presentation) == string(right.Presentation)
}

func interactionToRecord(value harness.Interaction) (interactionRecord, error) {
	if value.ID == "" || value.TurnID == "" || value.InvocationID == "" || value.Key == "" ||
		len(value.Schema) == 0 || !json.Valid(value.Schema) || value.Revision == 0 {
		return interactionRecord{}, harness.ErrInvalidRequest
	}
	applicationRefJSON := ""
	if value.ApplicationRef != nil {
		raw, err := json.Marshal(value.ApplicationRef)
		if err != nil {
			return interactionRecord{}, err
		}
		applicationRefJSON = string(raw)
	}
	artifactRefsJSON, err := json.Marshal(value.ArtifactRefs)
	if err != nil {
		return interactionRecord{}, err
	}
	return interactionRecord{
		ID: value.ID, TurnID: value.TurnID, InvocationID: value.InvocationID, ParentItemID: value.ParentItemID,
		ApplicationRefJSON: applicationRefJSON, ArtifactRefsJSON: string(artifactRefsJSON),
		Key: value.Key, Kind: string(value.Kind), SchemaJSON: string(value.Schema),
		PresentationJSON: string(value.Presentation), Status: string(value.Status), ResponseJSON: string(value.Response),
		Revision: value.Revision, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}, nil
}

func interactionFromRecord(value interactionRecord) (harness.Interaction, error) {
	if !json.Valid([]byte(value.SchemaJSON)) || value.PresentationJSON != "" && !json.Valid([]byte(value.PresentationJSON)) ||
		value.ResponseJSON != "" && !json.Valid([]byte(value.ResponseJSON)) ||
		value.ApplicationRefJSON != "" && !json.Valid([]byte(value.ApplicationRefJSON)) || !json.Valid([]byte(value.ArtifactRefsJSON)) {
		return harness.Interaction{}, harness.ErrConflict
	}
	var applicationRef *harness.HostRef
	if value.ApplicationRefJSON != "" {
		var ref harness.HostRef
		if err := json.Unmarshal([]byte(value.ApplicationRefJSON), &ref); err != nil {
			return harness.Interaction{}, harness.ErrConflict
		}
		applicationRef = &ref
	}
	artifactRefs := []harness.HostRef{}
	if err := json.Unmarshal([]byte(value.ArtifactRefsJSON), &artifactRefs); err != nil {
		return harness.Interaction{}, harness.ErrConflict
	}
	return harness.Interaction{
		ID: value.ID, TurnID: value.TurnID, InvocationID: value.InvocationID, ParentItemID: value.ParentItemID,
		ApplicationRef: applicationRef, ArtifactRefs: artifactRefs,
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
	return []any{
		&sessionRecord{}, &turnRecord{}, &invocationRecord{}, &interactionRecord{}, &configRecord{},
		&contextCheckpointRecord{}, &contextHeadRecord{}, &contextArtifactRecord{}, &itemRecord{},
	}
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
			"context_checkpoint_id": record.ContextCheckpointID, "context_ref_id": record.ContextRefID,
			"context_generation": record.ContextGeneration, "context_revision": record.ContextRevision,
			"context_lineage_hash":              record.ContextLineageHash,
			"context_covered_through_source_id": record.ContextCoveredThroughSourceID,
			"context_content_hash":              record.ContextContentHash,
			"status":                            record.Status, "revision": record.Revision, "error_code": record.ErrorCode,
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

func (store *Store) PutContextCheckpoint(
	ctx context.Context,
	value runtimecontext.Checkpoint,
) (runtimecontext.Checkpoint, bool, error) {
	if !runtimecontext.ValidCheckpoint(value) {
		return runtimecontext.Checkpoint{}, false, harness.ErrInvalidRequest
	}
	created := false
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var putErr error
		created, putErr = putContextCheckpointTx(tx, value)
		return putErr
	})
	if err != nil {
		return runtimecontext.Checkpoint{}, false, err
	}
	return runtimecontext.CloneCheckpoint(value), created, nil
}

func putContextCheckpointTx(tx *gorm.DB, value runtimecontext.Checkpoint) (bool, error) {
	if tx == nil || !runtimecontext.ValidCheckpoint(value) {
		return false, harness.ErrInvalidRequest
	}
	if err := validateContextCheckpointReferencesTx(tx, value); err != nil {
		return false, err
	}
	record, err := contextCheckpointToRecord(value)
	if err != nil {
		return false, err
	}
	result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 1 {
		return true, nil
	}
	return replayContextCheckpointTx(tx, value)
}

func validateContextCheckpointReferencesTx(tx *gorm.DB, value runtimecontext.Checkpoint) error {
	if value.ParentCheckpointID != "" {
		var parent contextCheckpointRecord
		if err := tx.Where("id = ? AND scope_id = ?", value.ParentCheckpointID, value.ScopeID).Take(&parent).Error; err != nil {
			return harness.ErrConflict
		}
	}
	for _, artifactID := range value.ArtifactIDs {
		var artifact contextArtifactRecord
		if err := tx.Where("id = ? AND scope_id = ?", artifactID, value.ScopeID).Take(&artifact).Error; err != nil {
			return harness.ErrConflict
		}
	}
	return nil
}

func contextCheckpointToRecord(value runtimecontext.Checkpoint) (contextCheckpointRecord, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return contextCheckpointRecord{}, err
	}
	return contextCheckpointRecord{
		ID: value.ID, ScopeID: value.ScopeID, Generation: value.Generation, Revision: value.Revision,
		StaticFingerprint: value.StaticFingerprint, CoveredThroughSourceID: value.CoveredThroughSourceID,
		CoveredPathHash: value.CoveredPathHash, SourceAligned: runtimecontext.SourceAlignedCheckpoint(value),
		PayloadJSON: string(payload),
	}, nil
}

func replayContextCheckpointTx(tx *gorm.DB, value runtimecontext.Checkpoint) (bool, error) {
	var existingRecord contextCheckpointRecord
	if loadErr := tx.Where("id = ?", value.ID).Take(&existingRecord).Error; loadErr != nil {
		return false, harness.ErrConflict
	}
	existing, loadErr := contextCheckpointFromRecord(existingRecord)
	if loadErr != nil || !reflect.DeepEqual(existing, value) {
		return false, harness.ErrConflict
	}
	return false, nil
}

func (store *Store) GetContextCheckpoint(ctx context.Context, id string) (runtimecontext.Checkpoint, error) {
	var record contextCheckpointRecord
	if err := store.db.WithContext(ctx).Where("id = ?", strings.TrimSpace(id)).Take(&record).Error; err != nil {
		return runtimecontext.Checkpoint{}, mapError(err)
	}
	return contextCheckpointFromRecord(record)
}

func (store *Store) GetActiveContextCheckpoint(ctx context.Context, scopeID string) (runtimecontext.Checkpoint, error) {
	scopeID = strings.TrimSpace(scopeID)
	if scopeID == "" {
		return runtimecontext.Checkpoint{}, harness.ErrInvalidRequest
	}
	var head contextHeadRecord
	if err := store.db.WithContext(ctx).Where("scope_id = ?", scopeID).Take(&head).Error; err != nil {
		return runtimecontext.Checkpoint{}, mapError(err)
	}
	checkpoint, err := store.GetContextCheckpoint(ctx, head.CheckpointID)
	if err != nil || checkpoint.ScopeID != scopeID {
		return runtimecontext.Checkpoint{}, harness.ErrConflict
	}
	return checkpoint, nil
}

func (store *Store) FindContextCheckpointForPath(
	ctx context.Context,
	query harness.ContextCheckpointPathQuery,
) (runtimecontext.Checkpoint, error) {
	query, pathIndex, records, err := store.loadContextCheckpointPathCandidates(ctx, query)
	if err != nil {
		return runtimecontext.Checkpoint{}, err
	}
	bestIndex := -1
	best := contextCheckpointRecord{}
	for _, record := range records {
		index, ok := reusablePostgresContextCheckpointCandidate(pathIndex, record, best, bestIndex)
		if !ok {
			continue
		}
		bestIndex = index
		best = record
	}
	if bestIndex < 0 {
		return runtimecontext.Checkpoint{}, harness.ErrNotFound
	}
	candidate, err := store.GetContextCheckpoint(ctx, best.ID)
	if err != nil {
		return runtimecontext.Checkpoint{}, err
	}
	if runtimecontext.CheckpointSourceIndex(candidate, query.SourcePath) != bestIndex {
		return runtimecontext.Checkpoint{}, harness.ErrConflict
	}
	return runtimecontext.CloneCheckpoint(candidate), nil
}

func (store *Store) loadContextCheckpointPathCandidates(
	ctx context.Context,
	query harness.ContextCheckpointPathQuery,
) (harness.ContextCheckpointPathQuery, contextCheckpointPathIndex, []contextCheckpointRecord, error) {
	var err error
	query, err = harness.NormalizeContextCheckpointPathQuery(query)
	if err != nil || store == nil {
		return harness.ContextCheckpointPathQuery{}, contextCheckpointPathIndex{}, nil, harness.ErrInvalidRequest
	}
	pathIndex := newContextCheckpointPathIndex(query.SourcePath)
	var records []contextCheckpointRecord
	if err := store.db.WithContext(ctx).
		Select(
			"id", "scope_id", "generation", "revision", "static_fingerprint",
			"covered_through_source_id", "covered_path_hash", "source_aligned",
		).
		Where("scope_id = ? AND static_fingerprint = ? AND source_aligned = ?", query.ScopeID, query.StaticFingerprint, true).
		Find(&records).Error; err != nil {
		return harness.ContextCheckpointPathQuery{}, contextCheckpointPathIndex{}, nil, mapError(err)
	}
	return query, pathIndex, records, nil
}

type contextCheckpointPathIndex struct {
	positions map[string]int
	hashes    []string
}

func newContextCheckpointPathIndex(sourcePath []string) contextCheckpointPathIndex {
	positions := make(map[string]int, len(sourcePath))
	hashes := make([]string, len(sourcePath))
	current := ""
	for index, sourceID := range sourcePath {
		current = runtimecontext.ExtendLineageHash(current, sourceID)
		positions[sourceID] = index
		hashes[index] = current
	}
	return contextCheckpointPathIndex{positions: positions, hashes: hashes}
}

func reusablePostgresContextCheckpointCandidate(
	pathIndex contextCheckpointPathIndex,
	candidate contextCheckpointRecord,
	best contextCheckpointRecord,
	bestIndex int,
) (int, bool) {
	index, ok := pathIndex.positions[strings.TrimSpace(candidate.CoveredThroughSourceID)]
	if !ok || index < 0 || index >= len(pathIndex.hashes) || candidate.CoveredPathHash != pathIndex.hashes[index] || index < bestIndex {
		return -1, false
	}
	if index == bestIndex && !newerPostgresContextCheckpoint(candidate, best) {
		return -1, false
	}
	return index, true
}

func newerPostgresContextCheckpoint(left, right contextCheckpointRecord) bool {
	if right.ID == "" || left.Generation != right.Generation {
		return right.ID == "" || left.Generation > right.Generation
	}
	if left.Revision != right.Revision {
		return left.Revision > right.Revision
	}
	return left.ID > right.ID
}

func (store *Store) CommitContextCheckpoint(
	ctx context.Context,
	request harness.ContextCheckpointCommit,
) (harness.Turn, error) {
	request.TurnID = strings.TrimSpace(request.TurnID)
	request.ExpectedTurnCheckpointID = strings.TrimSpace(request.ExpectedTurnCheckpointID)
	request.ExpectedHeadCheckpointID = strings.TrimSpace(request.ExpectedHeadCheckpointID)
	checkpoint := runtimecontext.CloneCheckpoint(request.Checkpoint)
	if request.TurnID == "" || request.ExpectedTurnRevision == 0 || request.UpdatedAt.IsZero() ||
		!runtimecontext.ValidCheckpoint(checkpoint) {
		return harness.Turn{}, harness.ErrInvalidRequest
	}
	var updated harness.Turn
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		turn, head, headExists, err := loadContextCommitStateTx(tx, request, checkpoint)
		if err != nil {
			return err
		}
		if _, err := putContextCheckpointTx(tx, checkpoint); err != nil {
			return err
		}
		if err = commitContextHeadTx(tx, request, checkpoint, head, headExists); err != nil {
			return err
		}
		updated, err = commitContextTurnTx(tx, request, checkpoint, turn)
		return err
	})
	if err != nil {
		return harness.Turn{}, err
	}
	return updated, nil
}

func loadContextCommitStateTx(
	tx *gorm.DB,
	request harness.ContextCheckpointCommit,
	checkpoint runtimecontext.Checkpoint,
) (harness.Turn, contextHeadRecord, bool, error) {
	var turnRow turnRecord
	if err := tx.Clauses(clause.Locking{Strength: rowLockStrengthUpdate}).
		Where("id = ?", request.TurnID).Take(&turnRow).Error; err != nil {
		return harness.Turn{}, contextHeadRecord{}, false, mapError(err)
	}
	turn := turnFromRecord(turnRow)
	if turn.Revision != request.ExpectedTurnRevision || turn.SessionID != checkpoint.ScopeID ||
		strings.TrimSpace(turn.ContextCheckpointID) != request.ExpectedTurnCheckpointID {
		return harness.Turn{}, contextHeadRecord{}, false, harness.ErrConflict
	}
	head, exists, err := loadContextHeadForCommitTx(tx, checkpoint.ScopeID, request.ExpectedHeadCheckpointID)
	return turn, head, exists, err
}

func loadContextHeadForCommitTx(tx *gorm.DB, scopeID string, expectedCheckpointID string) (contextHeadRecord, bool, error) {
	var head contextHeadRecord
	err := tx.Clauses(clause.Locking{Strength: rowLockStrengthUpdate}).Where("scope_id = ?", scopeID).Take(&head).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if expectedCheckpointID != "" {
			return contextHeadRecord{}, false, harness.ErrConflict
		}
		return contextHeadRecord{}, false, nil
	}
	if err != nil {
		return contextHeadRecord{}, false, err
	}
	return head, true, nil
}

func commitContextHeadTx(
	tx *gorm.DB,
	request harness.ContextCheckpointCommit,
	checkpoint runtimecontext.Checkpoint,
	head contextHeadRecord,
	exists bool,
) error {
	if !exists {
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&contextHeadRecord{
			ScopeID: checkpoint.ScopeID, CheckpointID: checkpoint.ID, Revision: 1, UpdatedAt: request.UpdatedAt,
		})
		return result.Error
	}
	if head.CheckpointID != request.ExpectedHeadCheckpointID {
		// Another top-level owner advanced the shared head after this Turn built its
		// Context. Keep this Turn's checkpoint lineage durable but detached; future
		// branch-path lookup can still reuse it without interrupting either owner.
		return nil
	}
	if head.CheckpointID == checkpoint.ID {
		return nil
	}
	result := tx.Model(&contextHeadRecord{}).
		Where("scope_id = ? AND checkpoint_id = ? AND revision = ?", head.ScopeID, request.ExpectedHeadCheckpointID, head.Revision).
		Updates(map[string]any{
			"checkpoint_id": checkpoint.ID, "revision": head.Revision + 1, "updated_at": request.UpdatedAt,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return harness.ErrConflict
	}
	return nil
}

func commitContextTurnTx(
	tx *gorm.DB,
	request harness.ContextCheckpointCommit,
	checkpoint runtimecontext.Checkpoint,
	turn harness.Turn,
) (harness.Turn, error) {
	turn.ContextCheckpointID = checkpoint.ID
	turn.ContextRef = harness.ContextCheckpointRef{
		ID: checkpoint.ID, Generation: checkpoint.Generation, Revision: checkpoint.Revision,
		LineageHash: checkpoint.LineageHash, CoveredThroughSourceID: checkpoint.CoveredThroughSourceID,
		ContentHash: checkpoint.ContentHash,
	}
	turn.UpdatedAt = request.UpdatedAt
	record := turnToRecord(turn)
	record.Revision = request.ExpectedTurnRevision + 1
	result := tx.Model(&turnRecord{}).
		Where("id = ? AND revision = ? AND context_checkpoint_id = ?", turn.ID, request.ExpectedTurnRevision, request.ExpectedTurnCheckpointID).
		Updates(map[string]any{
			"context_checkpoint_id": record.ContextCheckpointID, "context_ref_id": record.ContextRefID,
			"context_generation": record.ContextGeneration, "context_revision": record.ContextRevision,
			"context_lineage_hash":              record.ContextLineageHash,
			"context_covered_through_source_id": record.ContextCoveredThroughSourceID,
			"context_content_hash":              record.ContextContentHash,
			"revision":                          record.Revision, "updated_at": record.UpdatedAt,
		})
	if result.Error != nil {
		return harness.Turn{}, result.Error
	}
	if result.RowsAffected != 1 {
		return harness.Turn{}, harness.ErrConflict
	}
	turn.Revision = record.Revision
	return turn, nil
}

func contextCheckpointFromRecord(record contextCheckpointRecord) (runtimecontext.Checkpoint, error) {
	var value runtimecontext.Checkpoint
	if err := json.Unmarshal([]byte(record.PayloadJSON), &value); err != nil ||
		value.ID != record.ID || value.ScopeID != record.ScopeID || value.Generation != record.Generation ||
		value.Revision != record.Revision || value.StaticFingerprint != record.StaticFingerprint ||
		value.CoveredThroughSourceID != record.CoveredThroughSourceID || value.CoveredPathHash != record.CoveredPathHash ||
		runtimecontext.SourceAlignedCheckpoint(value) != record.SourceAligned || !runtimecontext.ValidCheckpoint(value) {
		return runtimecontext.Checkpoint{}, harness.ErrConflict
	}
	return runtimecontext.CloneCheckpoint(value), nil
}

func (store *Store) PutContextArtifact(
	ctx context.Context,
	value runtimecontext.Artifact,
) (runtimecontext.Artifact, bool, error) {
	if !runtimecontext.ValidArtifact(value) {
		return runtimecontext.Artifact{}, false, harness.ErrInvalidRequest
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return runtimecontext.Artifact{}, false, err
	}
	record := contextArtifactRecord{
		ID: value.ID, ScopeID: value.ScopeID, Generation: value.Generation, PayloadJSON: string(payload),
	}
	result := store.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	if result.Error != nil {
		return runtimecontext.Artifact{}, false, result.Error
	}
	if result.RowsAffected == 1 {
		return runtimecontext.CloneArtifacts([]runtimecontext.Artifact{value})[0], true, nil
	}
	existing, loadErr := store.GetContextArtifact(ctx, value.ID)
	if loadErr != nil || !reflect.DeepEqual(existing, value) {
		return runtimecontext.Artifact{}, false, harness.ErrConflict
	}
	return existing, false, nil
}

func (store *Store) GetContextArtifact(ctx context.Context, id string) (runtimecontext.Artifact, error) {
	var record contextArtifactRecord
	if err := store.db.WithContext(ctx).Where("id = ?", strings.TrimSpace(id)).Take(&record).Error; err != nil {
		return runtimecontext.Artifact{}, mapError(err)
	}
	var value runtimecontext.Artifact
	if err := json.Unmarshal([]byte(record.PayloadJSON), &value); err != nil ||
		value.ID != record.ID || value.ScopeID != record.ScopeID || value.Generation != record.Generation ||
		!runtimecontext.ValidArtifact(value) {
		return runtimecontext.Artifact{}, harness.ErrConflict
	}
	return runtimecontext.CloneArtifacts([]runtimecontext.Artifact{value})[0], nil
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
		ConfigSnapshotID: value.ConfigSnapshotID, ContextCheckpointID: value.ContextCheckpointID,
		ContextRefID: value.ContextRef.ID, ContextGeneration: value.ContextRef.Generation, ContextRevision: value.ContextRef.Revision,
		ContextLineageHash: value.ContextRef.LineageHash, ContextCoveredThroughSourceID: value.ContextRef.CoveredThroughSourceID,
		ContextContentHash: value.ContextRef.ContentHash,
		Status:             string(value.Status), Revision: value.Revision, ErrorCode: value.ErrorCode, ErrorDetail: value.ErrorDetail,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func turnFromRecord(value turnRecord) harness.Turn {
	return harness.Turn{
		ID: value.ID, SessionID: value.SessionID, HostTurn: harness.HostRef{Kind: value.HostTurnKind, ID: value.HostTurnID},
		ConfigSnapshotID: value.ConfigSnapshotID, ContextCheckpointID: value.ContextCheckpointID,
		ContextRef: harness.ContextCheckpointRef{
			ID: value.ContextRefID, Generation: value.ContextGeneration, Revision: value.ContextRevision,
			LineageHash: value.ContextLineageHash, CoveredThroughSourceID: value.ContextCoveredThroughSourceID,
			ContentHash: value.ContextContentHash,
		},
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

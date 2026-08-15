package harnesspostgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	harness "github.com/orz-i/Gaoge/sdk/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrNilDatabase = errors.New("harness postgres database is nil")

// Store persists Harness Session/Turn/Config/Item state in PostgreSQL-compatible GORM databases.
type Store struct{ db *gorm.DB }

// New creates a durable Harness Store. Schema migration remains an explicit host operation.
func New(db *gorm.DB) (*Store, error) {
	if db == nil {
		return nil, ErrNilDatabase
	}
	return &Store{db: db}, nil
}

func (store *Store) GetTurnByRootRunID(ctx context.Context, rootRunID string) (harness.Turn, error) {
	rootRunID = strings.TrimSpace(rootRunID)
	if store == nil || store.db == nil || rootRunID == "" {
		return harness.Turn{}, harness.ErrInvalidRequest
	}
	var record turnRecord
	if err := store.db.WithContext(ctx).Where("root_run_id = ?", rootRunID).Take(&record).Error; err != nil {
		return harness.Turn{}, mapError(err)
	}
	return turnFromRecord(record), nil
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
	return []any{&sessionRecord{}, &turnRecord{}, &configRecord{}, &itemRecord{}}
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
			"root_run_id": record.RootRunID, "context_snapshot_id": record.ContextSnapshotID,
			"status": record.Status, "revision": record.Revision, "error_code": record.ErrorCode,
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
		RootRunID: value.RootRunID, ConfigSnapshotID: value.ConfigSnapshotID, ContextSnapshotID: value.ContextSnapshotID,
		Status: string(value.Status), Revision: value.Revision, ErrorCode: value.ErrorCode, ErrorDetail: value.ErrorDetail,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func turnFromRecord(value turnRecord) harness.Turn {
	return harness.Turn{
		ID: value.ID, SessionID: value.SessionID, HostTurn: harness.HostRef{Kind: value.HostTurnKind, ID: value.HostTurnID},
		RootRunID: value.RootRunID, ConfigSnapshotID: value.ConfigSnapshotID, ContextSnapshotID: value.ContextSnapshotID,
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
		HostRefKind: hostKind, HostRefID: hostID, RunID: value.RunID, ParentItemID: value.ParentItemID,
		PayloadJSON: payload, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}, nil
}

func itemFromRecord(value itemRecord) (harness.Item, error) {
	result := harness.Item{
		ID: value.ID, TurnID: value.TurnID, Seq: value.Seq, Kind: harness.ItemKind(value.Kind), Status: harness.ItemStatus(value.Status),
		RunID: value.RunID, ParentItemID: value.ParentItemID, Payload: json.RawMessage(value.PayloadJSON),
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
		left.RunID == right.RunID && left.ParentItemID == right.ParentItemID && string(left.Payload) == string(right.Payload) &&
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

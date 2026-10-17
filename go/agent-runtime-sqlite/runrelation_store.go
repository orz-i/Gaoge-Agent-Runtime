package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// RunRelationStore persists immutable parent/child ownership in the same
// private SQLite Profile as the Kernel. It does not provide automatic
// child-terminal reconciliation or implement ReconciliationPager.
type RunRelationStore struct {
	db *sql.DB
}

var _ runrelation.Store = (*RunRelationStore)(nil)

// NewRunRelationStore uses an existing SQLite Store and its DB connection.
// The caller owns the Store's lifecycle; this adapter has no separate Close.
func NewRunRelationStore(store *Store) *RunRelationStore {
	if store == nil {
		return nil
	}
	return &RunRelationStore{db: store.db}
}

// Put atomically creates or reuses a relation. SQLite uniqueness constraints
// are the authority across OS processes; the same short IMMEDIATE transaction
// also reads the persisted identity after a uniqueness collision.
func (s *RunRelationStore) Put(
	ctx context.Context, relation runrelation.Relation,
) (runrelation.Relation, bool, error) {
	if s == nil || s.db == nil || ctx == nil {
		return runrelation.Relation{}, false, runrelation.ErrInvalidInput
	}
	relation, err := runrelation.Prepare(relation)
	if err != nil {
		return runrelation.Relation{}, false, err
	}
	createdNS, err := storedNanoseconds(relation.CreatedAt)
	if err != nil {
		return runrelation.Relation{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return runrelation.Relation{}, false, conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx,
		`INSERT INTO runtime_run_relations(child_run_id,parent_run_id,kind,owner_node_id,created_ns)
		 VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`,
		relation.ChildRunID, relation.ParentRunID, string(relation.Kind), relation.OwnerNodeID, createdNS)
	if err != nil {
		return runrelation.Relation{}, false, conflictOrDatabaseError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return runrelation.Relation{}, false, err
	}
	switch affected {
	case 1:
		if err = tx.Commit(); err != nil {
			return runrelation.Relation{}, false, conflictOrDatabaseError(err)
		}
		return relation, false, nil
	case 0:
		// Both identities have UNIQUE constraints. Any existing conflicting
		// owner or child is visible within this same writer transaction.
		existing, loadErr := scanRunRelation(tx.QueryRowContext(ctx,
			`SELECT parent_run_id,child_run_id,kind,owner_node_id,created_ns
			 FROM runtime_run_relations
			 WHERE child_run_id=? OR (parent_run_id=? AND kind=? AND owner_node_id=?)
			 LIMIT 1`,
			relation.ChildRunID, relation.ParentRunID, string(relation.Kind), relation.OwnerNodeID))
		if loadErr != nil {
			return runrelation.Relation{}, false, conflictOrDatabaseError(loadErr)
		}
		if !runrelation.EqualIdentity(existing, relation) {
			return runrelation.Relation{}, false, runrelation.ErrConflict
		}
		return existing, true, nil
	default:
		return runrelation.Relation{}, false, fmt.Errorf("%w: unexpected inserted row count %d", ErrInvalidRecord, affected)
	}
}

// GetByChild reads the sole immutable owner for a Child Run.
func (s *RunRelationStore) GetByChild(ctx context.Context, childRunID string) (runrelation.Relation, error) {
	childRunID = strings.TrimSpace(childRunID)
	if s == nil || s.db == nil || ctx == nil || childRunID == "" {
		return runrelation.Relation{}, runrelation.ErrInvalidInput
	}
	return scanRunRelation(s.db.QueryRowContext(ctx,
		`SELECT parent_run_id,child_run_id,kind,owner_node_id,created_ns
		 FROM runtime_run_relations WHERE child_run_id=?`, childRunID))
}

// ListChildren returns all direct children in the shared deterministic order.
func (s *RunRelationStore) ListChildren(ctx context.Context, parentRunID string) ([]runrelation.Relation, error) {
	parentRunID = strings.TrimSpace(parentRunID)
	if s == nil || s.db == nil || ctx == nil || parentRunID == "" {
		return nil, runrelation.ErrInvalidInput
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT parent_run_id,child_run_id,kind,owner_node_id,created_ns
		 FROM runtime_run_relations WHERE parent_run_id=?
		 ORDER BY created_ns,kind,owner_node_id,child_run_id`, parentRunID)
	if err != nil {
		return nil, conflictOrDatabaseError(err)
	}
	return scanRunRelations(rows)
}

// ListAll reads all immutable relations in the shared deterministic order.
func (s *RunRelationStore) ListAll(ctx context.Context) ([]runrelation.Relation, error) {
	if s == nil || s.db == nil || ctx == nil {
		return nil, runrelation.ErrInvalidInput
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT parent_run_id,child_run_id,kind,owner_node_id,created_ns
		 FROM runtime_run_relations ORDER BY created_ns,kind,owner_node_id,child_run_id`)
	if err != nil {
		return nil, conflictOrDatabaseError(err)
	}
	return scanRunRelations(rows)
}

type runRelationScanner interface {
	Scan(...any) error
}

func scanRunRelation(scanner runRelationScanner) (runrelation.Relation, error) {
	var relation runrelation.Relation
	var kind string
	var createdNS int64
	err := scanner.Scan(&relation.ParentRunID, &relation.ChildRunID, &kind, &relation.OwnerNodeID, &createdNS)
	if errors.Is(err, sql.ErrNoRows) {
		return runrelation.Relation{}, runrelation.ErrNotFound
	}
	if err != nil {
		return runrelation.Relation{}, conflictOrDatabaseError(err)
	}
	relation.Kind = runrelation.Kind(kind)
	relation.CreatedAt = time.Unix(0, createdNS).UTC()
	normalized, err := runrelation.Prepare(relation)
	if err != nil || normalized.ParentRunID != relation.ParentRunID ||
		normalized.ChildRunID != relation.ChildRunID || normalized.Kind != relation.Kind ||
		normalized.OwnerNodeID != relation.OwnerNodeID {
		return runrelation.Relation{}, fmt.Errorf("%w: invalid persisted RunRelation", ErrInvalidRecord)
	}
	return normalized, nil
}

func scanRunRelations(rows *sql.Rows) ([]runrelation.Relation, error) {
	defer func() { _ = rows.Close() }()
	relations := make([]runrelation.Relation, 0)
	for rows.Next() {
		relation, err := scanRunRelation(rows)
		if err != nil {
			return nil, err
		}
		relations = append(relations, relation)
	}
	if err := rows.Err(); err != nil {
		return nil, conflictOrDatabaseError(err)
	}
	return relations, nil
}

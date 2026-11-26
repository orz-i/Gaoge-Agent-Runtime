package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// Delivery bookkeeping is deliberately separate from immutable ownership
// and native execution/callback evidence. Zero means no acknowledged handoff.
const relationProgressDDL = `CREATE TABLE runtime_run_relation_progress (
		child_run_id TEXT PRIMARY KEY REFERENCES runtime_run_relations(child_run_id),
		last_parent_revision INTEGER NOT NULL CHECK(last_parent_revision > 0),
		last_child_revision INTEGER NOT NULL CHECK(last_child_revision > 0)
	)`

// Materialize only the indexed child-ID page before joining Run/progress
// rows. Missing Runs are allowed by the relation contract. Returning every
// scanned key makes even an entirely ineligible page advance.
const relationPageSQL = `WITH scanned AS MATERIALIZED (
	SELECT child_run_id,parent_run_id,kind,owner_node_id,created_ns
	FROM runtime_run_relations WHERE child_run_id > ?
	ORDER BY child_run_id LIMIT ?
)
SELECT s.child_run_id,s.parent_run_id,s.kind,s.owner_node_id,s.created_ns,
	COALESCE(p.last_parent_revision,0),COALESCE(p.last_child_revision,0),
	COALESCE(parent.status,''),COALESCE(child.status,''),COALESCE(parent.revision,0),COALESCE(child.revision,0)
FROM scanned s
LEFT JOIN runtime_runs child ON child.run_id=s.child_run_id
LEFT JOIN runtime_runs parent ON parent.run_id=s.parent_run_id
LEFT JOIN runtime_run_relation_progress p ON p.child_run_id=s.child_run_id
ORDER BY s.child_run_id`

func (s *RunRelationStore) ListReconcilePage(ctx context.Context, after string, limit int) (runrelation.ReconciliationPage, error) {
	if s == nil || s.db == nil || ctx == nil || limit < 1 || limit > runrelation.MaxReconcilePageSize {
		return runrelation.ReconciliationPage{}, runrelation.ErrInvalidInput
	}
	rows, err := s.db.QueryContext(ctx, relationPageSQL, after, limit)
	if err != nil {
		return runrelation.ReconciliationPage{}, conflictOrDatabaseError(err)
	}
	defer func() { _ = rows.Close() }()
	scan := make([]runrelation.ReconciliationScanRow, 0, limit)
	for rows.Next() {
		var row runrelation.ReconciliationScanRow
		var createdNS int64
		r := &row.Relation
		if err = rows.Scan(&r.ChildRunID, &r.ParentRunID, &r.Kind, &r.OwnerNodeID, &createdNS, &row.LastParentRevision, &row.LastChildRevision,
			&row.ParentStatus, &row.ChildStatus, &row.ParentRevision, &row.ChildRevision); err != nil {
			return runrelation.ReconciliationPage{}, conflictOrDatabaseError(err)
		}
		r.CreatedAt = time.Unix(0, createdNS).UTC()
		scan = append(scan, row)
	}
	if err = rows.Err(); err != nil {
		return runrelation.ReconciliationPage{}, conflictOrDatabaseError(err)
	}
	return runrelation.ReconciliationPageFromScan(after, limit, scan)
}

func (s *RunRelationStore) MarkObserved(ctx context.Context, child string, parentRevision, childRevision uint64) error {
	return s.MarkEnqueued(ctx, child, parentRevision, childRevision)
}

// MarkEnqueued records only original queue acceptance or feature-verified
// consumption. It cannot alter Runs, grants, relations, or physical effects.
func (s *RunRelationStore) MarkEnqueued(ctx context.Context, child string, parentRevision, childRevision uint64) error {
	child, err := runrelation.PrepareReconciliationProgress(child, parentRevision, childRevision)
	if s == nil || s.db == nil || ctx == nil || err != nil {
		return runrelation.ErrInvalidInput
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO runtime_run_relation_progress(child_run_id,last_parent_revision,last_child_revision)
		SELECT child_run_id,?,? FROM runtime_run_relations WHERE child_run_id=?
		ON CONFLICT(child_run_id) DO UPDATE SET
		last_parent_revision=max(last_parent_revision,excluded.last_parent_revision),
		last_child_revision=max(last_child_revision,excluded.last_child_revision)`, parentRevision, childRevision, child)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return runrelation.ErrNotFound
	}
	if n != 1 {
		return fmt.Errorf("%w: unexpected progress row count", ErrInvalidRecord)
	}
	return nil
}

package runrelation

import (
	"math"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// ReconciliationScanRow is one bounded, indexed relation and its joined Run
// facts from a single storage snapshot. Missing Runs have zero revisions.
// The relational Store owns the query, not the eligibility rule.
type ReconciliationScanRow struct {
	ReconciliationCandidate
	ParentStatus, ChildStatus     kernel.RunStatus
	ParentRevision, ChildRevision uint64
}

// ReconciliationPageFromScan advances over every scanned key before applying
// the shared child-terminal, live-parent and delivery-watermark predicate.
// Both SQL adapters materialize no more than limit relation rows.
func ReconciliationPageFromScan(after string, limit int, rows []ReconciliationScanRow) (ReconciliationPage, error) {
	if limit < 1 || limit > MaxReconcilePageSize || len(rows) > limit {
		return ReconciliationPage{}, ErrInvalidInput
	}
	page := ReconciliationPage{NextCursor: after, HasMore: len(rows) == limit, Candidates: make([]ReconciliationCandidate, 0)}
	// The database's indexed collation defines ordering for its own cursor.
	// Do not re-sort the page or impose Go's byte order on a PG locale index.
	seen := map[string]bool{after: true}
	for _, row := range rows {
		c := row.ReconciliationCandidate
		if c.Relation.ChildRunID == "" || seen[c.Relation.ChildRunID] {
			return ReconciliationPage{}, ErrConflict
		}
		seen[c.Relation.ChildRunID] = true
		page.NextCursor = c.Relation.ChildRunID
		if row.ParentRevision == 0 || row.ChildRevision == 0 || row.ParentStatus != kernel.RunStatusRunning ||
			(row.ChildStatus != kernel.RunStatusCompleted && row.ChildStatus != kernel.RunStatusFailed && row.ChildStatus != kernel.RunStatusCancelled) ||
			(c.LastParentRevision >= row.ParentRevision && c.LastChildRevision >= row.ChildRevision) {
			continue
		}
		normalized, err := Prepare(c.Relation)
		if err != nil || !EqualIdentity(c.Relation, normalized) {
			return ReconciliationPage{}, ErrConflict
		}
		c.Relation = normalized
		page.Candidates = append(page.Candidates, c)
	}
	return page, nil
}

// PrepareReconciliationProgress defines the revision domain common to the
// native SQL stores and Memory. Both relational schemas use signed BIGINTs.
func PrepareReconciliationProgress(child string, parentRevision, childRevision uint64) (string, error) {
	child = strings.TrimSpace(child)
	if child == "" || parentRevision == 0 || childRevision == 0 || parentRevision > math.MaxInt64 || childRevision > math.MaxInt64 {
		return "", ErrInvalidInput
	}
	return child, nil
}

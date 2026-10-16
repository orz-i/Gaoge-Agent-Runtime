package sqlite

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var _ kernel.ControlStore = (*Store)(nil)

// ApplyControl is the trusted, exact gate-CAS path. The Feature validates
// Tool approval/allowance authority before invoking it; the Store rechecks
// the persisted guard inside one SQLite write transaction before revoking
// any old execution owner and committing state, events, ReadyWork and proof.
func (s *Store) ApplyControl(ctx context.Context, runID string, expectedRevision uint64,
	guard kernel.ControlGuard, mutation kernel.StoreMutation) (kernel.Snapshot, bool, error) {
	revoked := false
	snapshot, err := s.applyWithExecution(ctx, nil, &guard, &revoked, runID, expectedRevision, mutation)
	if err != nil {
		return kernel.Snapshot{}, false, err
	}
	return snapshot, revoked, nil
}

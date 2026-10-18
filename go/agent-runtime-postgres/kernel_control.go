package postgres

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var _ kernel.ControlStore = (*KernelStore)(nil)

// ApplyControl uses a locked, validated current Run and a single database
// transaction for revocation, revision CAS, state, journal and outbox.
// Feature authorization/Tool binding is verified before this trusted call.
func (store *KernelStore) ApplyControl(ctx context.Context, runID string, expectedRevision uint64,
	guard kernel.ControlGuard, mutation kernel.StoreMutation) (kernel.Snapshot, bool, error) {
	revoked := false
	snapshot, err := store.applyWithExecution(ctx, nil, &guard, &revoked, runID, expectedRevision, mutation)
	if err != nil {
		return kernel.Snapshot{}, false, err
	}
	return snapshot, revoked, nil
}

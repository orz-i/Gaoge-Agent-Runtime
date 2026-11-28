package conformance

import (
	"context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func (s *ea1StartSettlementStore) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	if s.mode == "P4" && s.finishCommits != 0 && s.original.ID == id {
		return kernel.OwnedRetirementReceipt{}, s.fault
	}
	return s.PhysicalExecutionStore.LoadOwnedRetirement(ctx, id)
}

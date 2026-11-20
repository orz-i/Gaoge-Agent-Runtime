package memory

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var _ kernel.PreparedRootSettlementStore = (*Store)(nil)

// An initial Deferred Root must have actually completed and ACKed its
// original first-party Prepare callback. This never borrows the retirement
// of a later Model, Tool or Host Approval callback.
func (s *Store) VerifyPreparedRootSettlement(ctx context.Context, expected kernel.Snapshot) error {
	return s.verifyRootSettlement(ctx, expected, kernel.VerifyPreparedRootSettlementFacts)
}

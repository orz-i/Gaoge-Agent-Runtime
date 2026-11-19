package postgres

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var _ kernel.PreparedRootSettlementStore = (*KernelStore)(nil)

// One original Root row lock protects both an ACKed Prepare retirement and
// the next physical Start or Kernel Cancel. No extra owner/lease state.
func (s *KernelStore) VerifyPreparedRootSettlement(ctx context.Context, expected kernel.Snapshot) error {
	return s.verifyRootSettlement(ctx, expected, true)
}

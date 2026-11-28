package memory

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

var _ budget.LedgerStore = (*Store)(nil)

func (s *Store) LoadBudget(ctx context.Context, id string) (budget.Ledger, error) {
	if err := ctx.Err(); err != nil {
		return budget.Ledger{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ledgers.LoadBudget(ctx, id)
}
func (s *Store) SaveBudget(ctx context.Context, l budget.Ledger, rev uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ledgers.SaveBudget(ctx, l, rev)
}

package sqlite

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget/sqlledger"
)

var _ budget.LedgerStore = (*Store)(nil)

func (s *Store) LoadBudget(ctx context.Context, id string) (budget.Ledger, error) {
	l, err := (sqlledger.Store{DB: s.db}).LoadBudget(ctx, id)
	return l, conflictOrDatabaseError(err)
}
func (s *Store) SaveBudget(ctx context.Context, l budget.Ledger, rev uint64) error {
	return conflictOrDatabaseError((sqlledger.Store{DB: s.db}).SaveBudget(ctx, l, rev))
}

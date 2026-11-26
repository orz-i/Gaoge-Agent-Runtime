package postgres

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget/sqlledger"
)

var _ budget.LedgerStore = (*KernelStore)(nil)

func (store *KernelStore) LoadBudget(ctx context.Context, id string) (budget.Ledger, error) {
	db := store.db.WithContext(ctx)
	return (sqlledger.Store{DB: db.Statement.ConnPool}).LoadBudget(ctx, id)
}
func (store *KernelStore) SaveBudget(ctx context.Context, l budget.Ledger, rev uint64) error {
	db := store.db.WithContext(ctx)
	return (sqlledger.Store{DB: db.Statement.ConnPool}).SaveBudget(ctx, l, rev)
}

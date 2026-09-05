package budget

import (
	"context"
	"sync"
)

// MemoryLedgerStore is the reference CAS implementation for embedded hosts.
type MemoryLedgerStore struct {
	mu      sync.RWMutex
	ledgers map[string]Ledger
}

func NewMemoryLedgerStore() *MemoryLedgerStore {
	return &MemoryLedgerStore{ledgers: map[string]Ledger{}}
}

func (store *MemoryLedgerStore) LoadBudget(_ context.Context, id string) (Ledger, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	ledger, ok := store.ledgers[id]
	if !ok {
		return Ledger{}, ErrLedgerNotFound
	}
	return CloneLedger(ledger), nil
}

func (store *MemoryLedgerStore) SaveBudget(_ context.Context, ledger Ledger, revision uint64) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if ledger.ID == "" || ledger.Revision != revision+1 || !ValidLimits(ledger.Limits) {
		return ErrInvalidUsage
	}
	if store.ledgers[ledger.ID].Revision != revision {
		return ErrLedgerConflict
	}
	store.ledgers[ledger.ID] = CloneLedger(ledger)
	return nil
}

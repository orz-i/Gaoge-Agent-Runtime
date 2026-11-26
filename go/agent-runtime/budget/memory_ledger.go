package budget

import (
	"context"
	"sync"
)

// LedgerMap is the shared Memory CAS implementation. Callers hold their
// enclosing Store lock; the Runtime uses the same lock for Run and accounting.
type LedgerMap map[string]Ledger

func (m LedgerMap) LoadBudget(_ context.Context, id string) (Ledger, error) {
	l, ok := m[id]
	if !ok {
		return Ledger{}, ErrLedgerNotFound
	}
	return CloneLedger(l), nil
}
func (m LedgerMap) SaveBudget(_ context.Context, l Ledger, rev uint64) error {
	if err := ValidateLedgerWrite(l, rev); err != nil {
		return err
	}
	if m[l.ID].Revision != rev {
		return ErrLedgerConflict
	}
	m[l.ID] = CloneLedger(l)
	return nil
}

// MemoryLedgerStore is the standalone accounting CAS for non-Run consumers.
type MemoryLedgerStore struct {
	mu      sync.RWMutex
	ledgers LedgerMap
}

func NewMemoryLedgerStore() *MemoryLedgerStore { return &MemoryLedgerStore{ledgers: make(LedgerMap)} }
func (s *MemoryLedgerStore) LoadBudget(ctx context.Context, id string) (Ledger, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ledgers.LoadBudget(ctx, id)
}
func (s *MemoryLedgerStore) SaveBudget(ctx context.Context, l Ledger, rev uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ledgers.SaveBudget(ctx, l, rev)
}

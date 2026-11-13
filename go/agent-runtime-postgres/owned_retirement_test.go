package postgres

import (
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestOwnedRetirementGORM(t *testing.T) {
	conformance.RunOwnedRetirementStoreSuite(t, func(t *testing.T) conformance.ManagedRecoveryStoreFixture {
		db := openKernelStoreTestDB(t)
		pool, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		// Auxiliary SQLite/GORM serializes transactions; native PostgreSQL uses independent pools.
		pool.SetMaxOpenConns(1)
		return conformance.ManagedRecoveryStoreFixture{
			Store: NewKernelStore(db), Peer: NewKernelStore(db),
			AdvanceTo: func(target time.Time) {
				if d := time.Until(target) + 50*time.Millisecond; d > 0 {
					time.Sleep(d)
				}
			},
			// Fresh wrapper, not a persistent-process restart.
			Reopen: func() kernel.Store { return NewKernelStore(db) },
		}
	})
}

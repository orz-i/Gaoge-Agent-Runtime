package postgres

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestEA1MiddlewareNextLifetimeGORM(t *testing.T) {
	conformance.RunEA1MiddlewareNextLifetime(t, func(t *testing.T) (kernel.Store, kernel.Store, func() kernel.Store) {
		// Auxiliary adapter coverage retains the same database; it is not a
		// native PostgreSQL independent-pool or durable restart claim.
		db := openKernelStoreTestDB(t)
		return NewKernelStore(db), NewKernelStore(db), func() kernel.Store { return NewKernelStore(db) }
	})
}

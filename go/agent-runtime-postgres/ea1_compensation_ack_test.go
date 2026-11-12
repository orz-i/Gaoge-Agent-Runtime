package postgres

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// This supplements durable-backend reopen tests with the identical state
// machine and fresh Runtime/Workflow owner, not a persistent process restart.
func TestEA1CompensationMarkAckGORM(t *testing.T) {
	conformance.RunEA1CompensationMarkAckSuite(t, func(t *testing.T) (kernel.Store, func() kernel.Store) {
		store := NewKernelStore(openKernelStoreTestDB(t))
		return store, func() kernel.Store { return store }
	})
}

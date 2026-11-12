package memory_test

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

// This supplements durable-backend reopen tests with the identical state
// machine and fresh Runtime/Workflow owner, not a persistent process restart.
func TestEA1CompensationMarkAckMemory(t *testing.T) {
	conformance.RunEA1CompensationMarkAckSuite(t, func(t *testing.T) (kernel.Store, func() kernel.Store) {
		store := memory.NewStore()
		return store, func() kernel.Store { return store }
	})
}

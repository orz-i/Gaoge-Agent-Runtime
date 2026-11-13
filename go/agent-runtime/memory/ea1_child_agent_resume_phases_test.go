package memory_test

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func TestEA1ChildAgentResumePhasesMemory(t *testing.T) {
	conformance.RunEA1ChildAgentResumePhases(t, func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store) {
		store := memory.NewStore()
		// Memory cannot model process persistence; a fresh Kernel/Runner shares
		// the same Store state while persistence is tested by SQLite and PG.
		return store, store, func() kernel.Store { return store }
	})
}

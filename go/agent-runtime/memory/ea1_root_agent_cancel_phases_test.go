package memory_test

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func TestEA1RootAgentCancelPhasesMemory(t *testing.T) {
	conformance.RunEA1RootAgentCancelPhases(t, func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store) {
		s := memory.NewStore()
		return s, s, func() kernel.Store { return s }
	})
}

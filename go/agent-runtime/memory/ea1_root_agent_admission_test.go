package memory_test

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func TestEA1RootAgentAdmissionMemory(t *testing.T) {
	conformance.RunEA1RootAgentAdmission(t, func(t *testing.T) (kernel.Store, kernel.Store) { s := memory.NewStore(); return s, s })
}

func TestEA1RootAgentDefaultAdmissionMemory(t *testing.T) {
	conformance.RunEA1RootAgentDefaultAdmission(t, func(t *testing.T) (kernel.Store, kernel.Store) { s := memory.NewStore(); return s, s })
}

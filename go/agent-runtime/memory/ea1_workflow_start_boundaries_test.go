package memory_test

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func TestEA1WorkflowStartBoundariesMemory(t *testing.T) {
	conformance.RunEA1WorkflowStartBoundaries(t, func(t *testing.T) (kernel.Store, kernel.Store) { s := memory.NewStore(); return s, s })
}

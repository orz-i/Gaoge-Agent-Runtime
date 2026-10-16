package memory_test

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func TestControlStoreConformance(t *testing.T) {
	conformance.RunKernelControlSuite(t, func(_ *testing.T) kernel.Store { return memory.NewStore() })
}

func TestExecutionStoreConformance(t *testing.T) {
	conformance.RunKernelExecutionSuite(t, func(_ *testing.T) kernel.Store { return memory.NewStore() })
}

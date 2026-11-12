package memory_test

import (
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"testing"
)

func TestEA1WorkflowEntryPhasesMemory(t *testing.T) {
	factory := func(sub *testing.T) kernel.Store { return memory.NewStore() }
	t.Run("RootWaitP0", func(t *testing.T) { conformance.RunEA1RootWaitP0(t, factory) })
	t.Run("ResumeCompensationP3", func(t *testing.T) { conformance.RunEA1WorkflowResumeCompensationP3(t, factory) })
}

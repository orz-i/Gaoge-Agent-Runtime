package sqlite_test

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestEA1WorkflowEntryPhasesSQLite(t *testing.T) {
	factory := func(sub *testing.T) kernel.Store { return openStore(sub) }
	t.Run("RootWaitP0", func(t *testing.T) { conformance.RunEA1RootWaitP0(t, factory) })
	t.Run("ResumeCompensationP3", func(t *testing.T) { conformance.RunEA1WorkflowResumeCompensationP3(t, factory) })
}

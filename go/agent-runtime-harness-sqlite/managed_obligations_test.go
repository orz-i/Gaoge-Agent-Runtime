package harnesssqlite_test

import (
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestSQLiteManagedOriginalObligationsReopen(t *testing.T) {
	conformance.ManagedOriginalObligations(t, func(t *testing.T) (kernel.Store, harness.Store, func() (kernel.Store, harness.Store), func()) {
		k, h, reopen := newReopenKernelHost(t)
		return k, h, reopen, nil
	}, []string{"resume", "cancel closure", "public cancel", "claimed/before", "claimed/after", "completed/before", "completed/after",
		"input_applied/before", "input_applied/after", "initial_input/before", "initial_input/after", "initial_intent/before", "initial_intent/after"})
}

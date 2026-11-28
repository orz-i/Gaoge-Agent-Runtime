package harness_test

import (
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func TestManagedPhysicalDispatchMemory(t *testing.T) {
	conformance.ManagedPhysicalDispatch(t, func(t *testing.T) (kernel.Store, harness.Store, func() (kernel.Store, harness.Store)) {
		k, h := memory.NewStore(), harness.NewMemoryStore()
		return k, h, func() (kernel.Store, harness.Store) { return k, h }
	})
}

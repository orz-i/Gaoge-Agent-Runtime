package memory_test

import (
	"testing"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/memory"
)

func TestKernelStoreConformance(t *testing.T) {
	t.Parallel()
	conformance.RunKernelStoreSuite(t, func(testing.TB) kernel.Store {
		return memory.NewStore()
	})
}

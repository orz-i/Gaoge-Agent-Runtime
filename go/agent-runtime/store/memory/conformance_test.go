package memory_test

import (
	"testing"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/store/memory"
)

func TestStoreConformance(t *testing.T) {
	conformance.RunStore(t, func(testing.TB) agentruntime.Store { return memory.NewStore() })
}

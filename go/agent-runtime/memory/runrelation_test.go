package memory_test

import (
	"testing"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runrelation"
)

func TestRunRelationStoreConformance(t *testing.T) {
	t.Parallel()
	conformance.RunRunRelationStoreSuite(t, func(testing.TB) runrelation.Store {
		return memory.NewRunRelationStore()
	})
}

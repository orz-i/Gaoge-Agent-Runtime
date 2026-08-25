package postgres

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

func TestWorkflowDefinitionStoreConformance(t *testing.T) {
	t.Parallel()
	conformance.RunWorkflowDefinitionStoreSuite(t, func(tb testing.TB) workflow.DefinitionStore {
		tb.Helper()
		return NewWorkflowDefinitionStore(openKernelStoreTestDB(tb))
	})
}

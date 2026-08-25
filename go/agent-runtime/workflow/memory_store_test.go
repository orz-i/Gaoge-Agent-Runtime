package workflow_test

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

func TestWorkflowDefinitionMemoryStoreConformance(t *testing.T) {
	t.Parallel()
	conformance.RunWorkflowDefinitionStoreSuite(t, func(testing.TB) workflow.DefinitionStore {
		return workflow.NewMemoryDefinitionStore()
	})
}

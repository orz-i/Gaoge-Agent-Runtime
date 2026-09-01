package workflow_test

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

func TestRuntimeBudgetProjectsOnlyCommonWorkflowDimensions(t *testing.T) {
	t.Parallel()
	view := workflow.View{
		Definition: workflow.Definition{
			Limits: workflow.Limits{MaxStateBytes: 4096, MaxEffects: 9},
			Policy: workflow.DefinitionPolicy{MaxCostUnits: 50},
		},
		Budget: workflow.Budget{StateBytes: 1024, CostUnitsUsed: 17, Effects: 4, Segments: 3},
		Effects: []workflow.Effect{
			{ChildRunID: "child-1"}, {ChildRunID: "child-1"}, {ChildRunID: "child-2"}, {},
		},
	}
	common := workflow.RuntimeBudget(view)
	if common.Limits.MaxStateBytes != 4096 || common.Limits.MaxCostUnits != 50 ||
		common.Usage.StateBytes != 1024 || common.Usage.CostUnits != 17 || common.Usage.ChildRuns != 2 {
		t.Fatalf("common budget = %#v", common)
	}
	if common.Limits.MaxToolCalls != 0 || common.Usage.ToolCalls != 0 {
		t.Fatalf("workflow-specific effects were incorrectly flattened into tool calls: %#v", common)
	}
}

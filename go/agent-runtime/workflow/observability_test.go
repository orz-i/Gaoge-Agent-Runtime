package workflow_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/observability"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

type observedWorkflowExecutor struct {
	runtime *kernel.Runtime
	calls   int
}

func (executor *observedWorkflowExecutor) Execute(
	_ context.Context,
	request workflow.EffectRequest,
) (workflow.EffectResult, error) {
	executor.calls++
	if _, err := executor.runtime.Load(context.Background(), request.RunID); err != nil {
		return workflow.EffectResult{}, err
	}
	return workflow.EffectResult{
		Disposition: workflow.DispositionCompleted,
		ReceiptID:   "receipt-observed",
		ChildRunID:  "child-observed",
		Output:      json.RawMessage(`{"ok":true}`),
		CostUnits:   2,
	}, nil
}

func TestWorkflowTelemetryProjectsRunEffectAndCommonBudget(t *testing.T) {
	runtime := newWorkflowRuntime(t)
	executor := &observedWorkflowExecutor{runtime: runtime}
	events := make([]observability.Event, 0)
	runner, err := workflow.NewRunner(workflow.Dependencies{
		Runtime: runtime, Effects: executor,
		Telemetry: []observability.Recorder{observability.RecorderFunc{
			RecorderName: "capture",
			RecordFunc: func(_ context.Context, event observability.Event) {
				events = append(events, event)
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
		ID: "observed-workflow", Revision: 1, Name: "Observed Workflow",
		Nodes: []workflow.Node{
			{
				ID: "call", Type: workflow.NodeEffect,
				Effect: &workflow.EffectNode{
					Kind: "story.inspect", Input: json.RawMessage(`{"storyID":"story-1"}`), MaxCostUnits: 2,
				},
			},
			returnNode(json.RawMessage(`{"status":"done"}`)),
		},
		Policy: workflow.DefinitionPolicy{
			MaxCostUnits: 4, CostClass: workflow.CostLow, SideEffectClass: workflow.SideEffectRead,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.StartRun(t.Context(), workflowRequest(definition))
	if err != nil || snapshot.Run.Status != kernel.RunStatusCompleted || executor.calls != 1 {
		t.Fatalf("snapshot=%#v calls=%d err=%v", snapshot.Run, executor.calls, err)
	}
	view, err := workflow.ViewState(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	common := workflow.RuntimeBudget(view)
	if common.Limits.MaxCostUnits != 4 || common.Usage.CostUnits != 2 || common.Usage.ChildRuns != 1 ||
		common.Usage.StateBytes <= 0 {
		t.Fatalf("runtime budget = %#v", common)
	}
	startedRuns, completedRuns, startedEffects, completedEffects := 0, 0, 0, 0
	for _, event := range events {
		switch {
		case event.Scope == observability.ScopeRun && event.Phase == observability.PhaseStarted:
			startedRuns++
		case event.Scope == observability.ScopeRun && event.Phase == observability.PhaseCompleted:
			completedRuns++
			if event.Usage.CostUnits != 2 || event.Usage.ChildRuns != 1 || event.Usage.StateBytes <= 0 {
				t.Fatalf("terminal workflow telemetry = %#v", event)
			}
		case event.Scope == observability.ScopeWorkflowEffect && event.Phase == observability.PhaseStarted:
			startedEffects++
		case event.Scope == observability.ScopeWorkflowEffect && event.Phase == observability.PhaseCompleted:
			completedEffects++
			if event.Operation != "story.inspect" || event.ChildRunID != "child-observed" || event.Usage.CostUnits != 2 {
				t.Fatalf("effect telemetry = %#v", event)
			}
		}
	}
	if startedRuns != 1 || completedRuns != 1 || startedEffects != 1 || completedEffects != 1 {
		t.Fatalf("telemetry events = %#v", events)
	}
}

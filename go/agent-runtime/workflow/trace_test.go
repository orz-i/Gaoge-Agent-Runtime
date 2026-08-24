package workflow_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

func TestTraceProjectionExcludesWorkflowAndEffectContent(t *testing.T) {
	t.Parallel()
	runtime := newWorkflowRuntime(t)
	executor := &requestExecutor{handler: func(request workflow.EffectRequest) workflow.EffectResult {
		return completedEffect(request, json.RawMessage(`{"providerSecret":"top-secret-output"}`))
	}}
	runner := newWorkflowRunner(t, runtime, executor)
	definition := compileAdvancedWorkflow(t, []workflow.Node{
		{
			ID: "effect", Type: workflow.NodeEffect,
			Effect: &workflow.EffectNode{Kind: "redacted.effect", FromInput: true},
		},
		{
			ID: "done", Type: workflow.NodeReturn,
			Return: &workflow.ReturnNode{Source: &workflow.ValueSource{Kind: workflow.ValueNodeOutput, NodeID: "effect"}},
		},
	}, workflow.DefinitionPolicy{})
	request := workflowRequest(definition)
	request.Input = json.RawMessage(`{"prompt":"top-secret-input"}`)
	completed, err := runner.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	trace, err := runner.TraceForActor(t.Context(), completed.Run.ID, request.Actor)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "top-secret") || len(trace.Effects) != 1 ||
		trace.Effects[0].Kind != "redacted.effect" || trace.Effects[0].Status != workflow.EffectCompleted {
		t.Fatalf("trace=%s", encoded)
	}
	_, err = runner.TraceForActor(t.Context(), completed.Run.ID, kernel.ActorRef{
		TenantID: request.Actor.TenantID, ActorID: "other",
	})
	if !errors.Is(err, workflow.ErrEffectForbidden) {
		t.Fatalf("err=%v", err)
	}
}

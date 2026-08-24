package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

func TestAdvancedDataflowSelectsOneForwardBranch(t *testing.T) {
	t.Parallel()
	runtime := newWorkflowRuntime(t)
	executor := &requestExecutor{handler: func(request workflow.EffectRequest) workflow.EffectResult {
		switch request.Kind {
		case "story.inspect":
			return completedEffect(request, json.RawMessage(`{"approved":true,"payload":{"scene":7}}`))
		case "shot.plan":
			return completedEffect(request, request.Input)
		default:
			return workflow.EffectResult{Disposition: workflow.DispositionFailed, ErrorCode: "unexpected"}
		}
	}}
	runner := newWorkflowRunner(t, runtime, executor)
	definition := compileAdvancedWorkflow(t, []workflow.Node{
		{
			ID: "inspect", Type: workflow.NodeEffect,
			Effect: &workflow.EffectNode{Kind: "story.inspect", FromInput: true, MaxCostUnits: 1},
		},
		{
			ID: "route", Type: workflow.NodeIf,
			If: &workflow.IfNode{
				Condition:  workflow.ValueSource{Kind: workflow.ValueNodeOutput, NodeID: "inspect", Pointer: "/approved"},
				ThenNodeID: "approved", ElseNodeID: "rejected",
			},
		},
		{
			ID: "approved", Type: workflow.NodeAgentTask, Next: "done",
			AgentTask: &workflow.AgentTaskNode{
				AgentKey: "shot.plan", Revision: "agent-v3",
				Input:        workflow.ValueSource{Kind: workflow.ValueNodeOutput, NodeID: "inspect", Pointer: "/payload"},
				MaxCostUnits: 2,
			},
		},
		{
			ID: "rejected", Type: workflow.NodeEffect,
			Effect: &workflow.EffectNode{Kind: "never.called", Input: json.RawMessage(`null`)},
		},
		{
			ID: "done", Type: workflow.NodeReturn,
			Return: &workflow.ReturnNode{Source: &workflow.ValueSource{Kind: workflow.ValueNodeOutput, NodeID: "approved"}},
		},
	}, workflow.DefinitionPolicy{MaxCostUnits: 3, CostClass: workflow.CostLow, SideEffectClass: workflow.SideEffectRead})

	snapshot, err := driveWorkflow(t, runner, workflowRequest(definition))
	if err != nil || snapshot.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("snapshot=%#v err=%v", snapshot.Run, err)
	}
	var result map[string]int
	if err = json.Unmarshal(snapshot.Result.Content, &result); err != nil || result["scene"] != 7 {
		t.Fatalf("result=%s err=%v", snapshot.Result.Content, err)
	}
	requests := executor.Requests()
	if len(requests) != 2 || requests[0].Kind != "story.inspect" ||
		requests[1].Class != workflow.EffectClassAgent || requests[1].Revision != "agent-v3" {
		t.Fatalf("requests=%#v", requests)
	}
}

func TestWaitPayloadAndReturnUseTypedSources(t *testing.T) {
	t.Parallel()
	runtime := newWorkflowRuntime(t)
	runner := newWorkflowRunner(t, runtime, &requestExecutor{})
	definition := compileAdvancedWorkflow(t, []workflow.Node{
		{
			ID: "review", Type: workflow.NodeWait,
			Wait: &workflow.WaitNode{
				Kind:   "story.review",
				Source: &workflow.ValueSource{Kind: workflow.ValueWorkflowInput, Pointer: "/review"},
			},
		},
		{
			ID: "done", Type: workflow.NodeReturn,
			Return: &workflow.ReturnNode{Source: &workflow.ValueSource{
				Kind: workflow.ValueWaitResponse, NodeID: "review", Pointer: "/approved",
			}},
		},
	}, workflow.DefinitionPolicy{})
	request := workflowRequest(definition)
	request.Input = json.RawMessage(`{"review":{"role":"director"}}`)
	waiting, err := runner.StartRun(t.Context(), request)
	if err != nil || waiting.Run.Status != kernel.RunStatusWaitingInput || waiting.Checkpoint == nil {
		t.Fatalf("status=%s err=%v", waiting.Run.Status, err)
	}
	waitRequest, err := workflow.WaitRequestFromCheckpoint(waiting.Checkpoint)
	if err != nil || string(waitRequest.Payload) != `{"role":"director"}` {
		t.Fatalf("request=%#v err=%v", waitRequest, err)
	}
	completed, err := runner.ResolveWait(
		t.Context(), waiting.Run.ID, waiting.Run.Revision, json.RawMessage(`{"approved":true}`),
	)
	if err != nil || string(completed.Result.Content) != "true" {
		t.Fatalf("result=%#v err=%v", completed.Result, err)
	}
}

func TestParallelDispatchIsConcurrentAndOutputIsNamed(t *testing.T) {
	t.Parallel()
	runtime := newWorkflowRuntime(t)
	executor := newConcurrencyExecutor(2)
	runner := newWorkflowRunner(t, runtime, executor)
	definition := compileAdvancedWorkflow(t, []workflow.Node{
		{
			ID: "render", Type: workflow.NodeParallel,
			Parallel: &workflow.ParallelNode{
				MaxConcurrency: 2,
				Branches: []workflow.ParallelBranch{
					{ID: "left", Call: genericCall("render.left", workflow.ValueSource{Kind: workflow.ValueLiteral, Value: json.RawMessage(`1`)}, 1)},
					{ID: "right", Call: genericCall("render.right", workflow.ValueSource{Kind: workflow.ValueLiteral, Value: json.RawMessage(`2`)}, 1)},
				},
			},
		},
		{
			ID: "done", Type: workflow.NodeReturn,
			Return: &workflow.ReturnNode{Source: &workflow.ValueSource{Kind: workflow.ValueNodeOutput, NodeID: "render"}},
		},
	}, workflow.DefinitionPolicy{MaxCostUnits: 2, CostClass: workflow.CostLow, SideEffectClass: workflow.SideEffectRead})

	completed, err := runner.StartRun(t.Context(), workflowRequest(definition))
	if err != nil || executor.MaxActive() != 2 {
		t.Fatalf("status=%s max=%d err=%v", completed.Run.Status, executor.MaxActive(), err)
	}
	var result map[string]int
	if err = json.Unmarshal(completed.Result.Content, &result); err != nil || result["left"] != 1 || result["right"] != 2 {
		t.Fatalf("result=%s err=%v", completed.Result.Content, err)
	}
}

func TestMapHonoursConcurrencyAndPreservesInputOrder(t *testing.T) {
	t.Parallel()
	runtime := newWorkflowRuntime(t)
	executor := newConcurrencyExecutor(2)
	runner := newWorkflowRunner(t, runtime, executor)
	definition := compileAdvancedWorkflow(t, []workflow.Node{
		{
			ID: "shots", Type: workflow.NodeMap,
			Map: &workflow.MapNode{
				Items:          workflow.ValueSource{Kind: workflow.ValueWorkflowInput, Pointer: "/shots"},
				Call:           genericCall("shot.render", workflow.ValueSource{Kind: workflow.ValueMapItem}, 1),
				MaxConcurrency: 2,
			},
		},
		{
			ID: "done", Type: workflow.NodeReturn,
			Return: &workflow.ReturnNode{Source: &workflow.ValueSource{Kind: workflow.ValueNodeOutput, NodeID: "shots"}},
		},
	}, workflow.DefinitionPolicy{MaxCostUnits: 4, CostClass: workflow.CostLow, SideEffectClass: workflow.SideEffectRead})
	request := workflowRequest(definition)
	request.Input = json.RawMessage(`{"shots":["s1","s2","s3","s4"]}`)
	completed, err := runner.StartRun(t.Context(), request)
	if err != nil || executor.MaxActive() != 2 {
		t.Fatalf("status=%s max=%d err=%v", completed.Run.Status, executor.MaxActive(), err)
	}
	if string(completed.Result.Content) != `["s1","s2","s3","s4"]` {
		t.Fatalf("result=%s", completed.Result.Content)
	}
	requests := executor.Requests()
	seen := make(map[int]bool, len(requests))
	for _, request := range requests {
		seen[request.MapIndex] = true
	}
	if len(requests) != 4 || !seen[0] || !seen[1] || !seen[2] || !seen[3] {
		t.Fatalf("requests=%#v", requests)
	}
}

func TestSubworkflowDispatchCarriesExactImmutableReference(t *testing.T) {
	t.Parallel()
	runtime := newWorkflowRuntime(t)
	executor := &requestExecutor{handler: func(request workflow.EffectRequest) workflow.EffectResult {
		return completedEffect(request, json.RawMessage(`{"child":"complete"}`))
	}}
	runner := newWorkflowRunner(t, runtime, executor)
	reference := workflow.DefinitionReference{ID: "story.shot-plan.v2", Revision: 7, Hash: "sha256:exact"}
	definition := compileAdvancedWorkflow(t, []workflow.Node{
		{
			ID: "child", Type: workflow.NodeSubworkflow,
			Subworkflow: &workflow.SubworkflowNode{
				Definition: reference, Input: workflow.ValueSource{Kind: workflow.ValueWorkflowInput}, MaxCostUnits: 1,
			},
		},
		{
			ID: "done", Type: workflow.NodeReturn,
			Return: &workflow.ReturnNode{Source: &workflow.ValueSource{Kind: workflow.ValueNodeOutput, NodeID: "child"}},
		},
	}, workflow.DefinitionPolicy{MaxCostUnits: 1, CostClass: workflow.CostLow, SideEffectClass: workflow.SideEffectRead})
	completed, err := runner.StartRun(t.Context(), workflowRequest(definition))
	requests := executor.Requests()
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted || len(requests) != 1 ||
		requests[0].Class != workflow.EffectClassSubworkflow || requests[0].Definition == nil ||
		*requests[0].Definition != reference || requests[0].NestedDepth != 1 {
		t.Fatalf("status=%s requests=%#v err=%v", completed.Run.Status, requests, err)
	}
}

func TestRegisteredSubworkflowRunsExactRevisionAfterHeadIsDisabled(t *testing.T) {
	t.Parallel()
	runtime := newWorkflowRuntime(t)
	registry, err := workflow.NewDefinitionRegistry(workflow.NewMemoryDefinitionStore(), registryClock{})
	if err != nil {
		t.Fatal(err)
	}
	scope := workflow.DefinitionScope{
		Kind: workflow.DefinitionScopeActor, TenantID: "tenant", ActorID: "actor",
	}
	published, head, _, err := registry.Publish(t.Context(), workflow.PublishDefinitionRequest{
		Scope: scope, ExpectedRevision: 0, IdempotencyKey: "child-v1", PublishedBy: "actor",
		Draft: workflow.DefinitionDraft{
			ID: "child", Name: "Child",
			Nodes: []workflow.Node{{
				ID: "done", Type: workflow.NodeReturn,
				Return: &workflow.ReturnNode{Source: &workflow.ValueSource{Kind: workflow.ValueWorkflowInput}},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = registry.SetActivation(t.Context(), workflow.ActivateDefinitionRequest{
		Scope: scope, DefinitionID: "child", Availability: workflow.DefinitionDisabled,
		ExpectedVersion: head.Version,
	}); err != nil {
		t.Fatal(err)
	}
	external := &requestExecutor{}
	runner, err := workflow.NewRunner(workflow.Dependencies{
		Runtime: runtime, Effects: external, Registry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := compileAdvancedWorkflow(t, []workflow.Node{
		{
			ID: "child", Type: workflow.NodeSubworkflow,
			Subworkflow: &workflow.SubworkflowNode{
				Definition: workflow.DefinitionReference{
					ID: "child", Revision: published.Definition.Revision, Hash: published.Definition.Hash,
				},
				Input: workflow.ValueSource{Kind: workflow.ValueWorkflowInput},
			},
		},
		{
			ID: "done", Type: workflow.NodeReturn,
			Return: &workflow.ReturnNode{Source: &workflow.ValueSource{Kind: workflow.ValueNodeOutput, NodeID: "child"}},
		},
	}, workflow.DefinitionPolicy{})
	request := workflowRequest(parent)
	request.Input = json.RawMessage(`{"snapshot":"immutable"}`)
	completed, err := runner.StartRun(t.Context(), request)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted ||
		string(completed.Result.Content) != `{"snapshot":"immutable"}` || len(external.Requests()) != 0 {
		t.Fatalf("status=%s result=%#v external=%#v err=%v", completed.Run.Status, completed.Result, external.Requests(), err)
	}
}

func TestCostBudgetFailsBeforeAnyEffectDispatch(t *testing.T) {
	t.Parallel()
	runtime := newWorkflowRuntime(t)
	executor := &requestExecutor{}
	runner := newWorkflowRunner(t, runtime, executor)
	definition := compileAdvancedWorkflow(t, []workflow.Node{
		{
			ID: "over-budget", Type: workflow.NodeParallel,
			Parallel: &workflow.ParallelNode{
				MaxConcurrency: 2,
				Branches: []workflow.ParallelBranch{
					{ID: "one", Call: genericCall("one", workflow.ValueSource{Kind: workflow.ValueLiteral, Value: json.RawMessage(`1`)}, 1)},
					{ID: "two", Call: genericCall("two", workflow.ValueSource{Kind: workflow.ValueLiteral, Value: json.RawMessage(`2`)}, 1)},
				},
			},
		},
		{ID: "done", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`null`)}},
	}, workflow.DefinitionPolicy{MaxCostUnits: 1, CostClass: workflow.CostLow, SideEffectClass: workflow.SideEffectRead})
	failed, err := runner.StartRun(t.Context(), workflowRequest(definition))
	if !errors.Is(err, workflow.ErrBudgetExceeded) || failed.Run.Status != kernel.RunStatusFailed ||
		len(executor.Requests()) != 0 {
		t.Fatalf("status=%s calls=%d err=%v", failed.Run.Status, len(executor.Requests()), err)
	}
}

func TestRetryReusesStableEffectIntentAndStopsAtBound(t *testing.T) {
	t.Parallel()
	runtime := newWorkflowRuntime(t)
	executor := &requestExecutor{handler: func(request workflow.EffectRequest) workflow.EffectResult {
		if request.Attempt == 1 {
			return workflow.EffectResult{
				Disposition: workflow.DispositionFailed,
				ErrorCode:   "provider.timeout", ErrorDetail: "retryable timeout",
			}
		}
		return completedEffect(request, json.RawMessage(`{"ok":true}`))
	}}
	runner := newWorkflowRunner(t, runtime, executor)
	definition := compileAdvancedWorkflow(t, []workflow.Node{
		{
			ID: "generate", Type: workflow.NodeMediaEffect,
			MediaEffect: &workflow.MediaEffectNode{
				CapabilityKey: "visual.generate", Revision: "cap-v2",
				Input: workflow.ValueSource{Kind: workflow.ValueWorkflowInput}, MaxCostUnits: 1,
				Retry: workflow.RetryPolicy{MaxAttempts: 2, RetryableErrorCodes: []string{"provider.timeout"}},
			},
		},
		{
			ID: "done", Type: workflow.NodeReturn,
			Return: &workflow.ReturnNode{Source: &workflow.ValueSource{Kind: workflow.ValueNodeOutput, NodeID: "generate"}},
		},
	}, workflow.DefinitionPolicy{MaxCostUnits: 1, CostClass: workflow.CostLow, SideEffectClass: workflow.SideEffectWrite})
	pending, err := runner.StartRun(t.Context(), workflowRequest(definition))
	if !errors.Is(err, workflow.ErrEffectPending) || pending.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("status=%s err=%v", pending.Run.Status, err)
	}
	completed, err := runner.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("status=%s err=%v", completed.Run.Status, err)
	}
	requests := executor.Requests()
	if len(requests) != 2 || requests[0].EffectID != requests[1].EffectID ||
		requests[0].Attempt != 1 || requests[1].Attempt != 2 || requests[1].MaxAttempts != 2 {
		t.Fatalf("requests=%#v", requests)
	}
}

func TestSubworkflowDepthLimitRejectsBeforeDispatch(t *testing.T) {
	t.Parallel()
	runtime := newWorkflowRuntime(t)
	executor := &requestExecutor{}
	runner := newWorkflowRunner(t, runtime, executor)
	definition := compileAdvancedWorkflow(t, []workflow.Node{
		{
			ID: "child", Type: workflow.NodeSubworkflow,
			Subworkflow: &workflow.SubworkflowNode{
				Definition: workflow.DefinitionReference{ID: "child", Revision: 1, Hash: "exact"},
				Input:      workflow.ValueSource{Kind: workflow.ValueWorkflowInput},
			},
		},
		{ID: "done", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`null`)}},
	}, workflow.DefinitionPolicy{})
	request := workflowRequest(definition)
	request.NestedDepth = definition.Limits.MaxNestedDepth
	failed, err := runner.StartRun(t.Context(), request)
	if !errors.Is(err, workflow.ErrBudgetExceeded) || failed.Run.Status != kernel.RunStatusFailed ||
		len(executor.Requests()) != 0 {
		t.Fatalf("status=%s calls=%d err=%v", failed.Run.Status, len(executor.Requests()), err)
	}
}

func TestEmptyMapCompletesWithoutDispatch(t *testing.T) {
	t.Parallel()
	runtime := newWorkflowRuntime(t)
	executor := &requestExecutor{}
	runner := newWorkflowRunner(t, runtime, executor)
	definition := compileAdvancedWorkflow(t, []workflow.Node{
		{
			ID: "map", Type: workflow.NodeMap,
			Map: &workflow.MapNode{
				Items:          workflow.ValueSource{Kind: workflow.ValueWorkflowInput},
				Call:           genericCall("noop", workflow.ValueSource{Kind: workflow.ValueMapItem}, 0),
				MaxConcurrency: 1,
			},
		},
		{
			ID: "done", Type: workflow.NodeReturn,
			Return: &workflow.ReturnNode{Source: &workflow.ValueSource{Kind: workflow.ValueNodeOutput, NodeID: "map"}},
		},
	}, workflow.DefinitionPolicy{})
	request := workflowRequest(definition)
	request.Input = json.RawMessage(`[]`)
	completed, err := runner.StartRun(t.Context(), request)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted ||
		string(completed.Result.Content) != "[]" || len(executor.Requests()) != 0 {
		t.Fatalf("status=%s result=%#v calls=%d err=%v", completed.Run.Status, completed.Result, len(executor.Requests()), err)
	}
}

func TestCompensationsRunInReverseCompletionOrder(t *testing.T) {
	t.Parallel()
	runtime := newWorkflowRuntime(t)
	executor := &requestExecutor{handler: func(request workflow.EffectRequest) workflow.EffectResult {
		if request.Kind == "production.fail" {
			return workflow.EffectResult{
				Disposition: workflow.DispositionFailed,
				ErrorCode:   "production.failed", ErrorDetail: "boom",
			}
		}
		return completedEffect(request, request.Input)
	}}
	runner := newWorkflowRunner(t, runtime, executor)
	definition := compileAdvancedWorkflow(t, []workflow.Node{
		compensationNode("reserve", "location.reserve", "location.release"),
		compensationNode("book", "crew.book", "crew.release"),
		{
			ID: "fail", Type: workflow.NodeEffect,
			Effect: &workflow.EffectNode{Kind: "production.fail", Input: json.RawMessage(`{}`)},
		},
		{ID: "done", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`null`)}},
	}, workflow.DefinitionPolicy{MaxCostUnits: 5, CostClass: workflow.CostLow, SideEffectClass: workflow.SideEffectWrite})
	failed, err := driveWorkflow(t, runner, workflowRequest(definition))
	if !errors.Is(err, workflow.ErrEffectFailed) || failed.Run.Status != kernel.RunStatusFailed {
		t.Fatalf("status=%s err=%v", failed.Run.Status, err)
	}
	requests := executor.Requests()
	kinds := make([]string, len(requests))
	for index := range requests {
		kinds[index] = requests[index].Kind
	}
	want := []string{"location.reserve", "crew.book", "production.fail", "crew.release", "location.release"}
	if !equalStrings(kinds, want) {
		t.Fatalf("kinds=%v want=%v", kinds, want)
	}
	view := mustWorkflowView(t, failed)
	if len(view.Compensations) != 2 ||
		view.Compensations[0].Status != workflow.CompensationCompleted ||
		view.Compensations[1].Status != workflow.CompensationCompleted {
		t.Fatalf("compensations=%#v", view.Compensations)
	}
}

func TestCancelCompensatesBeforeBecomingTerminal(t *testing.T) {
	t.Parallel()
	runtime := newWorkflowRuntime(t)
	executor := &requestExecutor{handler: func(request workflow.EffectRequest) workflow.EffectResult {
		return completedEffect(request, request.Input)
	}}
	runner := newWorkflowRunner(t, runtime, executor)
	definition := compileAdvancedWorkflow(t, []workflow.Node{
		compensationNode("reserve", "location.reserve", "location.release"),
		{
			ID: "approval", Type: workflow.NodeWait,
			Wait: &workflow.WaitNode{Kind: "director.approval", Payload: json.RawMessage(`{}`)},
		},
		{ID: "done", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`null`)}},
	}, workflow.DefinitionPolicy{MaxCostUnits: 2, CostClass: workflow.CostLow, SideEffectClass: workflow.SideEffectWrite})
	waiting, err := runner.StartRun(t.Context(), workflowRequest(definition))
	if err != nil || waiting.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("status=%s err=%v", waiting.Run.Status, err)
	}
	cancelled, err := runner.Cancel(t.Context(), waiting.Run.ID, waiting.Run.Revision, "director stopped production")
	if err != nil || cancelled.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("status=%s err=%v", cancelled.Run.Status, err)
	}
	requests := executor.Requests()
	if len(requests) != 2 || requests[1].Kind != "location.release" || !requests[1].Compensation {
		t.Fatalf("requests=%#v", requests)
	}
}

func compileAdvancedWorkflow(
	t *testing.T,
	nodes []workflow.Node,
	policy workflow.DefinitionPolicy,
) workflow.Definition {
	t.Helper()
	definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
		ID: "advanced-workflow", Revision: 1, Name: "Advanced Workflow",
		Nodes: nodes, Policy: policy,
	})
	if err != nil {
		t.Fatalf("compile workflow: %v", err)
	}
	return definition
}

func driveWorkflow(
	t *testing.T,
	runner *workflow.Runner,
	request workflow.StartRequest,
) (kernel.Snapshot, error) {
	t.Helper()
	snapshot, err := runner.StartRun(t.Context(), request)
	for attempts := 0; attempts < 32 && snapshot.Run.Status == kernel.RunStatusRunning; attempts++ {
		if err != nil && !errors.Is(err, workflow.ErrSegmentYielded) &&
			!errors.Is(err, workflow.ErrEffectPending) && !errors.Is(err, workflow.ErrEffectFailed) {
			return snapshot, err
		}
		snapshot, err = runner.Resume(t.Context(), snapshot.Run.ID, snapshot.Run.Revision)
	}
	return snapshot, err
}

func genericCall(kind string, input workflow.ValueSource, maxCost int64) workflow.EffectCall {
	return workflow.EffectCall{
		Class: workflow.EffectClassGeneric, Kind: kind, Input: input, MaxCostUnits: maxCost,
	}
}

func compensationNode(id string, doKind string, undoKind string) workflow.Node {
	return workflow.Node{
		ID: id, Type: workflow.NodeCompensation,
		Compensation: &workflow.CompensationNode{
			Do: genericCall(doKind, workflow.ValueSource{
				Kind: workflow.ValueLiteral, Value: json.RawMessage(`{"id":"` + id + `"}`),
			}, 1),
			Undo: genericCall(undoKind, workflow.ValueSource{
				Kind: workflow.ValueNodeOutput, NodeID: id,
			}, 0),
		},
	}
}

func completedEffect(request workflow.EffectRequest, output json.RawMessage) workflow.EffectResult {
	return workflow.EffectResult{
		Disposition: workflow.DispositionCompleted,
		ReceiptID:   "receipt-" + request.EffectID, Output: cloneTestJSON(output),
		CostUnits: request.MaxCostUnits,
	}
}

func cloneTestJSON(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func equalStrings(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

type requestExecutor struct {
	mu       sync.Mutex
	requests []workflow.EffectRequest
	handler  func(workflow.EffectRequest) workflow.EffectResult
}

func (executor *requestExecutor) Execute(
	_ context.Context,
	request workflow.EffectRequest,
) (workflow.EffectResult, error) {
	executor.mu.Lock()
	executor.requests = append(executor.requests, request)
	handler := executor.handler
	executor.mu.Unlock()
	if handler != nil {
		return handler(request), nil
	}
	return completedEffect(request, request.Input), nil
}

func (executor *requestExecutor) Requests() []workflow.EffectRequest {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return append([]workflow.EffectRequest(nil), executor.requests...)
}

type concurrencyExecutor struct {
	requestExecutor
	barrierSize int
	mu          sync.Mutex
	active      int
	maxActive   int
	arrived     int
	barrier     chan struct{}
}

func newConcurrencyExecutor(barrierSize int) *concurrencyExecutor {
	return &concurrencyExecutor{barrierSize: barrierSize, barrier: make(chan struct{})}
}

func (executor *concurrencyExecutor) Execute(
	ctx context.Context,
	request workflow.EffectRequest,
) (workflow.EffectResult, error) {
	executor.requestExecutor.mu.Lock()
	executor.requests = append(executor.requests, request)
	executor.requestExecutor.mu.Unlock()

	executor.mu.Lock()
	executor.active++
	if executor.active > executor.maxActive {
		executor.maxActive = executor.active
	}
	executor.arrived++
	if executor.arrived == executor.barrierSize {
		close(executor.barrier)
	}
	barrier := executor.barrier
	executor.mu.Unlock()
	select {
	case <-barrier:
	case <-ctx.Done():
		return workflow.EffectResult{}, ctx.Err()
	}
	executor.mu.Lock()
	executor.active--
	executor.mu.Unlock()
	return completedEffect(request, request.Input), nil
}

func (executor *concurrencyExecutor) MaxActive() int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.maxActive
}

func TestAdvancedCompileRejectsUnsafeTopologyAndPolicies(t *testing.T) {
	t.Parallel()
	tests := map[string]workflow.DefinitionDraft{
		"branch also declares next": {
			ID: "branch-next", Revision: 1, Name: "invalid",
			Nodes: []workflow.Node{
				{
					ID: "route", Type: workflow.NodeIf, Next: "done",
					If: &workflow.IfNode{
						Condition:  workflow.ValueSource{Kind: workflow.ValueWorkflowInput},
						ThenNodeID: "done", ElseNodeID: "done",
					},
				},
				{ID: "done", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`null`)}},
			},
		},
		"retry exceeds definition limit": {
			ID: "retry-limit", Revision: 1, Name: "invalid",
			Limits: workflow.Limits{MaxAttemptsPerEffect: 2},
			Nodes: []workflow.Node{
				{
					ID: "call", Type: workflow.NodeEffect,
					Effect: &workflow.EffectNode{
						Kind: "call", Input: json.RawMessage(`null`),
						Retry: workflow.RetryPolicy{MaxAttempts: 3, RetryableErrorCodes: []string{"*"}},
					},
				},
				{ID: "done", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`null`)}},
			},
		},
		"subworkflow reference is not exact": {
			ID: "reference", Revision: 1, Name: "invalid",
			Nodes: []workflow.Node{
				{
					ID: "child", Type: workflow.NodeSubworkflow,
					Subworkflow: &workflow.SubworkflowNode{
						Definition: workflow.DefinitionReference{ID: "child", Revision: 1},
						Input:      workflow.ValueSource{Kind: workflow.ValueWorkflowInput},
					},
				},
				{ID: "done", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`null`)}},
			},
		},
		"map source escapes map": {
			ID: "map-source", Revision: 1, Name: "invalid",
			Nodes: []workflow.Node{
				{
					ID: "agent", Type: workflow.NodeAgentTask,
					AgentTask: &workflow.AgentTaskNode{
						AgentKey: "agent", Revision: "v1",
						Input: workflow.ValueSource{Kind: workflow.ValueMapItem},
					},
				},
				{ID: "done", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`null`)}},
			},
		},
	}
	for name, draft := range tests {
		draft := draft
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := workflow.CompileDefinition(draft); !errors.Is(err, workflow.ErrInvalidDefinition) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

package harness_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	runtimemodel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

func TestRootAgentInvokesFrozenCapabilityAndResumesSameToolCall(t *testing.T) {
	fixture := newCapabilityToolFixture(t)
	started := startCapabilityToolHarness(t, fixture)
	root, child := rootAndWorkflowInvocations(t, started.Invocations)
	assertPendingCapabilityHarness(t, started, fixture, root, child)
	completeCapabilityChild(t, fixture.runtime, child)
	resumeCapabilityRoot(t, fixture, root)
	assertCompletedCapabilityHarness(t, fixture.runner, started.Turn.ID)
}

type capabilityToolFixture struct {
	runtime         *kernel.Runtime
	relations       *runrelation.Registry
	runner          *harness.Runner
	agentRunner     *agent.Runner
	model           *capabilityToolModel
	workflowFeature *pendingWorkflowFeature
}

func newCapabilityToolFixture(t *testing.T) capabilityToolFixture {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	relations, err := runrelation.New(memory.NewRunRelationStore(), capabilityToolClock{})
	if err != nil {
		t.Fatal(err)
	}
	materializer := workflowOnlyMaterializer{t: t}
	toolHandler := harness.NewCapabilityInvocationToolHandler(materializer)
	registry, err := tools.NewRegistry([]tools.Registration{harness.CapabilityInvocationToolRegistration(toolHandler)})
	if err != nil {
		t.Fatal(err)
	}
	schemaMiddleware, err := harness.NewCapabilityInvocationModelMiddleware(store)
	if err != nil {
		t.Fatal(err)
	}
	timeline, err := harness.NewToolTimelineMiddleware(store, capabilityToolClock{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	model := &capabilityToolModel{t: t}
	agentRunner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: model, Catalog: registry, Executor: registry,
		ModelMiddleware: []plugin.ModelMiddleware{schemaMiddleware},
		ToolMiddleware:  []plugin.ToolMiddleware{timeline},
	})
	if err != nil {
		t.Fatal(err)
	}
	workflowFeature := &pendingWorkflowFeature{runtime: runtime}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: agentRunner, Store: store, Clock: capabilityToolClock{},
		Workflows: workflowFeature, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = toolHandler.Bind(runner); err != nil {
		t.Fatal(err)
	}
	return capabilityToolFixture{
		runtime: runtime, relations: relations, runner: runner, agentRunner: agentRunner,
		model: model, workflowFeature: workflowFeature,
	}
}

func startCapabilityToolHarness(t *testing.T, fixture capabilityToolFixture) harness.Snapshot {
	t.Helper()
	commands := harness.FirstPartyCommandDescriptors()
	started, err := fixture.runner.Start(t.Context(), harness.StartRequest{
		HostThread: harness.HostRef{Kind: testThreadKind, ID: "capability-thread"},
		HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "capability-turn"},
		Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
		Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "capability-thread"},
		RequestID:  "capability-request", Goal: "coordinate the workflow",
		Config: harness.ConfigSnapshot{
			Model: "fixture", Commands: commands,
			ToolKeys: []string{harness.CapabilityInvocationToolKey},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return started
}

func assertPendingCapabilityHarness(
	t *testing.T,
	started harness.Snapshot,
	fixture capabilityToolFixture,
	root, child harness.Invocation,
) {
	t.Helper()
	if started.Turn.Status != harness.TurnRunning || root.Status != harness.InvocationRunning || child.Status != harness.InvocationRunning {
		t.Fatalf("pending Harness snapshot = %#v", started)
	}
	if fixture.model.calls != 1 || fixture.workflowFeature.starts != 1 {
		t.Fatalf("pending calls model=%d workflowStarts=%d", fixture.model.calls, fixture.workflowFeature.starts)
	}
	assertCapabilityRelation(t, fixture.relations, root.ExecutionRefID, child.ExecutionRefID, child.ID)
}

func completeCapabilityChild(t *testing.T, runtime *kernel.Runtime, child harness.Invocation) {
	t.Helper()
	childRuntime, err := runtime.Load(t.Context(), child.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Apply(t.Context(), child.ExecutionRefID, childRuntime.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: json.RawMessage(`{}`),
		Result: &kernel.Result{ContentType: "text", Content: json.RawMessage(`"child complete"`)},
	}); err != nil {
		t.Fatal(err)
	}
}

func resumeCapabilityRoot(t *testing.T, fixture capabilityToolFixture, root harness.Invocation) {
	t.Helper()
	rootRuntime, err := fixture.runtime.Load(t.Context(), root.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	completedRoot, err := fixture.agentRunner.Resume(t.Context(), root.ExecutionRefID, rootRuntime.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if completedRoot.Run.Status != kernel.RunStatusCompleted || fixture.model.calls != 2 || fixture.workflowFeature.starts != 1 {
		t.Fatalf("completed root = %#v model=%d workflowStarts=%d", completedRoot, fixture.model.calls, fixture.workflowFeature.starts)
	}
}

func assertCompletedCapabilityHarness(t *testing.T, runner *harness.Runner, turnID string) {
	t.Helper()
	completed, err := runner.Refresh(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	_, completedChild := rootAndWorkflowInvocations(t, completed.Invocations)
	if completed.Turn.Status != harness.TurnCompleted || completedChild.Status != harness.InvocationCompleted {
		t.Fatalf("completed Harness snapshot = %#v", completed)
	}
}

func rootAndWorkflowInvocations(t *testing.T, values []harness.Invocation) (harness.Invocation, harness.Invocation) {
	t.Helper()
	var root harness.Invocation
	var child harness.Invocation
	for _, value := range values {
		switch value.ExecutionClass {
		case harness.ExecutionAgent:
			root = value
		case harness.ExecutionWorkflow:
			child = value
		case harness.ExecutionTeam, harness.ExecutionPlanExecute, harness.ExecutionApplication:
			// This fixture only starts an Agent root and one Workflow child.
		default:
			t.Fatalf("unexpected execution class %q", value.ExecutionClass)
		}
	}
	if root.ID == "" || child.ID == "" || child.ParentItemID == "" {
		t.Fatalf("missing root/child invocation: %#v", values)
	}
	return root, child
}

func assertCapabilityRelation(
	t *testing.T,
	relations *runrelation.Registry,
	parentRunID, childRunID, invocationID string,
) {
	t.Helper()
	relation, err := relations.GetByChild(t.Context(), childRunID)
	if err != nil {
		t.Fatal(err)
	}
	if relation.ParentRunID != parentRunID || relation.Kind != runrelation.KindCapability || relation.OwnerNodeID != invocationID {
		t.Fatalf("capability relation = %#v", relation)
	}
}

type workflowOnlyMaterializer struct {
	t *testing.T
}

func (materializer workflowOnlyMaterializer) MaterializeCapability(
	_ context.Context,
	request harness.CapabilityMaterializeRequest,
) (harness.CapabilityInvocationSpec, error) {
	if request.Descriptor.ID != "workflow" {
		return harness.CapabilityInvocationSpec{}, harness.ErrNotFound
	}
	if materializer.t != nil && (request.Actor != (kernel.ActorRef{TenantID: testTenant, ActorID: testActor}) ||
		request.Thread != (kernel.ThreadRef{Kind: testThreadKind, ID: "capability-thread"})) {
		materializer.t.Fatalf("capability materialization identity = actor:%#v thread:%#v", request.Actor, request.Thread)
	}
	return harness.CapabilityInvocationSpec{Workflow: &harness.WorkflowCapabilitySpec{
		Definition: workflow.Definition{ID: "fixture-workflow", Revision: 1, Name: "Fixture workflow"},
		Input:      json.RawMessage(`{}`),
	}}, nil
}

type pendingWorkflowFeature struct {
	runtime *kernel.Runtime
	starts  int
}

func (feature *pendingWorkflowFeature) StartRun(ctx context.Context, request workflow.StartRequest) (kernel.Snapshot, error) {
	feature.starts++
	snapshot, err := feature.runtime.Create(ctx, kernel.CreateRequest{
		ID: request.ID, Kind: workflow.RunKind, Actor: request.Actor, Thread: request.Thread,
		RequestID: request.RequestID, Goal: request.Goal, State: json.RawMessage(`{}`),
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return snapshot, workflow.ErrEffectPending
}

func (feature *pendingWorkflowFeature) Resume(ctx context.Context, runID string, _ uint64) (kernel.Snapshot, error) {
	return feature.runtime.Load(ctx, runID)
}

func (feature *pendingWorkflowFeature) ResolveWait(
	ctx context.Context,
	runID string,
	_ uint64,
	_ json.RawMessage,
) (kernel.Snapshot, error) {
	return feature.runtime.Load(ctx, runID)
}

type capabilityToolModel struct {
	t     *testing.T
	calls int
}

func (model *capabilityToolModel) Generate(_ context.Context, request runtimemodel.Request) (runtimemodel.Response, error) {
	model.calls++
	if model.calls == 1 {
		if len(request.Tools) != 1 || request.Tools[0].Key != harness.CapabilityInvocationToolKey {
			model.t.Fatalf("root capability Tools = %#v", request.Tools)
		}
		schema := string(request.Tools[0].InputSchema)
		if !strings.Contains(schema, `"const":"workflow"`) || strings.Contains(schema, `"definition"`) || strings.Contains(schema, `"members"`) {
			model.t.Fatalf("capability schema leaked Runtime topology: %s", schema)
		}
		return runtimemodel.Response{ToolCalls: []tools.Call{{
			ID: "invoke-workflow", ToolKey: harness.CapabilityInvocationToolKey,
			Arguments: json.RawMessage(`{"commandID":"workflow","goal":"child workflow","arguments":{}}`),
		}}}, nil
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role != runtimemodel.RoleTool || last.ToolCallID != "invoke-workflow" || !strings.Contains(last.Content, "child complete") {
		model.t.Fatalf("resumed capability transcript = %#v", last)
	}
	return runtimemodel.Response{Content: "root complete"}, nil
}

type capabilityToolClock struct{}

func (capabilityToolClock) Now() time.Time {
	return time.Date(2026, 8, 19, 8, 0, 0, 0, time.UTC)
}

var _ harness.CapabilityInvocationMaterializer = workflowOnlyMaterializer{}
var _ harness.WorkflowFeature = (*pendingWorkflowFeature)(nil)
var _ runtimemodel.Client = (*capabilityToolModel)(nil)

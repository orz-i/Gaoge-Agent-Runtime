package evaluation_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/evaluation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

func TestRuntimeFeatureScenarioCorpus(t *testing.T) {
	draft := loadScenarioSuite(t, "testdata/runtime-feature-smoke.json")
	dataset, err := evaluation.CompileScenarioDataset(draft)
	if err != nil {
		t.Fatalf("compile runtime feature dataset: %v", err)
	}
	runner, err := evaluation.NewScenarioRunner(runtimeFeatureScenarioExecutor{})
	if err != nil {
		t.Fatalf("create runtime feature scenario runner: %v", err)
	}
	record, err := runner.Execute(t.Context(), evaluation.ExecuteRequest{
		RequestID: "runtime-feature-smoke-v1", TargetName: "memory-runtime-features",
		Dataset: dataset, MaxConcurrency: 1,
	})
	if err != nil {
		t.Fatalf("execute runtime feature scenario corpus: %v", err)
	}
	baseline := loadBaseline(t, "testdata/runtime-feature-smoke-baseline.json")
	comparison, err := evaluation.CompareBaseline(record, baseline)
	if err != nil {
		t.Fatalf("compare runtime feature baseline (dataset hash %s): %v", dataset.Hash, err)
	}
	if !comparison.Passed {
		t.Fatalf("runtime feature baseline regressed: %v", comparison.Regressions)
	}
	encoded, err := json.Marshal(record.Report)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("runtime feature eval report: %s", encoded)
}

type runtimeFeatureScenarioExecutor struct{}

type runtimeFeatureScenarioInput struct {
	Operation string `json:"operation"`
}

func (runtimeFeatureScenarioExecutor) Execute(
	ctx context.Context,
	request evaluation.ScenarioRequest,
) (evaluation.ScenarioObservation, error) {
	var input runtimeFeatureScenarioInput
	if err := json.Unmarshal(request.Input, &input); err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	switch input.Operation {
	case "agent_tool_once":
		return executeAgentToolScenario(ctx)
	case "workflow_wait_resume":
		return executeWorkflowWaitScenario(ctx)
	case "team_child_once":
		return executeTeamChildScenario(ctx)
	default:
		return evaluation.ScenarioObservation{}, errors.New("unsupported runtime feature scenario")
	}
}

func executeAgentToolScenario(ctx context.Context) (evaluation.ScenarioObservation, error) {
	runtime, err := newFeatureScenarioRuntime()
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	var executions atomic.Int32
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{
			Key: "feature.echo", Name: "feature_echo", InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
			executions.Add(1)
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: "feature-tool-1", Disposition: "committed"},
			}, nil
		}),
	}})
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	modelClient := &featureScenarioModel{}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: modelClient, Clock: scenarioClock{}, Catalog: registry, Executor: registry,
	})
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	snapshot, err := runner.StartRun(ctx, agent.StartRequest{
		ID: "eval-agent-feature", Actor: featureScenarioActor(), Thread: featureScenarioThread(),
		Goal: "use the echo tool once", Model: "fixture", ToolKeys: []string{"feature.echo"},
	})
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	return observeScenario(ctx, runtime, snapshot, []evaluation.EffectCount{
		{Key: "model_calls", Count: int(modelClient.calls.Load())},
		{Key: "tool_execution", Count: int(executions.Load())},
	})
}

type featureScenarioModel struct {
	calls atomic.Int32
}

func (client *featureScenarioModel) Generate(context.Context, model.Request) (model.Response, error) {
	call := client.calls.Add(1)
	if call == 1 {
		return model.Response{
			ToolCalls:  []tools.Call{{ID: "echo-call", ToolKey: "feature.echo", Arguments: json.RawMessage(`{}`)}},
			ResponseID: "feature-response-1",
		}, nil
	}
	return model.Response{Content: "done", ResponseID: "feature-response-2"}, nil
}

func executeWorkflowWaitScenario(ctx context.Context) (evaluation.ScenarioObservation, error) {
	runtime, err := newFeatureScenarioRuntime()
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
		ID: "feature-wait", Revision: 1, Name: "Feature wait",
		InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`),
		Nodes: []workflow.Node{
			{ID: "approval", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "approval", Payload: json.RawMessage(`{"prompt":"approve"}`)}},
			{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`{"done":true}`)}},
		},
	})
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	runner, err := workflow.NewRunner(workflow.Dependencies{
		Runtime: runtime, Effects: featureScenarioEffects{},
	})
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	waiting, startErr := runner.StartRun(ctx, workflow.StartRequest{
		ID: "eval-workflow-feature", Actor: featureScenarioActor(), Thread: featureScenarioThread(),
		Goal: "wait then finish", Definition: definition, Input: json.RawMessage(`{}`),
	})
	if startErr != nil || waiting.Run.Status != kernel.RunStatusWaitingInput {
		return evaluation.ScenarioObservation{}, errors.New("workflow did not enter wait")
	}
	completed, err := runner.ResolveWait(ctx, waiting.Run.ID, waiting.Run.Revision, json.RawMessage(`{"approved":true}`))
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	return observeScenario(ctx, runtime, completed, []evaluation.EffectCount{{Key: "wait_resolution", Count: 1}})
}

type featureScenarioEffects struct{}

func (featureScenarioEffects) Execute(context.Context, workflow.EffectRequest) (workflow.EffectResult, error) {
	return workflow.EffectResult{}, nil
}

func executeTeamChildScenario(ctx context.Context) (evaluation.ScenarioObservation, error) {
	runtime, err := newFeatureScenarioRuntime()
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	children := &featureScenarioChildren{snapshots: make(map[string]kernel.Snapshot)}
	coordinator, err := handoff.New(children)
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), scenarioClock{})
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	runner, err := team.NewRunner(team.Dependencies{
		Runtime: runtime, Handoffs: coordinator, Relations: relations, MaxMembers: 4,
	})
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	snapshot, err := runner.StartRun(ctx, team.StartRequest{
		ID: "eval-team-feature", Actor: featureScenarioActor(), Thread: featureScenarioThread(),
		Goal: "complete one child", Mode: team.ExecutionSequential,
		Members: []team.Member{{ID: "worker", Goal: "finish"}},
		Join:    handoff.Join{Mode: handoff.JoinAll, Quorum: 1, FailurePolicy: handoff.FailureCollect},
	})
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	items, err := relations.ListChildren(ctx, snapshot.Run.ID)
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	return observeScenario(ctx, runtime, snapshot, []evaluation.EffectCount{
		{Key: "child_start", Count: int(children.starts.Load())},
		{Key: "relation_count", Count: len(items)},
	})
}

type featureScenarioChildren struct {
	mu        sync.Mutex
	snapshots map[string]kernel.Snapshot
	starts    atomic.Int32
}

func (children *featureScenarioChildren) StartRun(_ context.Context, request agent.StartRequest) (kernel.Snapshot, error) {
	children.starts.Add(1)
	snapshot := kernel.Snapshot{
		Run: kernel.Run{
			ID: request.ID, Kind: agent.RunKind, Actor: request.Actor, Thread: request.Thread,
			Goal: request.Goal, Status: kernel.RunStatusCompleted, Revision: 2,
			CreatedAt: scenarioNow(), UpdatedAt: scenarioNow(),
		},
		State:     json.RawMessage(`{}`),
		Result:    &kernel.Result{ContentType: "text", Content: json.RawMessage(`"done"`)},
		EventHead: 2,
	}
	children.mu.Lock()
	children.snapshots[request.ID] = snapshot
	children.mu.Unlock()
	return snapshot, nil
}

func (children *featureScenarioChildren) LoadRun(_ context.Context, runID string) (kernel.Snapshot, error) {
	children.mu.Lock()
	defer children.mu.Unlock()
	snapshot, ok := children.snapshots[runID]
	if !ok {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	return snapshot, nil
}

func newFeatureScenarioRuntime() (*kernel.Runtime, error) {
	return kernel.New(kernel.Dependencies{
		Store: memory.NewStore(), Clock: scenarioClock{}, IDs: &featureScenarioIDs{},
	})
}

type featureScenarioIDs struct {
	next atomic.Uint64
}

func (ids *featureScenarioIDs) NewID(prefix string) (string, error) {
	return prefix + "-" + strconv.FormatUint(ids.next.Add(1), 10), nil
}

func featureScenarioActor() kernel.ActorRef {
	return kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
}

func featureScenarioThread() kernel.ThreadRef {
	return kernel.ThreadRef{Kind: "evaluation", ID: "thread"}
}

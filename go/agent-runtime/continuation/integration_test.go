package continuation_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runtimemodel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"

	continuationadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/continuation"
	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const integrationToolKey = "test.publish"

var errUnexpectedResumer = errors.New("unexpected resumer")

func TestApprovedChildAutomaticallyContinuesOwningTeamOnce(t *testing.T) {
	fixture := newContinuationIntegrationFixture(t)
	defer fixture.close(t)
	parent := fixture.startPendingTeam(t)
	child := fixture.waitingChild(t, parent)
	// Establish the fixture before exercising asynchronous continuation. This
	// test verifies approval wakeup and child-terminal propagation, not
	// concurrent inline parent startup.
	if err := fixture.worker.Start(fixture.workerContext); err != nil {
		t.Fatal(err)
	}
	fixture.approveChild(t, child)
	fixture.waitForCompletedParent(t, parent.Run.ID)
	fixture.assertSingleExecution(t)
}

type continuationIntegrationFixture struct {
	runtime       *kernel.Runtime
	agent         *agent.Runner
	teams         *team.Runner
	worker        *continuation.Worker
	workerContext context.Context
	cancel        context.CancelFunc
	executions    *atomic.Int32
	model         *approvalIntegrationModel
}

func newContinuationIntegrationFixture(t *testing.T) continuationIntegrationFixture {
	t.Helper()
	runtime, scheduler, relations, delivery := newIntegrationRuntime(t)
	executions := &atomic.Int32{}
	registry := newIntegrationToolRegistry(t, executions)
	agentRunner, model := newIntegrationAgent(t, runtime, registry)
	teamRunner := newIntegrationTeam(t, runtime, relations, agentRunner)
	worker := newIntegrationWorker(t, runtime, scheduler, delivery, agentRunner, teamRunner)
	workerCtx, cancel := context.WithCancel(context.Background())
	return continuationIntegrationFixture{
		runtime: runtime, agent: agentRunner, teams: teamRunner, worker: worker,
		cancel: cancel, workerContext: workerCtx, executions: executions, model: model,
	}
}

func newIntegrationRuntime(
	t *testing.T,
) (*kernel.Runtime, *continuation.Scheduler, *runrelation.Registry, *queuecore.Memory) {
	t.Helper()
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	delivery := queuecore.NewMemory(queuecore.Dependencies{})
	store := memory.NewStore()
	var runtime *kernel.Runtime
	scheduler, err := continuation.NewScheduler(continuation.SchedulerDependencies{
		Outbox: store, Queue: delivery, Relations: relations,
		Runs: continuation.LoaderFunc(func(ctx context.Context, runID string) (kernel.Snapshot, error) {
			return runtime.Load(ctx, runID)
		}),
		ProjectorID: "integration-projector",
	}, continuationadapter.Triggers()...)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err = kernel.New(kernel.Dependencies{Store: store, IDs: &atomicIDs{}})
	if err != nil {
		t.Fatal(err)
	}
	return runtime, scheduler, relations, delivery
}

func newIntegrationToolRegistry(t *testing.T, executions *atomic.Int32) *tools.Registry {
	t.Helper()
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{
			Key: integrationToolKey, Name: "test_publish",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
			executions.Add(1)
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"published":true}`),
				Receipt: tools.Receipt{ExecutionID: "publish-1", Disposition: "committed"},
			}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func newIntegrationAgent(
	t *testing.T,
	runtime *kernel.Runtime,
	registry *tools.Registry,
) (*agent.Runner, *approvalIntegrationModel) {
	t.Helper()
	approvals, err := interaction.New(runtime)
	if err != nil {
		t.Fatal(err)
	}
	model := &approvalIntegrationModel{}
	agentRunner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: model, Catalog: registry, Executor: registry,
		Approvals:        interactionadapter.New(approvals),
		ApprovalPolicies: []plugin.ApprovalPolicy{integrationApprovalPolicy{}},
		DeferResumption:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return agentRunner, model
}

type integrationApprovalPolicy struct{}

func (integrationApprovalPolicy) Name() string { return "integration-tool-approval" }

func (integrationApprovalPolicy) Approval(
	context.Context,
	plugin.ToolInvocation,
) (plugin.ApprovalRequirement, error) {
	return plugin.ApprovalRequired, nil
}

func newIntegrationTeam(
	t *testing.T,
	runtime *kernel.Runtime,
	relations *runrelation.Registry,
	agentRunner *agent.Runner,
) *team.Runner {
	t.Helper()
	coordinator, err := handoff.New(agentRunner)
	if err != nil {
		t.Fatal(err)
	}
	teamRunner, err := team.NewRunner(team.Dependencies{
		Runtime: runtime, Handoffs: coordinator, Relations: relations, MaxMembers: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	return teamRunner
}

func newIntegrationWorker(
	t *testing.T,
	runtime *kernel.Runtime,
	scheduler *continuation.Scheduler,
	delivery *queuecore.Memory,
	agentRunner *agent.Runner,
	teamRunner *team.Runner,
) *continuation.Worker {
	t.Helper()
	unused := unusedResumer{}
	dispatcher, err := continuation.NewDispatcher(
		runtime,
		continuation.RegisterResumer(agent.RunKind, agentRunner),
		continuation.RegisterResumer(team.RunKind, teamRunner),
		continuation.RegisterResumer(kernel.RunKind("unused_workflow"), unused),
	)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := continuation.NewWorker(delivery, dispatcher, continuation.WorkerOptions{
		WorkerID: "integration-worker", PollInterval: 5 * time.Millisecond,
		Projector:  scheduler,
		Reconciler: scheduler, ReconcileInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func (fixture continuationIntegrationFixture) startPendingTeam(t *testing.T) kernel.Snapshot {
	t.Helper()
	parent, err := fixture.teams.StartRun(t.Context(), team.StartRequest{
		ID: "parent-team", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "publish",
		Mode: team.ExecutionSequential,
		Members: []team.Member{{
			ID: "publisher", Goal: "publish once", ToolKeys: []string{integrationToolKey},
		}},
		Join: handoff.Join{Mode: handoff.JoinAll, Quorum: 1, FailurePolicy: handoff.FailureCollect},
	})
	if !errors.Is(err, team.ErrMemberPending) {
		t.Fatalf("start parent: %#v, %v", parent.Run, err)
	}
	return parent
}

func (fixture continuationIntegrationFixture) waitingChild(
	t *testing.T,
	parent kernel.Snapshot,
) kernel.Snapshot {
	t.Helper()
	view, err := team.ViewState(parent)
	if err != nil {
		t.Fatal(err)
	}
	childID := view.Members[0].Delegation.ChildRunID
	child, err := fixture.runtime.Load(t.Context(), childID)
	if err != nil || child.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("child=%#v err=%v", child.Run, err)
	}
	return child
}

func (fixture continuationIntegrationFixture) approveChild(t *testing.T, child kernel.Snapshot) {
	t.Helper()
	resolved, err := fixture.agent.ResolveApproval(t.Context(), child.Run.ID, child.Run.Revision,
		plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
	if err != nil || resolved.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("resolved=%#v err=%v", resolved.Run, err)
	}
}

func (fixture continuationIntegrationFixture) waitForCompletedParent(t *testing.T, runID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		parent, err := fixture.runtime.Load(t.Context(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if parent.Run.Status == kernel.RunStatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("parent did not continue: %#v", parent.Run)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (fixture continuationIntegrationFixture) assertSingleExecution(t *testing.T) {
	t.Helper()
	if fixture.executions.Load() != 1 || fixture.model.CallCount() != 2 {
		t.Fatalf("executions=%d modelCalls=%d", fixture.executions.Load(), fixture.model.CallCount())
	}
}

func (fixture continuationIntegrationFixture) close(t *testing.T) {
	t.Helper()
	fixture.cancel()
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := fixture.worker.Close(closeCtx); err != nil {
		t.Errorf("close worker: %v", err)
	}
}

type approvalIntegrationModel struct {
	mu    sync.Mutex
	calls int
}

func (model *approvalIntegrationModel) Generate(
	context.Context,
	runtimemodel.Request,
) (runtimemodel.Response, error) {
	model.mu.Lock()
	defer model.mu.Unlock()
	model.calls++
	if model.calls == 1 {
		return runtimemodel.Response{ToolCalls: []tools.Call{{
			ID: "publish-call", ToolKey: integrationToolKey, Arguments: json.RawMessage(`{}`),
		}}}, nil
	}
	return runtimemodel.Response{Content: "published"}, nil
}

func (model *approvalIntegrationModel) CallCount() int {
	model.mu.Lock()
	defer model.mu.Unlock()
	return model.calls
}

type unusedResumer struct{}

func (unusedResumer) Resume(context.Context, string, uint64) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, errUnexpectedResumer
}

type atomicIDs struct {
	next atomic.Uint64
}

func (ids *atomicIDs) NewID(prefix string) (string, error) {
	return prefix + "-" + strconv.FormatUint(ids.next.Add(1), 10), nil
}

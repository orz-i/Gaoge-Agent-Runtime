package continuation_test

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	continuationadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/continuation"
	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	runtimemodel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const integrationToolKey = "test.publish"

type integrationRuntimeStore interface {
	kernel.Store
	kernel.TransitionOutbox
}

func newIntegrationRuntime(
	t *testing.T,
	stores ...integrationRuntimeStore,
) (*kernel.Runtime, *continuation.Scheduler, *runrelation.Registry, *queuecore.Memory) {
	t.Helper()
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	delivery := queuecore.NewMemory(queuecore.Dependencies{})
	var store integrationRuntimeStore = memory.NewStore()
	if len(stores) > 1 {
		t.Fatal("at most one integration Store may be supplied")
	}
	if len(stores) == 1 {
		store = stores[0]
	}
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
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			executions.Add(1)
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"published":true}`),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"},
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

type atomicIDs struct {
	next atomic.Uint64
}

func (ids *atomicIDs) NewID(prefix string) (string, error) {
	return prefix + "-" + strconv.FormatUint(ids.next.Add(1), 10), nil
}

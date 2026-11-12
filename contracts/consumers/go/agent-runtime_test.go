package consumer

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// Keep host-implemented ports explicit: embedding the SDK interface would hide
// new required methods and would not protect external implementations.
type storeContract interface {
	Create(context.Context, kernel.Record, []kernel.EventDraft) (kernel.Snapshot, error)
	Load(context.Context, string) (kernel.Snapshot, error)
	Apply(context.Context, string, uint64, kernel.StoreMutation) (kernel.Snapshot, error)
	ListEvents(context.Context, string, int64, int) ([]kernel.Event, error)
	ClaimTransitions(context.Context, kernel.TransitionClaimRequest) ([]kernel.TransitionClaim, error)
	AckTransition(context.Context, kernel.TransitionLeaseRequest) error
	RetryTransition(context.Context, kernel.TransitionRetryRequest) error
}

type modelContract interface {
	Generate(context.Context, model.Request) (model.Response, error)
}

type catalogContract interface {
	Resolve(string) (tools.Definition, bool)
	List([]string) ([]tools.Definition, error)
}

type executorContract interface {
	Execute(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error)
}

type workerContract interface {
	Descriptor() kernel.FeatureDescriptor
	Start(context.Context) error
	Close(context.Context) error
}

var (
	_ kernel.Store                                        = (storeContract)(nil)
	_ storeContract                                       = (kernel.Store)(nil)
	_ model.Client                                        = (modelContract)(nil)
	_ modelContract                                       = (model.Client)(nil)
	_ tools.Catalog                                       = (catalogContract)(nil)
	_ catalogContract                                     = (tools.Catalog)(nil)
	_ tools.Executor                                      = (executorContract)(nil)
	_ executorContract                                    = (tools.Executor)(nil)
	_ kernel.WorkerFeature                                = (workerContract)(nil)
	_ workerContract                                      = (kernel.WorkerFeature)(nil)
	_ kernel.Store                                        = (*memory.Store)(nil)
	_ func(kernel.Dependencies) (*kernel.Runtime, error)  = kernel.New
	_ func(agent.Dependencies) (*agent.Runner, error)     = agent.NewRunner
	_ func([]tools.Registration) (*tools.Registry, error) = tools.NewRegistry
	_ continuation.ToolWaitResumer                        = (*agent.Runner)(nil)
)

// A separately linked Host implements only the narrow, immutable first-party
// AgentTask ownership proof. Missing methods must fail the external SDK build.
// External clients can see a pending original child Grant but cannot create
// the Agent's internal settlement proof. Store backends must check the opaque
// marker inside their own authoritative commit transaction.
type childPhysicalStoreContract interface {
	BeginChildExecution(context.Context, kernel.ChildExecutionGrant) error
	LookupChildExecution(context.Context, string) (kernel.ChildExecutionGrant, error)
	MarkChildExecutionSettled(context.Context, kernel.ChildExecutionGrant, kernel.ChildExecutionSettlement) error
	FinishChildExecution(context.Context, kernel.ChildExecutionGrant) error
}

var (
	_ kernel.ChildExecutionStore                                                                 = (childPhysicalStoreContract)(nil)
	_ childPhysicalStoreContract                                                                 = (kernel.ChildExecutionStore)(nil)
	_ childPhysicalStoreContract                                                                 = (*memory.Store)(nil)
	_ func(*kernel.Runtime, context.Context, string, uint64) (kernel.ChildExecutionGrant, error) = (*kernel.Runtime).BeginChildExecution
	_ func(*kernel.Runtime, context.Context, string) (kernel.ChildExecutionGrant, error)         = (*kernel.Runtime).LookupChildExecution
	_ func(*agent.Runner, context.Context, string) error                                         = (*agent.Runner).ReconcileChildPhysicalGrant
	_ func(*workflow.Runner, context.Context, string) error                                      = (*workflow.Runner).ReconcileChildPhysicalGrant
	_ func(*kernel.Runtime) *kernel.Runtime                                                      = (*kernel.Runtime).WithoutPhysicalGrant
	_ func([]byte) string                                                                        = agent.FrozenOriginFingerprint
	_ func(kernel.Snapshot, []byte) bool                                                         = agent.MatchesFrozenOrigin
	_                                                                                            = agent.Dependencies{
		ApprovalControlPreflight: func(context.Context, kernel.Snapshot) error { return nil },
		StartControlPreflight:    func(context.Context, agent.StartRequest) error { return nil },
	}
)

type workflowAgentTaskOwnerContract interface {
	LocalAgentTaskRunner() *agent.Runner
	LocalAgentTaskRecorder() runrelation.Recorder
	VerifyAgentTaskOrigin(workflow.Effect, kernel.Snapshot) bool
}

type workflowAgentTaskApprovalCancellationContract interface {
	FencesAgentTaskApprovalThroughHarness() bool
}

var (
	_ workflow.LocalAgentTaskOwner                                                                               = (workflowAgentTaskOwnerContract)(nil)
	_ workflowAgentTaskOwnerContract                                                                             = (workflow.LocalAgentTaskOwner)(nil)
	_ func(*agent.Runner, kernel.Snapshot, agent.StartRequest) bool                                              = (*agent.Runner).VerifyFrozenStartRequest
	_ func(kernel.Snapshot) bool                                                                                 = agent.TerminalPhysicalEffectsSettled
	_ workflow.LocalAgentTaskApprovalCancellationOwner                                                           = (workflowAgentTaskApprovalCancellationContract)(nil)
	_ workflowAgentTaskApprovalCancellationContract                                                              = (workflow.LocalAgentTaskApprovalCancellationOwner)(nil)
	_ func(*agent.Runner, *kernel.Runtime, kernel.Snapshot) bool                                                 = (*agent.Runner).QuiescentApprovalCancellation
	_ func(*workflow.Runner, *kernel.Runtime, *agent.Runner) bool                                                = (*workflow.Runner).UsesLocalAgentRunner
	_ func(*workflow.Runner, *kernel.Runtime, kernel.Snapshot, runrelation.Relation, kernel.Snapshot, bool) bool = (*workflow.Runner).VerifyLocalChild
)

type externalWaitContract interface {
	QueryPendingTool(context.Context, string, string) (*agent.PendingTool, error)
	NotifyExternalToolResultReady(context.Context, agent.ExternalToolResultSignal) (kernel.Snapshot, error)
}

var _ externalWaitContract = (*agent.Runner)(nil)

type hostModel struct {
	t     *testing.T
	calls int
}

func (host *hostModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	host.calls++
	if request.RunID != "consumer-run" || request.InvocationID == "" || len(request.Messages) == 0 {
		host.t.Fatalf("host did not receive durable model identity: %#v", request)
	}
	return model.Response{Content: "hello from the host"}, nil
}

func TestHostRunsAgentWithoutGaoge(t *testing.T) {
	ctx := context.Background()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	host := &hostModel{t: t}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: host})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.StartRun(ctx, agent.StartRequest{
		ID: "consumer-run", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "Say hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Run.Status != kernel.RunStatusCompleted || snapshot.Result == nil ||
		!json.Valid(snapshot.Result.Content) || host.calls != 1 {
		t.Fatalf("unexpected consumer result: %#v, model calls %d", snapshot, host.calls)
	}
	loaded, err := runtime.Load(ctx, snapshot.Run.ID)
	if err != nil || loaded.Run.Revision != snapshot.Run.Revision || loaded.EventHead == 0 {
		t.Fatalf("load durable result: %#v, %v", loaded, err)
	}
	descriptor := runner.Descriptor()
	if descriptor.Name == "" || len(descriptor.Requires) == 0 || len(descriptor.Provides) == 0 {
		t.Fatalf("incomplete feature descriptor: %#v", descriptor)
	}
}

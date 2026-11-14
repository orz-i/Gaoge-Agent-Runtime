package harnesspostgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	runtimepostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres"
	runtimesqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const ea1HACToolKey = "ea1HAC.read-evidence"

type ea1HACModel struct {
	runtime   *kernel.Runtime
	intents   []agent.ModelInvocation
	calls     int
	requests  []model.Request
	responses []model.Response
}

func (m *ea1HACModel) Generate(ctx context.Context, request model.Request) (response model.Response, err error) {
	defer func() { m.responses = append(m.responses, model.CloneResponse(response)) }()
	m.calls++
	m.requests = append(m.requests, model.CloneRequest(request))
	snapshot, e := m.runtime.Load(ctx, request.RunID)
	if e != nil {
		return model.Response{}, e
	}
	view, e := agent.ViewState(snapshot)
	if e != nil {
		return model.Response{}, e
	}
	found := false
	for _, inv := range view.ModelInvocations {
		if inv.ID == request.InvocationID {
			m.intents = append(m.intents, inv)
			found = true
			break
		}
	}
	if !found {
		return model.Response{}, errors.New("actual Model call lacks original durable intent")
	}

	if m.calls == 1 {
		return model.Response{ToolCalls: []tools.Call{{
			ID: "ea1HAC-call", ToolKey: ea1HACToolKey, Name: "read_evidence", Arguments: json.RawMessage(`{}`),
		}}}, nil
	}
	return model.Response{Content: "approved evidence"}, nil
}

type ea1HACAgentEffect struct {
	original       *agent.Runner
	relations      *runrelation.Registry
	approvalFenced bool
	startExpected  *agent.StartRequest
}

func (e *ea1HACAgentEffect) LocalAgentTaskRunner() *agent.Runner { return e.original }
func (e *ea1HACAgentEffect) LocalAgentTaskRecorder() runrelation.Recorder {
	return e.relations
}
func (e *ea1HACAgentEffect) FencesAgentTaskApprovalThroughHarness() bool {
	return e.approvalFenced
}
func (e *ea1HACAgentEffect) VerifyAgentTaskOrigin(effect workflow.Effect, child kernel.Snapshot) bool {
	var input struct {
		Goal string `json:"goal"`
	}
	return effect.Kind == "fixture.agent" && effect.Revision == "v1" &&
		agent.MatchesFrozenOrigin(child, effect.Input) &&
		json.Unmarshal(effect.Input, &input) == nil &&
		strings.TrimSpace(input.Goal) != "" && strings.TrimSpace(input.Goal) == child.Run.Goal
}
func (e *ea1HACAgentEffect) Execute(ctx context.Context, request workflow.EffectRequest) (workflow.EffectResult, error) {
	if request.Class != workflow.EffectClassAgent || request.Kind != "fixture.agent" || request.Revision != "v1" {
		return workflow.EffectResult{}, workflow.ErrEffectUnavailable
	}
	var input struct {
		Goal string `json:"goal"`
	}
	if err := json.Unmarshal(request.Input, &input); err != nil {
		return workflow.EffectResult{}, err
	}
	childID := request.EffectID
	if request.Attempt > 1 {
		childID = fmt.Sprintf("%s_attempt_%d", childID, request.Attempt)
	}
	if e.relations == nil || e.original == nil {
		return workflow.EffectResult{}, workflow.ErrInvalidExecution
	}
	if _, err := e.relations.Ensure(ctx, runrelation.Draft{
		ParentRunID: request.RunID, ChildRunID: childID,
		Kind: runrelation.KindWorkflowEffect, OwnerNodeID: request.OwnerNodeID,
	}); err != nil {
		return workflow.EffectResult{}, err
	}
	child, err := e.original.LoadRun(ctx, childID)
	if errors.Is(err, kernel.ErrNotFound) {
		originalStart := agent.StartRequest{
			ID: childID, Actor: request.Actor, Thread: request.Thread,
			RequestID: request.EffectID, ParentRunID: request.RunID,
			OriginFingerprint: agent.FrozenOriginFingerprint(request.Input),
			Goal:              input.Goal, Model: "fixture-model",
			ToolKeys: []string{ea1HACToolKey},
		}
		e.startExpected = &originalStart
		child, err = e.original.StartRun(ctx, originalStart)
	}
	if err != nil {
		return workflow.EffectResult{}, err
	}
	// A child ID collision cannot be reused as an unrelated effect.
	if child.Run.Kind != agent.RunKind || child.Run.ID != childID ||
		child.Run.Actor != request.Actor || child.Run.Thread != request.Thread ||
		child.Run.RequestID != request.EffectID || child.Run.Goal != input.Goal {
		return workflow.EffectResult{}, workflow.ErrInvalidExecution
	}
	if child.Run.Status == kernel.RunStatusCompleted && child.Result != nil {
		return workflow.EffectResult{Disposition: workflow.DispositionCompleted,
			ChildRunID: childID, ReceiptID: childID, Output: child.Result.Content}, nil
	}
	return workflow.EffectResult{Disposition: workflow.DispositionPending, ChildRunID: childID}, nil
}

type ea1HACFixture struct {
	kernelBackend                      ea1HACKernelBackend
	native, deferResume, observeHost   bool
	definitions                        *workflow.DefinitionRegistry
	approvalBefore, hostBefore         func()
	hostCalls, approvalCalls           atomic.Int32
	approvalOriginal, approvalResolved *kernel.Checkpoint
	runtime                            *kernel.Runtime
	store                              harness.Store
	backend                            ea1CancelHostFixture
	physical                           *ea1HACKernelStore
	toolUnknown                        bool
	toolRequest                        tools.ExecutionRequest
	toolResult                         tools.ExecutionResult
	budgets                            *harness.BudgetMiddleware
	agent                              *agent.Runner
	workflow                           *workflow.Runner
	harness                            *harness.Runner
	relations                          *runrelation.Registry
	model                              *ea1HACModel
	toolCalls                          int
	toolStarted                        chan struct{}
	toolRelease                        chan struct{}
	request                            harness.WorkflowTurnRequest
	executor                           *ea1HACAgentEffect
}

func newEA1HACFixture(t *testing.T, backend string, persistent bool) *ea1HACFixture {
	return newEA1HACFixtureWithOptions(t, backend, persistent, false, true)
}
func newEA1HACFixtureWithOptions(t *testing.T, backend string, persistent, native, deferResume bool) *ea1HACFixture {
	return newEA1HACFixtureWithHostOptions(t, backend, persistent, native, deferResume, true)
}

// observeHost is frozen before Create and retained through reopening. The
// no-middleware variant still uses actual Harness physical approval grants,
// frozen approval policy, original AgentTask route, cancellation and budgets.
func newEA1HACFixtureWithHostOptions(t *testing.T, backend string, persistent, native, deferResume, observeHost bool) *ea1HACFixture {
	t.Helper()
	k := ea1HACBackend(t, backend)
	h := ea1CancelHostBackend(t, persistent)
	f := &ea1HACFixture{kernelBackend: k, backend: h, store: h.host, model: &ea1HACModel{}, native: native, deferResume: deferResume, observeHost: observeHost}
	f.physical = &ea1HACKernelStore{ea1HACPortSet: k.store}
	f.bind(t)
	return f
}
func (f *ea1HACFixture) bind(t *testing.T) {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: f.physical})
	if err != nil {
		t.Fatal(err)
	}
	f.runtime = runtime
	f.model.runtime = runtime
	relations, err := runrelation.New(f.kernelBackend.relations, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.relations = relations
	store, backend := f.store, f.backend
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: ea1HACToolKey, Name: "read_evidence",
			InputSchema: json.RawMessage(`{"type":"object"}`), Terminal: true},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			f.toolCalls++
			f.toolRequest = request
			f.toolRequest.Call.Arguments = append(json.RawMessage(nil), request.Call.Arguments...)
			if f.toolStarted != nil {
				close(f.toolStarted)
				<-f.toolRelease
			}
			if f.toolUnknown {
				return tools.ExecutionResult{}, errors.New("ea1 actual Tool request sent; response unavailable")
			}
			result := tools.ExecutionResult{
				Content: json.RawMessage(`{"evidence":"approved"}`),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "read"},
			}
			f.toolResult = result
			f.toolResult.Content = append(json.RawMessage(nil), result.Content...)
			return result, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	interactionService, err := interaction.New(runtime)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := harness.NewFrozenApprovalPolicy(store, relations)
	if err != nil {
		t.Fatal(err)
	}
	dependencies := agent.Dependencies{
		StartControlPreflight: func(ctx context.Context, req agent.StartRequest) error {
			if f.executor == nil || f.executor.startExpected == nil || !reflect.DeepEqual(req, *f.executor.startExpected) {
				return agent.ErrInvalidRequest
			}
			rel, e := relations.GetByChild(ctx, req.ID)
			if e != nil || rel.ParentRunID != req.ParentRunID || rel.ChildRunID != req.ID || rel.Kind != runrelation.KindWorkflowEffect {
				return agent.ErrInvalidRequest
			}
			return nil
		},
		Runtime: runtime, Model: f.model, Catalog: registry, Executor: registry,
		Approvals:        &ea1HACApproval{ApprovalHandler: interactionadapter.New(interactionService), f: f},
		ApprovalPolicies: []plugin.ApprovalPolicy{policy},
		ApprovalControlPreflight: func(ctx context.Context, snapshot kernel.Snapshot) error {
			if snapshot.Checkpoint == nil || !harness.VerifiedSubtaskApprovalDispatch(ctx, snapshot.Run.ID, snapshot.Run.Revision, snapshot.Checkpoint.ID) {
				return harness.ErrConflict
			}
			return nil
		},
		DeferResumption: f.deferResume,
	}
	if f.observeHost {
		dependencies.RunMiddleware = []plugin.RunMiddleware{&ea1HACHost{f: f}}
	}
	if f.native {
		dependencies.RunExecution = &agent.RunExecution{OwnerID: "ea1-hac-original", LeaseDuration: time.Hour}
	}
	f.agent, err = agent.NewRunner(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	// Certify the actual composed graph, not a fixture boolean pretending to
	// be cancellation authority. The ordinary middleware variant is opaque.
	if f.agent.OwnsKernelRuntime(runtime) == f.observeHost {
		t.Fatal("fixture cancellation ownership does not match actual middleware composition")
	}
	f.executor = &ea1HACAgentEffect{original: f.agent, relations: relations, approvalFenced: true}
	static, err := workflow.NewStaticEffectRouter(nil, workflow.EffectRoute{
		Class: workflow.EffectClassAgent, Executor: f.executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.definitions == nil {
		f.definitions, err = workflow.NewDefinitionRegistry(workflow.NewMemoryDefinitionStore(), ea1CancelClock{})
	}
	definitions := f.definitions
	if err != nil {
		t.Fatal(err)
	}
	f.workflow, err = workflow.NewRunner(workflow.Dependencies{
		Runtime: runtime, Effects: static, Registry: definitions, Relations: relations, DeferResumption: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.budgets, err = harness.NewBudgetMiddleware(harness.BudgetMiddlewareDependencies{Store: store, Ledgers: backend.ledgers, Clock: ea1CancelClock{}})
	if err != nil {
		t.Fatal(err)
	}
	f.harness, err = harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: f.agent, Workflows: f.workflow, Store: store, Relations: relations,
		Clock: ea1CancelClock{}, Budget: f.budgets,
	})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
		ID: "ea1HAC", Revision: 1, Name: "AgentTask child approval",
		Nodes: []workflow.Node{
			{ID: "agent-node", Type: workflow.NodeAgentTask, Next: "done",
				AgentTask: &workflow.AgentTaskNode{AgentKey: "fixture.agent", Revision: "v1",
					Input: workflow.ValueSource{Kind: workflow.ValueLiteral, Value: json.RawMessage(`{"goal":"approved search"}`)},
					Retry: workflow.RetryPolicy{MaxAttempts: 1}}},
			{ID: "done", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`{"done":true}`)}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := harness.StartRequest{HostThread: harness.HostRef{Kind: "conversation"}, HostTurn: harness.HostRef{Kind: "conversation_turn"}, Actor: kernel.ActorRef{TenantID: "ea1-tenant", ActorID: "ea1-actor"}, Thread: kernel.ThreadRef{Kind: "conversation"}}
	request.HostThread.ID = "ea1HAC-thread"
	request.Thread.ID = "ea1HAC-thread"
	request.HostTurn.ID = "ea1HAC-turn"
	request.RequestID = "ea1HAC-request"
	request.Goal = "run approved subtask"
	request.Config.Model = "fixture-model"
	request.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 10, MaxTotalTokens: 1000}
	request.Config.ToolKeys = []string{ea1HACToolKey}
	request.Config.ToolPolicies = []harness.ToolPolicySnapshot{{
		Key: ea1HACToolKey, DefinitionVersion: "v1", ApprovalCapability: "per_call", ApprovalMode: "always",
	}}
	f.request = harness.WorkflowTurnRequest{StartRequest: request, Definition: definition, Input: json.RawMessage(`{}`)}
}

// Configuration explicitly selects default-child or native lease authority.
// All Store ports remain available; faults target the actual configured port.
type ea1HACKernelStore struct {
	*ea1HACPortSet
	beforeApply          func(context.Context, string, uint64, kernel.StoreMutation) error
	beforeApplyExecution func(context.Context, kernel.ExecutionLease, uint64, kernel.StoreMutation) error
	controlCalls         atomic.Int32
	target               string
	poison               func(kernel.Snapshot) kernel.Snapshot
	markArmed            atomic.Bool
	markHits             atomic.Int32
	markEntered          chan kernel.ChildExecutionGrant
	markRelease          chan struct{}
}

func (s *ea1HACKernelStore) Load(ctx context.Context, id string) (kernel.Snapshot, error) {
	snap, err := s.Store.Load(ctx, id)
	if err == nil && id == s.target && s.poison != nil {
		snap = s.poison(snap)
	}
	return snap, err
}
func (s *ea1HACKernelStore) ApplyControl(ctx context.Context, id string, rev uint64, g kernel.ControlGuard, m kernel.StoreMutation) (kernel.Snapshot, bool, error) {
	if g.Kind == kernel.ControlCancel {
		s.controlCalls.Add(1)
	}
	return s.ControlStore.ApplyControl(ctx, id, rev, g, m)
}
func (s *ea1HACKernelStore) MarkChildExecutionSettled(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	if g.ChildRunID == s.target && s.markArmed.CompareAndSwap(true, false) {
		s.markHits.Add(1)
		s.markEntered <- g
		select {
		case <-s.markRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.ChildExecutionStore.MarkChildExecutionSettled(ctx, g, p)
}

func ea1HACStart(t *testing.T, f *ea1HACFixture) (harness.Snapshot, harness.Subtask, harness.Invocation, budget.Ledger) {
	t.Helper()
	waiting, err := f.harness.StartWorkflowTurn(t.Context(), f.request)
	if err != nil {
		t.Fatalf("start original Workflow AgentTask: %v", err)
	}
	if len(waiting.Subtasks) != 1 || waiting.Subtasks[0].Approval == nil || f.model.calls != 1 || f.toolCalls != 0 {
		t.Fatalf("actual original AgentTask did not wait for approval: %+v", waiting)
	}
	root, ok := harness.TopLevelInvocation(waiting)
	if !ok {
		t.Fatal("missing original Workflow Invocation")
	}
	persistedOwner, err := f.store.GetInvocation(t.Context(), root.ID)
	if err != nil {
		t.Fatalf("load original persisted Workflow Invocation baseline: %v", err)
	}
	projectionComparison, persistedComparison := root, persistedOwner
	// The projection can clone [] to nil. Normalize only two genuinely
	// empty OutputRefs values on comparison copies; never discard an output.
	if len(projectionComparison.OutputRefs) == 0 && len(persistedComparison.OutputRefs) == 0 {
		projectionComparison.OutputRefs, persistedComparison.OutputRefs = nil, nil
	}
	if !reflect.DeepEqual(projectionComparison, persistedComparison) {
		t.Fatalf("original Workflow projection differs from persisted owner: projection=%+v persisted=%+v", root, persistedOwner)
	}
	root = persistedOwner
	task := waiting.Subtasks[0]
	c := budget.Coordinator{Store: f.backend.ledgers}
	// ExecutionPolicy tightens the submitted limits before sealing Config.
	// Compare the ledger against the actual immutable Turn-linked source,
	// not the pre-normalization request (which omits default child/concurrency caps).
	persistedTurn, err := f.store.GetTurn(t.Context(), waiting.Turn.ID)
	if err != nil || persistedTurn.ID != waiting.Turn.ID || persistedTurn.ConfigSnapshotID != waiting.Turn.ConfigSnapshotID {
		t.Fatalf("load original Turn budget source: %v", err)
	}
	frozenConfig, err := f.store.GetConfigSnapshot(t.Context(), persistedTurn.ConfigSnapshotID)
	if err != nil || frozenConfig.ID != persistedTurn.ConfigSnapshotID || frozenConfig.TurnID != persistedTurn.ID || frozenConfig.SharedBudget == nil {
		t.Fatalf("load frozen effective Config budget source: %v", err)
	}
	initial, err := f.backend.ledgers.LoadBudget(t.Context(), waiting.Turn.ID)
	if err != nil || initial.ID != persistedTurn.ID || initial.Limits != *frozenConfig.SharedBudget || len(initial.Runs) != 0 {
		t.Fatalf("initial Host ledger/source before explicit registration: ledger=%+v effective=%+v err=%v", initial, frozenConfig.SharedBudget, err)
	}
	registered, err := c.RegisterRun(t.Context(), waiting.Turn.ID, root.ExecutionRefID, budget.RunBudget{})
	if err != nil {
		t.Fatalf("register original Workflow root %s: %v", root.ExecutionRefID, err)
	}
	binding, exists := registered.Runs[root.ExecutionRefID]
	if !exists || binding != (budget.RunBudget{}) || registered.Limits != initial.Limits {
		t.Fatal("root registration changed frozen limits/binding")
	}
	// Like BudgetMiddleware.registerAncestry, bindings add only the parent;
	// effective global caps stay in Ledger.Limits, not duplicate local overrides.
	childBinding := budget.RunBudget{ParentRunID: root.ExecutionRefID}
	registered, err = c.RegisterRun(t.Context(), waiting.Turn.ID, task.RunID, childBinding)
	if err != nil {
		t.Fatalf("register original AgentTask child %s under %s: %v", task.RunID, root.ExecutionRefID, err)
	}
	if registered.Runs[task.RunID] != childBinding || registered.Runs[root.ExecutionRefID] != binding || registered.Limits != *frozenConfig.SharedBudget {
		t.Fatal("child registration changed original ancestry/effective budget source")
	}
	for _, id := range []string{"child-unsent", "child-dispatched"} {
		if _, err = c.Reserve(t.Context(), waiting.Turn.ID, budget.Reservation{ID: id, RunID: task.RunID, RequestHash: "original-" + id, Requested: budget.Usage{LLMCalls: 1, InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}, true); err != nil {
			t.Fatalf("reserve original child request %s: %v", id, err)
		}
	}
	if _, err = c.Dispatch(t.Context(), waiting.Turn.ID, "child-dispatched"); err != nil {
		t.Fatalf("dispatch original child reservation: %v", err)
	}
	ledger, err := f.backend.ledgers.LoadBudget(t.Context(), waiting.Turn.ID)
	if err != nil {
		t.Fatalf("load seeded cancellation budget: %v", err)
	}
	f.physical.target = task.RunID
	return waiting, task, root, ledger
}
func ea1HACRunner(t *testing.T, f *ea1HACFixture, h harness.Store, l budget.LedgerStore, relations harness.RunRelationSource) *harness.Runner {
	t.Helper()
	b, err := harness.NewBudgetMiddleware(harness.BudgetMiddlewareDependencies{Store: h, Ledgers: l, Clock: ea1CancelClock{}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := harness.NewRunner(harness.Dependencies{Runtime: f.runtime, Agent: f.agent, Workflows: f.workflow, Store: h, Relations: relations, Clock: ea1CancelClock{}, Budget: b})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func ea1HACLoad(t *testing.T, f *ea1HACFixture, id string) kernel.Snapshot {
	t.Helper()
	v, err := f.physical.Store.Load(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func ea1HACBudget(t *testing.T, l budget.LedgerStore, id string) budget.Ledger {
	t.Helper()
	v, err := l.LoadBudget(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func ea1HACItems(t *testing.T, h harness.Store, id string) []harness.Item {
	t.Helper()
	v, err := h.ListItems(t.Context(), id, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func ea1HACExpectedBudget(before budget.Ledger, child string) budget.Ledger {
	out := budget.CloneLedger(before)
	out.Revision++
	if out.CancelledRuns == nil {
		out.CancelledRuns = map[string]bool{}
	}
	out.CancelledRuns[child] = true
	for id, v := range out.Reservations {
		if v.RunID == child && !v.Dispatched && v.Status != budget.ReservationSettled {
			v.Status = budget.ReservationReleased
			v.Active = false
			out.Reservations[id] = v
		}
	}
	return out
}
func ea1HACAssertOwner(t *testing.T, f *ea1HACFixture, root harness.Invocation, parent, child kernel.Snapshot) {
	t.Helper()
	if !reflect.DeepEqual(parent, ea1HACLoad(t, f, parent.Run.ID)) || !reflect.DeepEqual(child, ea1HACLoad(t, f, child.Run.ID)) {
		t.Fatal("cancellation changed original Kernel parent/child snapshot")
	}
	got, err := f.store.GetInvocation(t.Context(), root.ID)
	if err != nil || !reflect.DeepEqual(got, root) {
		wantFields, gotFields := reflect.ValueOf(root), reflect.ValueOf(got)
		for index := 0; index < wantFields.NumField(); index++ {
			want, actual := wantFields.Field(index).Interface(), gotFields.Field(index).Interface()
			if reflect.DeepEqual(want, actual) {
				continue
			}
			name := wantFields.Type().Field(index).Name
			t.Logf("Workflow Invocation field %s: expected=%#v actual=%#v", name, want, actual)
			if wantTime, ok := want.(time.Time); ok {
				actualTime := actual.(time.Time)
				t.Logf("Workflow Invocation time %s: Equal=%t expectedLocation=%q(%p) actualLocation=%q(%p) UTCDeepEqual=%t", name, wantTime.Equal(actualTime), wantTime.Location().String(), wantTime.Location(), actualTime.Location().String(), actualTime.Location(), reflect.DeepEqual(wantTime.UTC(), actualTime.UTC()))
			}
		}
		t.Fatalf("cancellation changed original Workflow Invocation: %v", err)
	}
}
func ea1HACApprove(t *testing.T, f *ea1HACFixture, waiting harness.Snapshot, task harness.Subtask) kernel.Snapshot {
	t.Helper()
	_, err := f.harness.ResolveSubtaskApproval(t.Context(), waiting.Turn.ID, task.ID, task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove})
	if err != nil {
		t.Fatal(err)
	}
	child := ea1HACLoad(t, f, task.RunID)
	if child.Run.Status != kernel.RunStatusRunning || f.toolCalls != 0 {
		t.Fatal("explicit approval did not preserve deferred Tool boundary")
	}
	return child
}

// Every optional Kernel authority port is preserved, including default-child
// ordinary Apply and explicit-native ApplyExecution; neither is substituted.
type ea1HACPortSet struct {
	kernel.Store
	kernel.ExecutionStore
	kernel.ControlStore
	kernel.ChildExecutionStore
	kernel.OwnedPhysicalExecutionStore
	kernel.OwnedRetirementStore
	kernel.ExecutionQuarantineStore
}

func ea1HACPorts(s kernel.Store) *ea1HACPortSet {
	return &ea1HACPortSet{s, s.(kernel.ExecutionStore), s.(kernel.ControlStore), s.(kernel.ChildExecutionStore), s.(kernel.OwnedPhysicalExecutionStore), s.(kernel.OwnedRetirementStore), s.(kernel.ExecutionQuarantineStore)}
}

type ea1HACKernelBackend struct {
	store, peer *ea1HACPortSet
	relations   runrelation.Store
	reopen      func() (*ea1HACPortSet, runrelation.Store)
}

func ea1HACBackend(t *testing.T, name string) ea1HACKernelBackend {
	t.Helper()
	switch name {
	case "memory":
		s := ea1HACPorts(memory.NewStore())
		r := memory.NewRunRelationStore()
		return ea1HACKernelBackend{s, s, r, func() (*ea1HACPortSet, runrelation.Store) { return s, r }}
	case "sqlite":
		dir := t.TempDir()
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "kernel.db")
		s, err := runtimesqlite.CreateNew(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		peer, err := runtimesqlite.OpenExisting(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close(); _ = peer.Close() })
		return ea1HACKernelBackend{ea1HACPorts(s), ea1HACPorts(peer), runtimesqlite.NewRunRelationStore(s), func() (*ea1HACPortSet, runrelation.Store) {
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if err := peer.Close(); err != nil {
				t.Fatal(err)
			}
			fresh, err := runtimesqlite.OpenExisting(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = fresh.Close() })
			return ea1HACPorts(fresh), runtimesqlite.NewRunRelationStore(fresh)
		}}
	case "postgres":
		dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
		if dsn == "" {
			t.Skip("TEST_POSTGRES_DSN is not configured")
		}
		db, isolated := openIsolatedHarnessPostgres(t, dsn)
		if err := runtimepostgres.Migrate(db); err != nil {
			t.Fatal(err)
		}
		peer, err := gorm.Open(pgdriver.Open(isolated), realHarnessPostgresTestConfig())
		if err != nil {
			t.Fatal(err)
		}
		oldPool, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		peerPool, err := peer.DB()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = peerPool.Close() })
		return ea1HACKernelBackend{ea1HACPorts(runtimepostgres.NewKernelStore(db)), ea1HACPorts(runtimepostgres.NewKernelStore(peer)), runtimepostgres.NewRunRelationStore(db), func() (*ea1HACPortSet, runrelation.Store) {
			if err := oldPool.Close(); err != nil {
				t.Fatal(err)
			}
			if err := peerPool.Close(); err != nil {
				t.Fatal(err)
			}
			fresh, err := gorm.Open(pgdriver.Open(isolated), realHarnessPostgresTestConfig())
			if err != nil {
				t.Fatal(err)
			}
			pool, err := fresh.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = pool.Close() })
			return ea1HACPorts(runtimepostgres.NewKernelStore(fresh)), runtimepostgres.NewRunRelationStore(fresh)
		}}
	default:
		t.Fatalf("unknown Kernel backend %q", name)
		return ea1HACKernelBackend{}
	}
}
func (s *ea1HACKernelStore) Apply(ctx context.Context, id string, rev uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	if s.beforeApply != nil {
		if err := s.beforeApply(ctx, id, rev, m); err != nil {
			return kernel.Snapshot{}, err
		}
	}
	return s.Store.Apply(ctx, id, rev, m)
}
func (s *ea1HACKernelStore) ApplyExecution(ctx context.Context, l kernel.ExecutionLease, rev uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	if s.beforeApplyExecution != nil {
		if err := s.beforeApplyExecution(ctx, l, rev, m); err != nil {
			return kernel.Snapshot{}, err
		}
	}
	return s.ExecutionStore.ApplyExecution(ctx, l, rev, m)
}

type ea1HACApproval struct {
	plugin.ApprovalHandler
	f *ea1HACFixture
}

func (a *ea1HACApproval) ResolveToolApproval(cp *kernel.Checkpoint, r plugin.ApprovalResponse) (*kernel.Checkpoint, error) {
	a.f.approvalCalls.Add(1)
	a.f.approvalOriginal = ea1HACCloneCheckpoint(cp)
	if a.f.approvalBefore != nil {
		a.f.approvalBefore()
	}
	resolved, err := a.ApprovalHandler.ResolveToolApproval(cp, r)
	a.f.approvalResolved = ea1HACCloneCheckpoint(resolved)
	return resolved, err
}
func ea1HACCloneCheckpoint(cp *kernel.Checkpoint) *kernel.Checkpoint {
	if cp == nil {
		return nil
	}
	raw, err := json.Marshal(cp)
	if err != nil {
		panic(err)
	}
	var out kernel.Checkpoint
	if err = json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return &out
}

type ea1HACHost struct{ f *ea1HACFixture }

func (*ea1HACHost) Name() string { return "ea1-hac-observed-host" }
func (h *ea1HACHost) Run(ctx context.Context, in plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	if in.Operation == plugin.RunResolveApproval {
		h.f.hostCalls.Add(1)
		if h.f.hostBefore != nil {
			h.f.hostBefore()
		}
	}
	return next(ctx)
}

// Only immutable compiled definitions and the test's external-call recorder are
// retained. Persistent Kernel/relations and Host pools are closed and reopened;
// every Runtime/Agent/Workflow/Harness execution owner is newly constructed.
func ea1HACReopen(t *testing.T, f *ea1HACFixture) {
	t.Helper()
	h, l := f.backend.reopen()
	s, r := f.kernelBackend.reopen()
	f.store = h
	f.backend.host = h
	f.backend.ledgers = l
	f.kernelBackend.store = s
	f.kernelBackend.relations = r
	f.physical.ea1HACPortSet = s
	f.physical.poison = nil
	f.physical.beforeApply = nil
	f.physical.beforeApplyExecution = nil
	f.hostBefore = nil
	f.approvalBefore = nil
	f.bind(t)
}
func ea1HACExpectedToolRequest(childID string) tools.ExecutionRequest {
	return tools.ExecutionRequest{RunID: childID, Call: tools.Call{ID: "ea1HAC-call", ToolKey: ea1HACToolKey, Name: "read_evidence", Arguments: json.RawMessage(`{}`)}}
}

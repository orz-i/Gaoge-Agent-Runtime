package harnesspostgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
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
)

const ea1ChildCancelToolKey = "ea1ChildCancel.read-evidence"

type ea1ChildCancelModel struct{ calls int }

func (m *ea1ChildCancelModel) Generate(_ context.Context, _ model.Request) (model.Response, error) {
	m.calls++
	if m.calls == 1 {
		return model.Response{ToolCalls: []tools.Call{{
			ID: "ea1ChildCancel-call", ToolKey: ea1ChildCancelToolKey, Name: "read_evidence", Arguments: json.RawMessage(`{}`),
		}}}, nil
	}
	return model.Response{Content: "approved evidence"}, nil
}

type ea1ChildCancelAgentEffect struct {
	original       *agent.Runner
	relations      *runrelation.Registry
	approvalFenced bool
}

func (e *ea1ChildCancelAgentEffect) LocalAgentTaskRunner() *agent.Runner { return e.original }
func (e *ea1ChildCancelAgentEffect) LocalAgentTaskRecorder() runrelation.Recorder {
	return e.relations
}
func (e *ea1ChildCancelAgentEffect) FencesAgentTaskApprovalThroughHarness() bool {
	return e.approvalFenced
}
func (e *ea1ChildCancelAgentEffect) VerifyAgentTaskOrigin(effect workflow.Effect, child kernel.Snapshot) bool {
	var input struct {
		Goal string `json:"goal"`
	}
	return effect.Kind == "fixture.agent" && effect.Revision == "v1" &&
		agent.MatchesFrozenOrigin(child, effect.Input) &&
		json.Unmarshal(effect.Input, &input) == nil &&
		strings.TrimSpace(input.Goal) != "" && strings.TrimSpace(input.Goal) == child.Run.Goal
}
func (e *ea1ChildCancelAgentEffect) Execute(ctx context.Context, request workflow.EffectRequest) (workflow.EffectResult, error) {
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
		child, err = e.original.StartRun(ctx, agent.StartRequest{
			ID: childID, Actor: request.Actor, Thread: request.Thread,
			RequestID: request.EffectID, ParentRunID: request.RunID,
			OriginFingerprint: agent.FrozenOriginFingerprint(request.Input),
			Goal:              input.Goal, Model: "fixture-model",
			ToolKeys: []string{ea1ChildCancelToolKey},
		})
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

type ea1ChildCancelHarnessFixture struct {
	runtime     *kernel.Runtime
	store       harness.Store
	backend     ea1CancelHostFixture
	physical    *ea1ChildCancelKernelStore
	toolUnknown bool
	toolRequest tools.ExecutionRequest
	toolResult  tools.ExecutionResult
	budgets     *harness.BudgetMiddleware
	agent       *agent.Runner
	workflow    *workflow.Runner
	harness     *harness.Runner
	relations   *runrelation.Registry
	model       *ea1ChildCancelModel
	toolCalls   int
	toolStarted chan struct{}
	toolRelease chan struct{}
	request     harness.WorkflowTurnRequest
	executor    *ea1ChildCancelAgentEffect
}

func newEA1ChildCancelHarnessFixture(t *testing.T, persistent bool) *ea1ChildCancelHarnessFixture {
	t.Helper()
	physical := &ea1ChildCancelKernelStore{Store: memory.NewStore()}
	runtime, err := kernel.New(kernel.Dependencies{Store: physical})
	if err != nil {
		t.Fatal(err)
	}
	backend := ea1CancelHostBackend(t, persistent)
	store := backend.host
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	f := &ea1ChildCancelHarnessFixture{runtime: runtime, store: store, relations: relations, model: &ea1ChildCancelModel{}, physical: physical, backend: backend}
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: ea1ChildCancelToolKey, Name: "read_evidence",
			InputSchema: json.RawMessage(`{"type":"object"}`), Terminal: true},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			if f.toolStarted != nil {
				close(f.toolStarted)
				<-f.toolRelease
			}
			f.toolCalls++
			f.toolRequest = request
			f.toolRequest.Call.Arguments = append(json.RawMessage(nil), request.Call.Arguments...)
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
		Runtime: runtime, Model: f.model, Catalog: registry, Executor: registry,
		Approvals:        interactionadapter.New(interactionService),
		ApprovalPolicies: []plugin.ApprovalPolicy{policy},
		ApprovalControlPreflight: func(ctx context.Context, snapshot kernel.Snapshot) error {
			if snapshot.Checkpoint == nil || !harness.VerifiedSubtaskApprovalDispatch(ctx, snapshot.Run.ID, snapshot.Run.Revision, snapshot.Checkpoint.ID) {
				return harness.ErrConflict
			}
			return nil
		},
		DeferResumption: true,
	}
	f.agent, err = agent.NewRunner(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	f.executor = &ea1ChildCancelAgentEffect{original: f.agent, relations: relations, approvalFenced: true}
	static, err := workflow.NewStaticEffectRouter(nil, workflow.EffectRoute{
		Class: workflow.EffectClassAgent, Executor: f.executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := workflow.NewDefinitionRegistry(workflow.NewMemoryDefinitionStore(), ea1CancelClock{})
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
		ID: "ea1ChildCancel", Revision: 1, Name: "AgentTask child approval",
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
	request.HostThread.ID = "ea1ChildCancel-thread"
	request.Thread.ID = "ea1ChildCancel-thread"
	request.HostTurn.ID = "ea1ChildCancel-turn"
	request.RequestID = "ea1ChildCancel-request"
	request.Goal = "run approved subtask"
	request.Config.Model = "fixture-model"
	request.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 10, MaxTotalTokens: 1000}
	request.Config.ToolKeys = []string{ea1ChildCancelToolKey}
	request.Config.ToolPolicies = []harness.ToolPolicySnapshot{{
		Key: ea1ChildCancelToolKey, DefinitionVersion: "v1", ApprovalCapability: "per_call", ApprovalMode: "always",
	}}
	f.request = harness.WorkflowTurnRequest{StartRequest: request, Definition: definition, Input: json.RawMessage(`{}`)}
	return f
}

// No native ExecutionLease is configured in this default-child fixture.
// Embedding Memory faithfully exposes all Store ports; only the exact child
// Mark and actual ApplyControl ports below are observed, never ApplyExecution.
type ea1ChildCancelKernelStore struct {
	*memory.Store
	controlCalls atomic.Int32
	target       string
	poison       func(kernel.Snapshot) kernel.Snapshot
	markArmed    atomic.Bool
	markHits     atomic.Int32
	markEntered  chan kernel.ChildExecutionGrant
	markRelease  chan struct{}
}

func (s *ea1ChildCancelKernelStore) Load(ctx context.Context, id string) (kernel.Snapshot, error) {
	snap, err := s.Store.Load(ctx, id)
	if err == nil && id == s.target && s.poison != nil {
		snap = s.poison(snap)
	}
	return snap, err
}
func (s *ea1ChildCancelKernelStore) ApplyControl(ctx context.Context, id string, rev uint64, g kernel.ControlGuard, m kernel.StoreMutation) (kernel.Snapshot, bool, error) {
	if g.Kind == kernel.ControlCancel {
		s.controlCalls.Add(1)
	}
	return s.Store.ApplyControl(ctx, id, rev, g, m)
}
func (s *ea1ChildCancelKernelStore) MarkChildExecutionSettled(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	if g.ChildRunID == s.target && s.markArmed.CompareAndSwap(true, false) {
		s.markHits.Add(1)
		s.markEntered <- g
		select {
		case <-s.markRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.Store.MarkChildExecutionSettled(ctx, g, p)
}

func ea1ChildCancelStart(t *testing.T, f *ea1ChildCancelHarnessFixture) (harness.Snapshot, harness.Subtask, harness.Invocation, budget.Ledger) {
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
func ea1ChildCancelRunner(t *testing.T, f *ea1ChildCancelHarnessFixture, h harness.Store, l budget.LedgerStore, relations harness.RunRelationSource) *harness.Runner {
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
func ea1ChildCancelLoad(t *testing.T, f *ea1ChildCancelHarnessFixture, id string) kernel.Snapshot {
	t.Helper()
	v, err := f.physical.Store.Load(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func ea1ChildCancelBudget(t *testing.T, l budget.LedgerStore, id string) budget.Ledger {
	t.Helper()
	v, err := l.LoadBudget(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func ea1ChildCancelItems(t *testing.T, h harness.Store, id string) []harness.Item {
	t.Helper()
	v, err := h.ListItems(t.Context(), id, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func ea1ChildCancelExpectedBudget(before budget.Ledger, child string) budget.Ledger {
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
func ea1ChildCancelAssertOwner(t *testing.T, f *ea1ChildCancelHarnessFixture, root harness.Invocation, parent, child kernel.Snapshot) {
	t.Helper()
	if !reflect.DeepEqual(parent, ea1ChildCancelLoad(t, f, parent.Run.ID)) || !reflect.DeepEqual(child, ea1ChildCancelLoad(t, f, child.Run.ID)) {
		t.Fatal("cancellation changed original Kernel parent/child snapshot")
	}
	got, err := f.store.GetInvocation(t.Context(), root.ID)
	if err != nil || !reflect.DeepEqual(got, root) {
		t.Fatalf("cancellation changed original Workflow Invocation: %v", err)
	}
}
func TestEA1HarnessAgentTaskCancelP0(t *testing.T) {
	for _, mode := range []string{"actor", "tenant", "thread", "parent", "origin", "original-runner", "foreign-recorder"} {
		t.Run(mode, func(t *testing.T) {
			f := newEA1ChildCancelHarnessFixture(t, false)
			waiting, task, root, ledger := ea1ChildCancelStart(t, f)
			parent, child := ea1ChildCancelLoad(t, f, root.ExecutionRefID), ea1ChildCancelLoad(t, f, task.RunID)
			items := ea1ChildCancelItems(t, f.store, waiting.Turn.ID)
			if _, e := f.runtime.LookupChildExecution(t.Context(), task.RunID); !errors.Is(e, kernel.ErrNotFound) {
				t.Fatalf("waiting child fixture retained grant: %v", e)
			}
			candidate := f.harness
			switch mode {
			case "original-runner":
				other, e := agent.NewRunner(agent.Dependencies{Runtime: f.runtime, Model: f.model})
				if e != nil {
					t.Fatal(e)
				}
				f.executor.original = other
			case "foreign-recorder":
				other, e := runrelation.New(memory.NewRunRelationStore(), nil)
				if e != nil {
					t.Fatal(e)
				}
				original, e := f.relations.GetByChild(t.Context(), task.RunID)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = other.Ensure(t.Context(), runrelation.Draft{ParentRunID: original.ParentRunID, ChildRunID: original.ChildRunID, Kind: original.Kind, OwnerNodeID: original.OwnerNodeID}); e != nil {
					t.Fatal(e)
				}
				candidate = ea1ChildCancelRunner(t, f, f.store, f.backend.ledgers, other)
			default:
				f.physical.poison = func(v kernel.Snapshot) kernel.Snapshot {
					switch mode {
					case "actor":
						v.Run.Actor.ActorID = "foreign"
					case "tenant":
						v.Run.Actor.TenantID = "foreign"
					case "thread":
						v.Run.Thread.ID = "foreign"
					case "parent":
						v.Run.ParentRunID = "foreign"
					case "origin":
						var state map[string]json.RawMessage
						if e := json.Unmarshal(v.State, &state); e != nil {
							panic(e)
						}
						state["originFingerprint"] = json.RawMessage(`"foreign"`)
						var e error
						v.State, e = json.Marshal(state)
						if e != nil {
							panic(e)
						}
					}
					return v
				}
			}
			_, err := candidate.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "reject foreign child source")
			f.physical.poison = nil
			if err == nil {
				t.Fatal("foreign original child source accepted")
			}
			if !errors.Is(err, harness.ErrRuntimeCancellationUnverified) {
				t.Fatalf("unexpected rejection class: %v", err)
			}
			ea1ChildCancelAssertOwner(t, f, root, parent, child)
			if _, e := f.runtime.LookupChildExecution(t.Context(), task.RunID); !errors.Is(e, kernel.ErrNotFound) {
				t.Fatalf("foreign preflight minted grant: %v", e)
			}
			if !reflect.DeepEqual(items, ea1ChildCancelItems(t, f.store, waiting.Turn.ID)) || !reflect.DeepEqual(ledger, ea1ChildCancelBudget(t, f.backend.ledgers, waiting.Turn.ID)) || f.physical.controlCalls.Load() != 0 || f.model.calls != 1 || f.toolCalls != 0 {
				t.Fatal("foreign preflight mutated timeline/budget or dispatched callback")
			}
		})
	}
}
func ea1ChildCancelApprove(t *testing.T, f *ea1ChildCancelHarnessFixture, waiting harness.Snapshot, task harness.Subtask) kernel.Snapshot {
	t.Helper()
	_, err := f.harness.ResolveSubtaskApproval(t.Context(), waiting.Turn.ID, task.ID, task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove})
	if err != nil {
		t.Fatal(err)
	}
	child := ea1ChildCancelLoad(t, f, task.RunID)
	if child.Run.Status != kernel.RunStatusRunning || f.toolCalls != 0 {
		t.Fatal("explicit approval did not preserve deferred Tool boundary")
	}
	return child
}
func TestEA1HarnessAgentTaskCancelP2(t *testing.T) {
	f := newEA1ChildCancelHarnessFixture(t, false)
	waiting, task, root, ledger := ea1ChildCancelStart(t, f)
	child := ea1ChildCancelApprove(t, f, waiting, task)
	f.toolUnknown = true
	_, err := f.agent.Resume(t.Context(), task.RunID, child.Run.Revision)
	if !errors.Is(err, agent.ErrToolOutcomeUnknown) || f.toolCalls != 1 || !reflect.DeepEqual(f.toolRequest, ea1ChildCancelExpectedToolRequest(task.RunID)) {
		t.Fatalf("actual Tool send did not become unknown: %v calls=%d", err, f.toolCalls)
	}
	if !reflect.DeepEqual(f.toolResult, tools.ExecutionResult{}) {
		t.Fatal("unknown Tool unexpectedly produced a receipt")
	}
	child = ea1ChildCancelLoad(t, f, task.RunID)
	parent := ea1ChildCancelLoad(t, f, root.ExecutionRefID)
	var state struct {
		ToolDispatchID string `json:"toolDispatchID"`
	}
	if err = json.Unmarshal(child.State, &state); err != nil || state.ToolDispatchID != "ea1ChildCancel-call" {
		t.Fatalf("original Tool dispatch not retained: %+v %v", state, err)
	}
	grant, err := f.runtime.LookupChildExecution(t.Context(), task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.harness.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "cancel unknown original Tool")
	if !errors.Is(err, kernel.ErrChildExecutionBusy) {
		t.Fatalf("unknown Tool cancel must preserve native Busy: %v", err)
	}
	ea1ChildCancelAssertOwner(t, f, root, parent, child)
	retained, e := f.runtime.LookupChildExecution(t.Context(), task.RunID)
	if e != nil || retained != grant {
		t.Fatal("unknown Tool cancellation retired/replaced grant")
	}
	items := ea1ChildCancelItems(t, f.store, waiting.Turn.ID)
	intent := ea1OnlyCancelIntent(t, items, waiting.Turn.ID, task.RunID)
	if !harness.ActiveCancellationFence(items) || !reflect.DeepEqual(ea1ChildCancelExpectedBudget(ledger, task.RunID), ea1ChildCancelBudget(t, f.backend.ledgers, waiting.Turn.ID)) {
		t.Fatal("admitted cancellation lost original election or exact subtree budget transition")
	}
	_, err = f.harness.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "repeat unknown cancellation")
	if !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("repeat unknown election: %v", err)
	}
	after := ea1ChildCancelItems(t, f.store, waiting.Turn.ID)
	if !reflect.DeepEqual(intent, ea1OnlyCancelIntent(t, after, waiting.Turn.ID, task.RunID)) || !reflect.DeepEqual(items, after) {
		t.Fatal("repeat created new election/facts")
	}
	if err = f.agent.ReconcileChildPhysicalGrant(t.Context(), task.RunID); !errors.Is(err, kernel.ErrChildExecutionBusy) {
		t.Fatalf("unknown outcome reconciled: %v", err)
	}
	if _, err = f.agent.Resume(t.Context(), task.RunID, child.Run.Revision); !errors.Is(err, agent.ErrToolOutcomeUnknown) {
		t.Fatalf("unknown original Tool dispatch rejected before grant admission: %v", err)
	}
	ea1ChildCancelAssertOwner(t, f, root, parent, child)
	if f.toolCalls != 1 || f.model.calls != 1 || f.physical.controlCalls.Load() != 1 {
		t.Fatal("unknown child was redispatched or cancellation retried physically")
	}
}
func TestEA1HarnessAgentTaskCancelP3(t *testing.T) {
	f := newEA1ChildCancelHarnessFixture(t, false)
	waiting, task, root, ledger := ea1ChildCancelStart(t, f)
	child := ea1ChildCancelApprove(t, f, waiting, task)
	f.physical.markEntered = make(chan kernel.ChildExecutionGrant, 1)
	f.physical.markRelease = make(chan struct{})
	f.physical.markArmed.Store(true)
	var once sync.Once
	release := func() { once.Do(func() { close(f.physical.markRelease) }) }
	t.Cleanup(release)
	done := make(chan error, 1)
	go func() { _, e := f.agent.Resume(context.Background(), task.RunID, child.Run.Revision); done <- e }()
	var intercepted kernel.ChildExecutionGrant
	select {
	case intercepted = <-f.physical.markEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("original child Mark not intercepted")
	}
	child = ea1ChildCancelLoad(t, f, task.RunID)
	parent := ea1ChildCancelLoad(t, f, root.ExecutionRefID)
	grant, err := f.runtime.LookupChildExecution(t.Context(), task.RunID)
	if err != nil || grant != intercepted || grant.ChildRunID != task.RunID || f.physical.markHits.Load() != 1 {
		t.Fatal("intercepted wrong physical child scope")
	}
	if child.Run.Status != kernel.RunStatusCompleted || f.toolCalls != 1 || !reflect.DeepEqual(f.toolRequest, ea1ChildCancelExpectedToolRequest(task.RunID)) || !reflect.DeepEqual(f.toolResult, tools.ExecutionResult{Content: json.RawMessage(`{"evidence":"approved"}`), Receipt: tools.Receipt{ExecutionID: "ea1ChildCancel-call", Disposition: "read"}}) || !reflect.DeepEqual(child.Result, &kernel.Result{ContentType: "application/json", Content: f.toolResult.Content}) {
		t.Fatal("actual terminal Tool receipt was not committed before original Mark")
	}
	events, eventErr := f.runtime.ListEvents(t.Context(), task.RunID, 0, 500)
	if eventErr != nil {
		t.Fatal(eventErr)
	}
	receiptEvents := 0
	for _, event := range events {
		if event.Type == "tool.completed" {
			receiptEvents++
			if event.Message != f.toolResult.Receipt.Disposition {
				t.Fatal("durable Tool receipt disposition differs from actual handler return")
			}
		}
	}
	if receiptEvents != 1 {
		t.Fatalf("original Tool completion event count=%d", receiptEvents)
	}
	if err = f.agent.ReconcileChildPhysicalGrant(t.Context(), task.RunID); !errors.Is(err, kernel.ErrChildExecutionBusy) {
		t.Fatalf("receipt alone retired live callback: %v", err)
	}
	_, err = f.harness.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "confirm original settled Tool while Mark pending")
	if err != nil {
		t.Fatalf("verified terminal observation incorrectly rejected: %v", err)
	}
	ea1ChildCancelAssertOwner(t, f, root, parent, child)
	retained, e := f.runtime.LookupChildExecution(t.Context(), task.RunID)
	if e != nil || retained != grant || f.physical.controlCalls.Load() != 0 {
		t.Fatal("Host confirmation replaced physical settlement or issued Cancel")
	}
	if !reflect.DeepEqual(ea1ChildCancelExpectedBudget(ledger, task.RunID), ea1ChildCancelBudget(t, f.backend.ledgers, waiting.Turn.ID)) {
		t.Fatal("terminal confirmation altered unrelated budget")
	}
	release()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("original callback did not retire")
	}
	if _, err = f.runtime.LookupChildExecution(t.Context(), task.RunID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("original marked scope failed to retire: %v", err)
	}
	if !reflect.DeepEqual(child, ea1ChildCancelLoad(t, f, task.RunID)) || !reflect.DeepEqual(parent, ea1ChildCancelLoad(t, f, root.ExecutionRefID)) {
		t.Fatal("Mark/retirement changed original receipt snapshot")
	}
	if f.toolCalls != 1 || f.model.calls != 1 || f.physical.markHits.Load() != 1 {
		t.Fatal("receipt completion replayed physical work")
	}
}

type ea1ChildCancelReceiptACK struct {
	harness.Store
	child   string
	hits    atomic.Int32
	intent  harness.Item
	receipt harness.Item
}

func (s *ea1ChildCancelReceiptACK) AppendItem(ctx context.Context, v harness.Item) (harness.Item, bool, error) {
	saved, fresh, err := s.Store.AppendItem(ctx, v)
	if err == nil && fresh && v.RunID == s.child {
		if v.ParentItemID == harness.CancellationFenceParentItemID {
			s.intent = saved
		}
		if v.ParentItemID == s.intent.ID && v.Status == harness.ItemCompleted && harness.VerifiedCancellationFenceReceipt(s.intent, []harness.Item{saved}) {
			s.receipt = saved
			s.hits.Add(1)
			return harness.Item{}, false, context.DeadlineExceeded
		}
	}
	return saved, fresh, err
}
func TestEA1HarnessAgentTaskCancelP4(t *testing.T)             { ea1ChildCancelACKRecovery(t, false) }
func TestRealPostgresEA1HarnessAgentTaskCancelP4(t *testing.T) { ea1ChildCancelACKRecovery(t, true) }
func ea1ChildCancelACKRecovery(t *testing.T, persistent bool) {
	t.Helper()
	f := newEA1ChildCancelHarnessFixture(t, persistent)
	waiting, task, root, before := ea1ChildCancelStart(t, f)
	parent := ea1ChildCancelLoad(t, f, root.ExecutionRefID)
	fault := &ea1ChildCancelReceiptACK{Store: f.store, child: task.RunID}
	_, err := ea1ChildCancelRunner(t, f, fault, f.backend.ledgers, f.relations).CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "original child cancellation")
	if !errors.Is(err, context.DeadlineExceeded) || fault.hits.Load() != 1 || f.physical.controlCalls.Load() != 1 {
		t.Fatalf("original child receipt ACK fault not hit exactly once: %v hits=%d controls=%d", err, fault.hits.Load(), f.physical.controlCalls.Load())
	}
	child := ea1ChildCancelLoad(t, f, task.RunID)
	if child.Run.Status != kernel.RunStatusCancelled || f.toolCalls != 0 || f.model.calls != 1 {
		t.Fatal("waiting child cancellation dispatched physical Tool")
	}
	ea1ChildCancelAssertOwner(t, f, root, parent, child)
	ledger := ea1ChildCancelBudget(t, f.backend.ledgers, waiting.Turn.ID)
	if !reflect.DeepEqual(ea1ChildCancelExpectedBudget(before, task.RunID), ledger) {
		t.Fatal("CancelSubtask changed unrelated ledger authority")
	}
	items := ea1ChildCancelItems(t, f.store, waiting.Turn.ID)
	if !reflect.DeepEqual(ea1OnlyCancelIntent(t, items, waiting.Turn.ID, task.RunID), fault.intent) || !harness.VerifiedCancellationFenceReceipt(fault.intent, ea1CancelReceipts(items, fault.intent.ID)) || harness.ActiveCancellationFence(items) {
		t.Fatal("actual ACK-lost original cancellation receipt missing")
	}
	// Close old Host and peer pools, then reopen only Host persistence. Kernel,
	// definition registry and exact Agent/Workflow/recorder are intentionally
	// retained; native Kernel crash recovery is separate AC-C conformance.
	h, l := f.backend.reopen()
	if !reflect.DeepEqual(items, ea1ChildCancelItems(t, h, waiting.Turn.ID)) || !reflect.DeepEqual(ledger, ea1ChildCancelBudget(t, l, waiting.Turn.ID)) {
		t.Fatal("Host reopen lost original timeline or full budget")
	}
	persistedRoot, e := h.GetInvocation(t.Context(), root.ID)
	if e != nil || !reflect.DeepEqual(persistedRoot, root) {
		t.Fatalf("Host reopen changed root Invocation: %v", e)
	}
	_, err = ea1ChildCancelRunner(t, f, h, l, f.relations).CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "inspect original child receipt after reopen")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(child, ea1ChildCancelLoad(t, f, task.RunID)) || !reflect.DeepEqual(parent, ea1ChildCancelLoad(t, f, root.ExecutionRefID)) || !reflect.DeepEqual(ledger, ea1ChildCancelBudget(t, l, waiting.Turn.ID)) || f.physical.controlCalls.Load() != 1 || f.toolCalls != 0 || f.model.calls != 1 {
		t.Fatal("recovery replayed physical cancellation or changed frozen authority/budget")
	}
	// A later completed cancellation may create a new settled election; it
	// cannot rewrite the original intent/receipt or repeat native Control.
	original, e := h.GetItem(t.Context(), fault.intent.ID)
	if e != nil || !reflect.DeepEqual(original, fault.intent) {
		t.Fatal("recovery replaced original election")
	}
	receipt, e := h.GetItem(t.Context(), fault.receipt.ID)
	if e != nil || !reflect.DeepEqual(receipt, fault.receipt) {
		t.Fatal("recovery replaced original committed receipt")
	}
}

func ea1ChildCancelExpectedToolRequest(childID string) tools.ExecutionRequest {
	return tools.ExecutionRequest{RunID: childID, Call: tools.Call{ID: "ea1ChildCancel-call", ToolKey: ea1ChildCancelToolKey, Name: "read_evidence", Arguments: json.RawMessage(`{}`)}}
}

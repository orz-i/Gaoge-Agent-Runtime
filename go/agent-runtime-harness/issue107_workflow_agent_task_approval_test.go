package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

const issue107ToolKey = "issue107.read-evidence"

type issue107Model struct{ calls int }

func (m *issue107Model) Generate(_ context.Context, _ model.Request) (model.Response, error) {
	m.calls++
	if m.calls == 1 {
		return model.Response{ToolCalls: []tools.Call{{
			ID: "issue107-call", ToolKey: issue107ToolKey, Name: "read_evidence", Arguments: json.RawMessage(`{}`),
		}}}, nil
	}
	return model.Response{Content: "approved evidence"}, nil
}

type issue107AgentEffect struct {
	original       *agent.Runner
	relations      *runrelation.Registry
	approvalFenced bool
}

func (e *issue107AgentEffect) LocalAgentTaskRunner() *agent.Runner { return e.original }
func (e *issue107AgentEffect) LocalAgentTaskRecorder() runrelation.Recorder {
	return e.relations
}
func (e *issue107AgentEffect) FencesAgentTaskApprovalThroughHarness() bool {
	return e.approvalFenced
}
func (e *issue107AgentEffect) VerifyAgentTaskOrigin(effect workflow.Effect, child kernel.Snapshot) bool {
	var input struct {
		Goal string `json:"goal"`
	}
	return effect.Kind == "fixture.agent" && effect.Revision == "v1" &&
		json.Unmarshal(effect.Input, &input) == nil &&
		strings.TrimSpace(input.Goal) != "" && strings.TrimSpace(input.Goal) == child.Run.Goal
}
func (e *issue107AgentEffect) Execute(ctx context.Context, request workflow.EffectRequest) (workflow.EffectResult, error) {
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
			ToolKeys: []string{issue107ToolKey},
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

type issue107HarnessFixture struct {
	savedApprovalCtx      context.Context
	observedApprovalGrant bool
	runtime               *kernel.Runtime
	store                 *harness.MemoryStore
	agent                 *agent.Runner
	workflow              *workflow.Runner
	harness               *harness.Runner
	relations             *runrelation.Registry
	model                 *issue107Model
	toolCalls             int
	toolStarted           chan struct{}
	toolRelease           chan struct{}
	approvalPauseStarted  chan struct{}
	approvalPauseRelease  chan struct{}
	request               harness.WorkflowTurnRequest
	executor              *issue107AgentEffect
}

type issue107ApprovalContextCapture struct {
	fixture *issue107HarnessFixture
}

func (issue107ApprovalContextCapture) Name() string { return "issue107.approval_context_capture" }
func (c issue107ApprovalContextCapture) Run(ctx context.Context, invocation plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	if invocation.Operation == plugin.RunResolveApproval {
		c.fixture.savedApprovalCtx = ctx
		child, err := c.fixture.runtime.Load(ctx, invocation.RunID)
		if err == nil && child.Checkpoint != nil {
			c.fixture.observedApprovalGrant = harness.VerifiedSubtaskApprovalDispatch(ctx, invocation.RunID,
				child.Run.Revision, child.Checkpoint.ID)
		}
		if c.fixture.approvalPauseStarted != nil {
			close(c.fixture.approvalPauseStarted)
			<-c.fixture.approvalPauseRelease
		}
	}
	return next(ctx)
}

func newIssue107HarnessFixture(t *testing.T, options ...bool) *issue107HarnessFixture {
	t.Helper()
	hosted := len(options) > 0 && options[0]
	fenced := len(options) > 1 && options[1]
	capture := len(options) > 2 && options[2]
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	f := &issue107HarnessFixture{runtime: runtime, store: store, relations: relations, model: &issue107Model{}}
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: issue107ToolKey, Name: "read_evidence",
			InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			if f.toolStarted != nil {
				close(f.toolStarted)
				<-f.toolRelease
			}
			f.toolCalls++
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"evidence":"approved"}`),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "read"},
			}, nil
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
	if hosted {
		dependencies.PrepareModelRequest = func(_ context.Context, request model.Request) (model.Request, error) {
			return request, nil
		}
	}
	if capture {
		dependencies.RunMiddleware = []plugin.RunMiddleware{issue107ApprovalContextCapture{fixture: f}}
	}
	f.agent, err = agent.NewRunner(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	f.executor = &issue107AgentEffect{original: f.agent, relations: relations, approvalFenced: fenced}
	static, err := workflow.NewStaticEffectRouter(nil, workflow.EffectRoute{
		Class: workflow.EffectClassAgent, Executor: f.executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := workflow.NewDefinitionRegistry(workflow.NewMemoryDefinitionStore(), featureInvocationClock{})
	if err != nil {
		t.Fatal(err)
	}
	f.workflow, err = workflow.NewRunner(workflow.Dependencies{
		Runtime: runtime, Effects: static, Registry: definitions, Relations: relations, DeferResumption: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.harness, err = harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: f.agent, Workflows: f.workflow, Store: store, Relations: relations,
		Clock: featureInvocationClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
		ID: "issue107", Revision: 1, Name: "AgentTask child approval",
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
	request := testStartRequest()
	request.HostThread.ID = "issue107-thread"
	request.Thread.ID = "issue107-thread"
	request.HostTurn.ID = "issue107-turn"
	request.RequestID = "issue107-request"
	request.Goal = "run approved subtask"
	request.Config.Model = "fixture-model"
	request.Config.ToolKeys = []string{issue107ToolKey}
	request.Config.ToolPolicies = []harness.ToolPolicySnapshot{{
		Key: issue107ToolKey, DefinitionVersion: "v1", ApprovalCapability: "per_call", ApprovalMode: "always",
	}}
	f.request = harness.WorkflowTurnRequest{StartRequest: request, Definition: definition, Input: json.RawMessage(`{}`)}
	return f
}

func TestIssue107WorkflowAgentTaskSubtaskApprovalAndResume(t *testing.T) {
	f := newIssue107HarnessFixture(t)
	waiting, err := f.harness.StartWorkflowTurn(t.Context(), f.request)
	if err != nil {
		t.Fatal(err)
	}
	if len(waiting.Subtasks) != 1 || waiting.Subtasks[0].Approval == nil ||
		waiting.Subtasks[0].Status != string(kernel.RunStatusWaitingInput) ||
		f.toolCalls != 0 || f.model.calls != 1 {
		t.Fatalf("before approval subtasks=%+v toolCalls=%d modelCalls=%d", waiting.Subtasks, f.toolCalls, f.model.calls)
	}
	task := waiting.Subtasks[0]
	if _, err = f.harness.ResolveSubtaskApproval(t.Context(), waiting.Turn.ID, task.ID, "stale-checkpoint",
		harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove}); !errors.Is(err, harness.ErrConflict) || f.toolCalls != 0 {
		t.Fatalf("stale checkpoint crossed control boundary: err=%v tools=%d", err, f.toolCalls)
	}
	approved, err := f.harness.ResolveSubtaskApproval(t.Context(), waiting.Turn.ID, task.ID, task.Approval.CheckpointID,
		harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove})
	if err != nil {
		t.Fatalf("original route checkpoint approval failed: %v", err)
	}
	if _, err = f.harness.ResolveSubtaskApproval(t.Context(), waiting.Turn.ID, task.ID, task.Approval.CheckpointID,
		harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove}); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("approval replay accepted: %v", err)
	}
	child, err := f.runtime.Load(t.Context(), task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.agent.Resume(t.Context(), child.Run.ID, child.Run.Revision); err != nil {
		t.Fatal(err)
	}
	if f.toolCalls != 1 {
		t.Fatalf("original Tool must dispatch exactly once, got %d", f.toolCalls)
	}
	root, ok := harness.TopLevelInvocation(waiting)
	if !ok {
		t.Fatal("missing original Workflow Invocation")
	}
	for range 4 {
		parent, loadErr := f.runtime.Load(t.Context(), root.ExecutionRefID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if parent.Run.Status == kernel.RunStatusCompleted {
			break
		}
		if _, resumeErr := f.workflow.Resume(t.Context(), parent.Run.ID, parent.Run.Revision); resumeErr != nil &&
			!errors.Is(resumeErr, workflow.ErrEffectPending) {
			t.Fatalf("workflow resume: %v", resumeErr)
		}
	}
	final, err := f.harness.Refresh(t.Context(), approved.Turn.ID)
	if err != nil || final.Turn.Status != harness.TurnCompleted || f.toolCalls != 1 || f.model.calls != 2 {
		t.Fatalf("resumed: turn=%+v tools=%d model=%d err=%v", final.Turn, f.toolCalls, f.model.calls, err)
	}
}

func TestIssue107WrongOriginalAgentCannotApprovePhysicalChild(t *testing.T) {
	f := newIssue107HarnessFixture(t)
	waiting, err := f.harness.StartWorkflowTurn(t.Context(), f.request)
	if err != nil || len(waiting.Subtasks) != 1 || waiting.Subtasks[0].Approval == nil {
		t.Fatalf("fixture: err=%v subtasks=%+v", err, waiting.Subtasks)
	}
	// Replacing the original route with another Agent on the *same* Kernel
	// must not make its middleware/approval hooks a valid continuation path.
	substitute, err := agent.NewRunner(agent.Dependencies{Runtime: f.runtime, Model: f.model})
	if err != nil {
		t.Fatal(err)
	}
	f.executor.original = substitute
	task := waiting.Subtasks[0]
	before := f.model.calls
	_, err = f.harness.ResolveSubtaskApproval(t.Context(), waiting.Turn.ID, task.ID,
		task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove})
	if !errors.Is(err, harness.ErrConflict) || f.model.calls != before || f.toolCalls != 0 {
		t.Fatalf("foreign agent route approved: err=%v model=%d->%d tool=%d", err, before, f.model.calls, f.toolCalls)
	}
}

func TestIssue107ForeignRelationRecorderCannotExposeApproval(t *testing.T) {
	f := newIssue107HarnessFixture(t)
	waiting, err := f.harness.StartWorkflowTurn(t.Context(), f.request)
	if err != nil || len(waiting.Subtasks) != 1 || waiting.Subtasks[0].Approval == nil {
		t.Fatalf("fixture: err=%v subtasks=%+v", err, waiting.Subtasks)
	}
	root, ok := harness.TopLevelInvocation(waiting)
	if !ok {
		t.Fatal("missing parent")
	}
	edges, err := f.relations.ListChildren(t.Context(), root.ExecutionRefID)
	if err != nil || len(edges) != 1 {
		t.Fatalf("relation fixture edges=%v err=%v", edges, err)
	}
	// Copy identical public relation values into another registry. Even a
	// matching parent/child/owner tuple is not physical recorder identity.
	foreign, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = foreign.Ensure(t.Context(), runrelation.Draft{
		ParentRunID: edges[0].ParentRunID, ChildRunID: edges[0].ChildRunID,
		Kind: edges[0].Kind, OwnerNodeID: edges[0].OwnerNodeID,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := harness.NewRunner(harness.Dependencies{
		Runtime: f.runtime, Agent: f.agent, Workflows: f.workflow, Store: f.store,
		Relations: foreign, Clock: featureInvocationClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := wrong.Refresh(t.Context(), waiting.Turn.ID)
	if err == nil && (len(observed.Subtasks) != 1 || observed.Subtasks[0].Approval != nil ||
		observed.Subtasks[0].Status == string(kernel.RunStatusWaitingInput)) {
		t.Fatalf("foreign relation registry projected local approval: %+v", observed.Subtasks)
	}
	// No physical Tool dispatch is permitted through the counterfeit index.
	if f.toolCalls != 0 {
		t.Fatalf("foreign registry dispatched %d tools", f.toolCalls)
	}
}
func TestIssue107AgentTaskCancelUsesExactPhysicalFence(t *testing.T) {
	f := newIssue107HarnessFixture(t)
	waiting, err := f.harness.StartWorkflowTurn(t.Context(), f.request)
	if err != nil || len(waiting.Subtasks) != 1 || waiting.Subtasks[0].Approval == nil {
		t.Fatalf("fixture: err=%v subtasks=%+v", err, waiting.Subtasks)
	}
	task := waiting.Subtasks[0]
	_, err = f.harness.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "stop unapproved child")
	if err != nil {
		t.Fatalf("same-Kernel native child cancellation: %v", err)
	}
	child, err := f.runtime.Load(t.Context(), task.RunID)
	if err != nil || child.Run.Status != kernel.RunStatusCancelled || f.toolCalls != 0 {
		t.Fatalf("physical child was not safely fenced: %+v err=%v calls=%d", child.Run, err, f.toolCalls)
	}
}
func TestIssue107ChildApprovalGrantFencesSubtaskCancellation(t *testing.T) {
	f := newIssue107HarnessFixture(t)
	waiting, err := f.harness.StartWorkflowTurn(t.Context(), f.request)
	if err != nil || len(waiting.Subtasks) != 1 || waiting.Subtasks[0].Approval == nil {
		t.Fatalf("fixture: err=%v subtasks=%+v", err, waiting.Subtasks)
	}
	root, ok := harness.TopLevelInvocation(waiting)
	if !ok {
		t.Fatal("missing root")
	}
	task := waiting.Subtasks[0]
	now := featureInvocationClock{}.Now()
	grant := harness.Item{
		ID: "issue107-approval-grant", TurnID: waiting.Turn.ID, InvocationID: root.ID,
		RunID: root.ExecutionRefID, Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		ParentItemID: harness.ExecutionDispatchParentItemID,
		Payload:      json.RawMessage(`{"phase":"resolve-subtask-approval","physicalRunID":"` + task.RunID + `"}`),
		CreatedAt:    now, UpdatedAt: now,
	}
	if _, fresh, err := f.store.AppendItem(t.Context(), grant); err != nil || !fresh {
		t.Fatalf("create physical approval grant: fresh=%v err=%v", fresh, err)
	}
	_, err = f.harness.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "race with approved child")
	if !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("unsettled child approval grant was not respected: %v", err)
	}
	child, err := f.runtime.Load(t.Context(), task.RunID)
	if err != nil || child.Run.Status != kernel.RunStatusWaitingInput || f.toolCalls != 0 {
		t.Fatalf("cancel raced original approval grant: child=%+v err=%v calls=%d", child.Run, err, f.toolCalls)
	}
}

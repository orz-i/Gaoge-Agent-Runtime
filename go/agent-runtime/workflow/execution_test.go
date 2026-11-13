package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

var errExecutorFailed = errors.New("executor failed")

type resumeResult struct {
	snapshot kernel.Snapshot
	err      error
}

func TestWorkflowRejectsInvalidDefinitionSchema(t *testing.T) {
	t.Parallel()
	_, err := workflow.CompileDefinition(workflow.DefinitionDraft{
		ID: "invalid-schema", Revision: 1, Name: "Invalid schema",
		InputSchema: json.RawMessage(`{"type":"not-a-real-type"}`),
		Nodes:       []workflow.Node{returnNode(json.RawMessage(`null`))},
	})
	if !errors.Is(err, workflow.ErrInvalidDefinition) {
		t.Fatalf("invalid definition schema = %v", err)
	}
}

func TestWorkflowValidatesInputBeforeCreatingRun(t *testing.T) {
	t.Parallel()
	runtime := newWorkflowRuntime(t)
	runner := newWorkflowRunner(t, runtime, &scriptedExecutor{runtime: runtime})
	definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
		ID: "input-contract", Revision: 1, Name: "Input contract",
		InputSchema: json.RawMessage(`{
			"type":"object","additionalProperties":false,"required":["storyID"],
			"properties":{"storyID":{"type":"string","minLength":1}}
		}`),
		Nodes: []workflow.Node{returnNode(json.RawMessage(`{"ok":true}`))},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := workflowRequest(definition)
	request.ID = "invalid-workflow-input"
	request.Input = json.RawMessage(`{"unexpected":true}`)
	_, err = runner.StartRun(t.Context(), request)
	if !errors.Is(err, workflow.ErrInputSchema) {
		t.Fatalf("invalid workflow input = %v", err)
	}
	if _, loadErr := runtime.Load(t.Context(), request.ID); !errors.Is(loadErr, kernel.ErrNotFound) {
		t.Fatalf("invalid input created a Run: %v", loadErr)
	}
}

func TestWorkflowValidatesOutputBeforeSuccessfulCompletion(t *testing.T) {
	t.Parallel()
	runtime := newWorkflowRuntime(t)
	runner := newWorkflowRunner(t, runtime, &scriptedExecutor{runtime: runtime})
	definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
		ID: "output-contract", Revision: 1, Name: "Output contract",
		OutputSchema: json.RawMessage(`{
			"type":"object","additionalProperties":false,"required":["status"],
			"properties":{"status":{"const":"done"}}
		}`),
		Nodes: []workflow.Node{returnNode(json.RawMessage(`{"wrong":true}`))},
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := runner.StartRun(t.Context(), workflowRequest(definition))
	if !errors.Is(err, workflow.ErrOutputSchema) || failed.Run.Status != kernel.RunStatusFailed || failed.Result != nil ||
		failed.Run.ErrorCode != "workflow.output_schema" {
		t.Fatalf("invalid output = %#v, %v", failed, err)
	}
}

func TestUnknownExternalEffectIntentCannotReplayWithoutReceipt(t *testing.T) {
	t.Parallel()
	runtime := newWorkflowRuntime(t)
	executor := &scriptedExecutor{
		runtime: runtime,
		results: []workflow.EffectResult{
			{Disposition: workflow.DispositionPending},
			{Disposition: workflow.DispositionCompleted, ReceiptID: "receipt_1", Output: json.RawMessage(`{"ok":true}`)},
		},
	}
	runner := newWorkflowRunner(t, runtime, executor)
	definition := compileWorkflow(t, []workflow.Node{
		effectNode("send", "message.send"),
		returnNode(json.RawMessage(`{"status":"sent"}`)),
	}, workflow.Limits{})

	pending, err := runner.StartRun(context.Background(), workflowRequest(definition))
	if !errors.Is(err, workflow.ErrEffectPending) || pending.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("expected pending effect: %#v, %v", pending, err)
	}
	pendingView := mustWorkflowView(t, pending)
	if len(pendingView.Effects) != 1 || pendingView.Effects[0].Status != workflow.EffectPending {
		t.Fatalf("unexpected pending intent: %#v", pendingView)
	}
	effectID := pendingView.Effects[0].ID

	// The initial external Executor returned DispositionPending without a
	// verifiable local child or physical receipt. Replay cannot be treated
	// as safe idempotency just because it reuses the same Effect ID.
	if !errors.Is(err, kernel.ErrChildExecutionBusy) {
		t.Fatalf("unknown original physical callback was not fenced: %v", err)
	}
	resumed, resumeErr := runner.Resume(context.Background(), pending.Run.ID, pending.Run.Revision)
	if !errors.Is(resumeErr, kernel.ErrChildExecutionBusy) {
		t.Fatalf("unknown physical Effect could be re-admitted: status=%s err=%v", resumed.Run.Status, resumeErr)
	}
	if len(executor.calls) != 1 || executor.calls[0] != effectID {
		t.Fatalf("unknown external Effect was replayed: %v", executor.calls)
	}
	if _, lookupErr := runtime.LookupChildExecution(context.Background(), pending.Run.ID); lookupErr != nil {
		t.Fatalf("unknown physical Grant was prematurely retired: %v", lookupErr)
	}
	if !executor.allCallsObservedPersistedIntent() {
		t.Fatal("Executor observed an Effect before its durable intent")
	}
}

func TestWaitCheckpointResumesWorkflow(t *testing.T) {
	t.Parallel()
	runtime := newWorkflowRuntime(t)
	runner := newWorkflowRunner(t, runtime, &scriptedExecutor{runtime: runtime})
	definition := compileWorkflow(t, []workflow.Node{
		{
			ID: "approval", Type: workflow.NodeWait,
			Wait: &workflow.WaitNode{Kind: "editor.approval", Payload: json.RawMessage(`{"required":true}`)},
		},
		returnNode(json.RawMessage(`{"status":"approved"}`)),
	}, workflow.Limits{})

	waiting, err := runner.StartRun(context.Background(), workflowRequest(definition))
	assertWorkflowWaiting(t, waiting, err)

	response := json.RawMessage(`{"approved":true,"reviewer":"editor"}`)
	completed, err := runner.ResolveWait(context.Background(), waiting.Run.ID, waiting.Run.Revision, response)
	if err != nil {
		t.Fatalf("resolve workflow wait: %v", err)
	}
	assertWorkflowCompleted(t, completed)
	assertResolvedWait(t, completed, response)
}

func TestDeferredWaitRequiresExplicitResume(t *testing.T) {
	t.Parallel()
	runtime := newWorkflowRuntime(t)
	runner, err := workflow.NewRunner(workflow.Dependencies{
		Runtime: runtime, Effects: &scriptedExecutor{runtime: runtime}, DeferResumption: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := compileWorkflow(t, []workflow.Node{
		{
			ID: "approval", Type: workflow.NodeWait,
			Wait: &workflow.WaitNode{Kind: "editor.approval", Payload: json.RawMessage(`{"required":true}`)},
		},
		returnNode(json.RawMessage(`{"status":"approved"}`)),
	}, workflow.Limits{})
	waiting, err := runner.StartRun(t.Context(), workflowRequest(definition))
	assertWorkflowWaiting(t, waiting, err)
	resolved, err := runner.ResolveWait(
		t.Context(), waiting.Run.ID, waiting.Run.Revision, json.RawMessage(`{"approved":true}`),
	)
	if err != nil || resolved.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("resolved=%#v err=%v", resolved.Run, err)
	}
	completed, err := runner.Resume(t.Context(), resolved.Run.ID, resolved.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkflowCompleted(t, completed)
}

func TestWorkflowEffectRecordsStableChildRelation(t *testing.T) {
	t.Parallel()
	runtime := newWorkflowRuntime(t)
	executor := &scriptedExecutor{
		runtime: runtime,
		results: []workflow.EffectResult{{
			Disposition: workflow.DispositionCompleted, ChildRunID: "child-1",
			ReceiptID: "receipt-1", Output: json.RawMessage(`{"ok":true}`),
		}},
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), workflowClock{})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := workflow.NewRunner(workflow.Dependencies{
		Runtime: runtime, Effects: executor, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := compileWorkflow(t, []workflow.Node{
		effectNode("draft", "agent.run"), returnNode(json.RawMessage(`{"done":true}`)),
	}, workflow.Limits{})
	completed, err := runner.StartRun(t.Context(), workflowRequest(definition))
	if err != nil {
		t.Fatal(err)
	}
	view := mustWorkflowView(t, completed)
	items, err := relations.ListChildren(t.Context(), completed.Run.ID)
	if err != nil || len(items) != 1 || view.Effects[0].ChildRunID != "child-1" ||
		items[0].Kind != runrelation.KindWorkflowEffect || items[0].OwnerNodeID != "draft" {
		t.Fatalf("view=%#v relations=%#v err=%v", view, items, err)
	}
}

func TestSegmentDispatchesAtMostOneEffect(t *testing.T) {
	t.Parallel()
	runtime := newWorkflowRuntime(t)
	executor := &scriptedExecutor{
		runtime: runtime,
		results: []workflow.EffectResult{
			{Disposition: workflow.DispositionCompleted, ReceiptID: "receipt_1", Output: json.RawMessage(`1`)},
			{Disposition: workflow.DispositionCompleted, ReceiptID: "receipt_2", Output: json.RawMessage(`2`)},
		},
	}
	runner := newWorkflowRunner(t, runtime, executor)
	definition := compileWorkflow(t, []workflow.Node{
		effectNode("first", "first.effect"),
		effectNode("second", "second.effect"),
		returnNode(json.RawMessage(`{"status":"done"}`)),
	}, workflow.Limits{MaxActivationsPerSegment: 8})

	yielded, err := runner.StartRun(context.Background(), workflowRequest(definition))
	if !errors.Is(err, workflow.ErrSegmentYielded) || len(executor.calls) != 1 {
		t.Fatalf("expected one effect then segment yield: calls=%v err=%v", executor.calls, err)
	}
	yieldedView := mustWorkflowView(t, yielded)
	if yieldedView.Budget.Segments != 1 || len(yieldedView.Effects) != 2 ||
		yieldedView.Effects[1].Status != workflow.EffectPending {
		t.Fatalf("unexpected yielded state: %#v", yieldedView)
	}

	completed, err := runner.Resume(context.Background(), yielded.Run.ID, yielded.Run.Revision)
	if err != nil {
		t.Fatalf("resume yielded workflow: %v", err)
	}
	assertWorkflowCompleted(t, completed)
	completedView := mustWorkflowView(t, completed)
	if completedView.Budget.Segments != 2 || len(executor.calls) != 2 {
		t.Fatalf("unexpected segment ledger: %#v calls=%v", completedView.Budget, executor.calls)
	}
}

func TestEffectFailureFailsWorkflow(t *testing.T) {
	t.Parallel()
	runtime := newWorkflowRuntime(t)
	executor := &scriptedExecutor{runtime: runtime, errs: []error{errExecutorFailed}}
	runner := newWorkflowRunner(t, runtime, executor)
	definition := compileWorkflow(t, []workflow.Node{
		effectNode("write", "unsafe.write"),
		returnNode(json.RawMessage(`null`)),
	}, workflow.Limits{})

	failed, err := runner.StartRun(context.Background(), workflowRequest(definition))
	if !errors.Is(err, workflow.ErrEffectFailed) || failed.Run.Status != kernel.RunStatusFailed {
		t.Fatalf("expected failed workflow: %#v, %v", failed, err)
	}
	view := mustWorkflowView(t, failed)
	if view.Effects[0].Status != workflow.EffectFailed || view.Activations[0].Status != workflow.ActivationFailed {
		t.Fatalf("unexpected failed effect state: %#v", view)
	}
}

func TestConcurrentResumeNeverReplaysUnknownExternalEffect(t *testing.T) {
	t.Parallel()
	runtime := newWorkflowRuntime(t)
	executor := newBarrierExecutor(runtime)
	runner := newWorkflowRunner(t, runtime, executor)
	definition := compileWorkflow(t, []workflow.Node{
		effectNode("send", "message.send"),
		returnNode(json.RawMessage(`true`)),
	}, workflow.Limits{})

	pending, err := runner.StartRun(context.Background(), workflowRequest(definition))
	if !errors.Is(err, workflow.ErrEffectPending) {
		t.Fatalf("expected initial pending effect, got %v", err)
	}
	results := make(chan resumeResult, 2)
	for range 2 {
		go func() {
			snapshot, resumeErr := runner.Resume(context.Background(), pending.Run.ID, pending.Run.Revision)
			results <- resumeResult{snapshot: snapshot, err: resumeErr}
		}()
	}
	first, second := <-results, <-results
	if !errors.Is(first.err, kernel.ErrChildExecutionBusy) ||
		!errors.Is(second.err, kernel.ErrChildExecutionBusy) ||
		len(executor.calls()) != 1 {
		t.Fatalf("duplicate queue Resume replayed an unknown pending Effect: first=%v second=%v calls=%v",
			first.err, second.err, executor.calls())
	}
}

func newWorkflowRuntime(t *testing.T) *kernel.Runtime {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{
		Store: memory.NewStore(), Clock: workflowClock{}, IDs: &workflowIDs{},
	})
	if err != nil {
		t.Fatalf("create kernel: %v", err)
	}
	return runtime
}

func newWorkflowRunner(t *testing.T, runtime *kernel.Runtime, executor workflow.EffectExecutor) *workflow.Runner {
	t.Helper()
	runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: runtime, Effects: executor})
	if err != nil {
		t.Fatalf("create workflow runner: %v", err)
	}
	return runner
}

func compileWorkflow(t *testing.T, nodes []workflow.Node, limits workflow.Limits) workflow.Definition {
	t.Helper()
	definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
		ID: "workflow", Revision: 1, Name: "Workflow", Nodes: nodes, Limits: limits,
	})
	if err != nil {
		t.Fatalf("compile workflow: %v", err)
	}
	return definition
}

func workflowRequest(definition workflow.Definition) workflow.StartRequest {
	return workflow.StartRequest{
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "execute workflow", Definition: definition, Input: json.RawMessage(`{"input":true}`),
	}
}

func effectNode(id string, kind string) workflow.Node {
	return workflow.Node{
		ID: id, Type: workflow.NodeEffect,
		Effect: &workflow.EffectNode{Kind: kind, Input: json.RawMessage(`{"value":1}`)},
	}
}

func returnNode(value json.RawMessage) workflow.Node {
	return workflow.Node{ID: workflowReturnNodeID, Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: value}}
}

func assertWorkflowWaiting(t *testing.T, snapshot kernel.Snapshot, err error) {
	t.Helper()
	if err != nil || snapshot.Run.Status != kernel.RunStatusWaitingInput || snapshot.Checkpoint == nil {
		t.Fatalf("expected workflow wait: %#v, %v", snapshot, err)
	}
	request, decodeErr := workflow.WaitRequestFromCheckpoint(snapshot.Checkpoint)
	if decodeErr != nil || request.Kind != "editor.approval" || request.WaitID == "" {
		t.Fatalf("unexpected wait request: %#v, %v", request, decodeErr)
	}
}

func assertResolvedWait(t *testing.T, snapshot kernel.Snapshot, response json.RawMessage) {
	t.Helper()
	view := mustWorkflowView(t, snapshot)
	if len(view.Waits) != 1 || view.Waits[0].Status != workflow.WaitResolved ||
		string(view.Waits[0].Response) != string(response) || view.Activations[0].Status != workflow.ActivationCompleted {
		t.Fatalf("unexpected resolved wait state: %#v", view)
	}
}

func mustWorkflowView(t *testing.T, snapshot kernel.Snapshot) workflow.View {
	t.Helper()
	view, err := workflow.ViewState(snapshot)
	if err != nil {
		t.Fatalf("decode workflow view: %v", err)
	}
	return view
}

func assertWorkflowCompleted(t *testing.T, snapshot kernel.Snapshot) {
	t.Helper()
	if snapshot.Run.Status != kernel.RunStatusCompleted || snapshot.Result == nil {
		t.Fatalf("expected completed workflow: %#v", snapshot)
	}
}

type scriptedExecutor struct {
	mu        sync.Mutex
	runtime   *kernel.Runtime
	results   []workflow.EffectResult
	errs      []error
	calls     []string
	persisted []bool
}

func (executor *scriptedExecutor) Execute(
	ctx context.Context,
	request workflow.EffectRequest,
) (workflow.EffectResult, error) {
	persisted := effectIntentPersisted(ctx, executor.runtime, request.RunID, request.EffectID)
	executor.mu.Lock()
	defer executor.mu.Unlock()
	index := len(executor.calls)
	executor.calls = append(executor.calls, request.EffectID)
	executor.persisted = append(executor.persisted, persisted)
	if index < len(executor.errs) && executor.errs[index] != nil {
		return workflow.EffectResult{}, executor.errs[index]
	}
	if index < len(executor.results) {
		return executor.results[index], nil
	}
	return workflow.EffectResult{Disposition: workflow.DispositionPending}, nil
}

func (executor *scriptedExecutor) allCallsObservedPersistedIntent() bool {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	for _, persisted := range executor.persisted {
		if !persisted {
			return false
		}
	}
	return true
}

type barrierExecutor struct {
	runtime *kernel.Runtime
	mu      sync.Mutex
	ids     []string
	resumes int
	ready   chan struct{}
}

func newBarrierExecutor(runtime *kernel.Runtime) *barrierExecutor {
	return &barrierExecutor{runtime: runtime, ready: make(chan struct{})}
}

func (executor *barrierExecutor) Execute(
	ctx context.Context,
	request workflow.EffectRequest,
) (workflow.EffectResult, error) {
	if !effectIntentPersisted(ctx, executor.runtime, request.RunID, request.EffectID) {
		return workflow.EffectResult{}, errExecutorFailed
	}
	executor.mu.Lock()
	executor.ids = append(executor.ids, request.EffectID)
	if len(executor.ids) == 1 {
		executor.mu.Unlock()
		return workflow.EffectResult{Disposition: workflow.DispositionPending}, nil
	}
	executor.resumes++
	if executor.resumes == 2 {
		close(executor.ready)
	}
	ready := executor.ready
	executor.mu.Unlock()
	<-ready
	return workflow.EffectResult{
		Disposition: workflow.DispositionCompleted,
		ReceiptID:   "receipt_" + request.EffectID,
		Output:      json.RawMessage(`{"ok":true}`),
	}, nil
}

func (executor *barrierExecutor) calls() []string {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return append([]string(nil), executor.ids...)
}

func effectIntentPersisted(ctx context.Context, runtime *kernel.Runtime, runID string, effectID string) bool {
	snapshot, err := runtime.Load(ctx, runID)
	if err != nil {
		return false
	}
	view, err := workflow.ViewState(snapshot)
	if err != nil {
		return false
	}
	for _, effect := range view.Effects {
		if effect.ID == effectID && effect.Status == workflow.EffectPending {
			return true
		}
	}
	return false
}

type workflowClock struct{}

func (workflowClock) Now() time.Time {
	return time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
}

// Concurrent queue-worker Resume now correctly obtains an original Store
// Grant before external effects, so the deterministic test ID generator
// must be safe for simultaneous preflight Grant ID requests.
type workflowIDs struct{ next atomic.Uint64 }

func (ids *workflowIDs) NewID(prefix string) (string, error) {
	return fmt.Sprintf("%s_%d", prefix, ids.next.Add(1)), nil
}

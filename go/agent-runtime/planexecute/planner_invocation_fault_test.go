package planexecute_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/planexecute"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

var errInjectedPlannerCrash = errors.New("injected planner crash")

func TestPlannerInvocationRecoversCrashAfterStartCommitBeforePlannerCall(t *testing.T) {
	t.Parallel()
	base := memory.NewStore()
	faults := &plannerFaultStore{Store: base, eventType: "planexecute.started", afterCommit: true}
	planner := newRecordingPlanner()
	children := newFakeAgentRunner(childComplete)
	runner := newPlannerFaultRunner(t, newPlannerFaultRuntime(t, faults, planFaultClock{}), planner, children, nil)
	request := baseStartRequest(planexecute.ApprovalAuto)
	request.ID = "planner-before-call"
	_, err := runner.StartRun(t.Context(), request)
	if !errors.Is(err, errInjectedPlannerCrash) {
		t.Fatalf("start error = %v", err)
	}
	if calls := planner.callsCopy(); len(calls) != 0 {
		t.Fatalf("planner called before recovery: %#v", calls)
	}

	restarted := newPlannerFaultRunner(
		t, newPlannerFaultRuntime(t, base, planFaultClock{offset: 3 * time.Minute}), planner, children, nil,
	)
	pending := mustLoadPlanRun(t, restarted, base, request.ID)
	completed, err := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	assertPlannerConsumed(t, completed, planner, 1)
}

type blockingPlanner struct {
	recordingPlanner
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingPlanner() *blockingPlanner {
	return &blockingPlanner{entered: make(chan struct{}), release: make(chan struct{})}
}

func (planner *blockingPlanner) GeneratePlan(
	_ context.Context,
	request planexecute.PlannerRequest,
) (planexecute.PlannerResponse, error) {
	cloned := request
	cloned.AllowedToolKeys = append([]string(nil), request.AllowedToolKeys...)
	planner.mu.Lock()
	planner.calls = append(planner.calls, cloned)
	planner.mu.Unlock()
	planner.once.Do(func() { close(planner.entered) })
	<-planner.release
	return planexecute.PlannerResponse{
		ResponseID: "planner-response",
		Draft: planexecute.PlanDraft{
			Summary: "Durable plan", Steps: []planexecute.StepDraft{{Title: "Execute", Goal: "execute"}},
		},
	}, nil
}

func TestPlannerInvocationConcurrentResumeExecutesOnePhysicalCall(t *testing.T) {
	t.Parallel()
	base := memory.NewStore()
	faults := &plannerFaultStore{Store: base, eventType: "planexecute.started", afterCommit: true}
	planner := newBlockingPlanner()
	children := newFakeAgentRunner(childComplete)
	preparing := newPlannerFaultRunner(t, newPlannerFaultRuntime(t, faults, planFaultClock{}), planner, children, nil)
	request := baseStartRequest(planexecute.ApprovalAuto)
	request.ID = "planner-concurrent-resume"
	_, err := preparing.StartRun(t.Context(), request)
	if !errors.Is(err, errInjectedPlannerCrash) {
		t.Fatalf("prepare error = %v", err)
	}

	runtime := newPlannerFaultRuntime(t, base, planFaultClock{})
	pending, err := runtime.Load(t.Context(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	runnerA := newPlannerFaultRunner(t, runtime, planner, children, nil)
	runnerB := newPlannerFaultRunner(t, runtime, planner, children, nil)
	type resumeResult struct {
		snapshot kernel.Snapshot
		err      error
	}
	results := make(chan resumeResult, 1)
	go func() {
		snapshot, resumeErr := runnerA.Resume(context.Background(), pending.Run.ID, pending.Run.Revision)
		results <- resumeResult{snapshot: snapshot, err: resumeErr}
	}()
	select {
	case <-planner.entered:
	case <-time.After(time.Second):
		t.Fatal("winning planner execution did not start")
	}
	if _, secondErr := runnerB.Resume(t.Context(), pending.Run.ID, pending.Run.Revision); !errors.Is(secondErr, kernel.ErrConflict) {
		t.Fatalf("concurrent resume error = %v", secondErr)
	}
	close(planner.release)
	first := <-results
	if first.err != nil {
		t.Fatalf("winning resume failed: %v", first.err)
	}
	if got := len(planner.callsCopy()); got != 1 {
		t.Fatalf("planner physical calls = %d, want 1", got)
	}
	assertPlannerConsumed(t, first.snapshot, &planner.recordingPlanner, 1)
}

func TestPlannerInvocationRecoversCrashAfterClaimBeforePlannerCall(t *testing.T) {
	t.Parallel()
	base := memory.NewStore()
	faults := &plannerFaultStore{Store: base, eventType: "planexecute.planner.claimed", afterCommit: true}
	planner := newRecordingPlanner()
	children := newFakeAgentRunner(childComplete)
	runner := newPlannerFaultRunner(t, newPlannerFaultRuntime(t, faults, planFaultClock{}), planner, children, nil)
	request := baseStartRequest(planexecute.ApprovalAuto)
	request.ID = "planner-after-claim"
	_, err := runner.StartRun(t.Context(), request)
	if !errors.Is(err, errInjectedPlannerCrash) {
		t.Fatalf("start error = %v", err)
	}
	if calls := planner.callsCopy(); len(calls) != 0 {
		t.Fatalf("planner called after injected claim crash: %#v", calls)
	}

	runtime := newPlannerFaultRuntime(t, base, planFaultClock{offset: 3 * time.Minute})
	restarted := newPlannerFaultRunner(t, runtime, planner, children, nil)
	claimed, err := runtime.Load(t.Context(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := restarted.Resume(t.Context(), claimed.Run.ID, claimed.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	assertPlannerConsumed(t, completed, planner, 1)
}

func TestPlannerInvocationRecoversCrashAfterResponseBeforeReceipt(t *testing.T) {
	t.Parallel()
	base := memory.NewStore()
	faults := &plannerFaultStore{Store: base, eventType: "planexecute.planner.completed", afterCommit: false}
	planner := newRecordingPlanner()
	children := newFakeAgentRunner(childComplete)
	runner := newPlannerFaultRunner(t, newPlannerFaultRuntime(t, faults, planFaultClock{}), planner, children, nil)
	request := baseStartRequest(planexecute.ApprovalAuto)
	request.ID = "planner-before-receipt"
	_, err := runner.StartRun(t.Context(), request)
	if !errors.Is(err, errInjectedPlannerCrash) {
		t.Fatalf("start error = %v", err)
	}
	first := planner.callsCopy()
	if len(first) != 1 || first[0].InvocationID == "" {
		t.Fatalf("first planner calls = %#v", first)
	}

	runtime := newPlannerFaultRuntime(t, base, planFaultClock{offset: 3 * time.Minute})
	restarted := newPlannerFaultRunner(t, runtime, planner, children, nil)
	pending, err := runtime.Load(t.Context(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	calls := planner.callsCopy()
	if len(calls) != 2 || calls[0].InvocationID != calls[1].InvocationID {
		t.Fatalf("physical retry did not reuse planner invocation: %#v", calls)
	}
	assertPlannerConsumed(t, completed, planner, 2)
}

func TestPlannerInvocationRecoversCrashAfterReceiptBeforeConsumption(t *testing.T) {
	t.Parallel()
	base := memory.NewStore()
	faults := &plannerFaultStore{Store: base, eventType: "planexecute.planner.completed", afterCommit: true}
	planner := newRecordingPlanner()
	children := newFakeAgentRunner(childComplete)
	runner := newPlannerFaultRunner(t, newPlannerFaultRuntime(t, faults, planFaultClock{}), planner, children, nil)
	request := baseStartRequest(planexecute.ApprovalAuto)
	request.ID = "planner-after-receipt"
	_, err := runner.StartRun(t.Context(), request)
	if !errors.Is(err, errInjectedPlannerCrash) {
		t.Fatalf("start error = %v", err)
	}
	if got := len(planner.callsCopy()); got != 1 {
		t.Fatalf("planner calls = %d", got)
	}

	runtime := newPlannerFaultRuntime(t, base, planFaultClock{})
	restarted := newPlannerFaultRunner(t, runtime, planner, children, nil)
	receipt, err := runtime.Load(t.Context(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	view := mustView(t, receipt)
	if view.PlannerInvocation == nil || view.PlannerInvocation.Status != planexecute.PlannerInvocationCompleted {
		t.Fatalf("durable planner receipt = %#v", view.PlannerInvocation)
	}
	completed, err := restarted.Resume(t.Context(), receipt.Run.ID, receipt.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	assertPlannerConsumed(t, completed, planner, 1)
}

func TestPlanStepStartedCommitRecoversRelationAndChildAfterCrash(t *testing.T) {
	t.Parallel()
	base := memory.NewStore()
	faults := &plannerFaultStore{Store: base, eventType: "plan.step_started", afterCommit: true}
	planner := newRecordingPlanner()
	children := newFakeAgentRunner(childComplete)
	relations, err := runrelation.New(memory.NewRunRelationStore(), planFaultClock{})
	if err != nil {
		t.Fatal(err)
	}
	runner := newPlannerFaultRunner(t, newPlannerFaultRuntime(t, faults, planFaultClock{}), planner, children, relations)
	request := baseStartRequest(planexecute.ApprovalAuto)
	request.ID = "plan-step-start-crash"
	_, err = runner.StartRun(t.Context(), request)
	if !errors.Is(err, errInjectedPlannerCrash) {
		t.Fatalf("start error = %v", err)
	}
	if children.startCount != 0 {
		t.Fatalf("child started before recovery: %d", children.startCount)
	}
	if items, listErr := relations.ListChildren(t.Context(), request.ID); listErr != nil || len(items) != 0 {
		t.Fatalf("relation existed before recovery: %#v, %v", items, listErr)
	}

	runtime := newPlannerFaultRuntime(t, base, planFaultClock{})
	restarted := newPlannerFaultRunner(t, runtime, planner, children, relations)
	crashed, err := runtime.Load(t.Context(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := restarted.Resume(t.Context(), crashed.Run.ID, crashed.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	assertCompletedPlan(t, completed, 1)
	if children.startCount != 1 {
		t.Fatalf("recovery child starts = %d", children.startCount)
	}
	items, err := relations.ListChildren(t.Context(), request.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("recovered relation = %#v, %v", items, err)
	}
}

func TestPlanStepCompletedCommitResumesWithoutDuplicateChild(t *testing.T) {
	t.Parallel()
	base := memory.NewStore()
	faults := &plannerFaultStore{Store: base, eventType: "plan.step_completed", afterCommit: true}
	planner := newRecordingPlanner()
	children := newFakeAgentRunner(childComplete)
	runner := newPlannerFaultRunner(t, newPlannerFaultRuntime(t, faults, planFaultClock{}), planner, children, nil)
	request := baseStartRequest(planexecute.ApprovalAuto)
	request.ID = "plan-step-complete-crash"
	_, err := runner.StartRun(t.Context(), request)
	if !errors.Is(err, errInjectedPlannerCrash) {
		t.Fatalf("start error = %v", err)
	}
	if children.startCount != 1 {
		t.Fatalf("child starts before recovery = %d", children.startCount)
	}

	runtime := newPlannerFaultRuntime(t, base, planFaultClock{})
	restarted := newPlannerFaultRunner(t, runtime, planner, children, nil)
	crashed, err := runtime.Load(t.Context(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := restarted.Resume(t.Context(), crashed.Run.ID, crashed.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	assertCompletedPlan(t, completed, 1)
	if children.startCount != 1 {
		t.Fatalf("recovery duplicated child: %d", children.startCount)
	}
}

type plannerFaultStore struct {
	kernel.Store
	mu          sync.Mutex
	eventType   string
	afterCommit bool
	failed      bool
}

func (store *plannerFaultStore) Create(
	ctx context.Context,
	record kernel.Record,
	events []kernel.EventDraft,
) (kernel.Snapshot, error) {
	shouldFail := store.shouldFail(events)
	if !shouldFail {
		return store.Store.Create(ctx, record, events)
	}
	if !store.afterCommit {
		return kernel.Snapshot{}, errInjectedPlannerCrash
	}
	snapshot, err := store.Store.Create(ctx, record, events)
	if err != nil {
		return snapshot, err
	}
	return kernel.Snapshot{}, errInjectedPlannerCrash
}

func (store *plannerFaultStore) Apply(
	ctx context.Context,
	runID string,
	expectedRevision uint64,
	mutation kernel.StoreMutation,
) (kernel.Snapshot, error) {
	shouldFail := store.shouldFail(mutation.Events)
	if !shouldFail {
		return store.Store.Apply(ctx, runID, expectedRevision, mutation)
	}
	if !store.afterCommit {
		return kernel.Snapshot{}, errInjectedPlannerCrash
	}
	snapshot, err := store.Store.Apply(ctx, runID, expectedRevision, mutation)
	if err != nil {
		return snapshot, err
	}
	return kernel.Snapshot{}, errInjectedPlannerCrash
}

func (store *plannerFaultStore) shouldFail(events []kernel.EventDraft) bool {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.failed {
		return false
	}
	for _, event := range events {
		if event.Type == store.eventType {
			store.failed = true
			return true
		}
	}
	return false
}

type recordingPlanner struct {
	mu    sync.Mutex
	calls []planexecute.PlannerRequest
}

func newRecordingPlanner() *recordingPlanner { return &recordingPlanner{} }

func (planner *recordingPlanner) GeneratePlan(
	_ context.Context,
	request planexecute.PlannerRequest,
) (planexecute.PlannerResponse, error) {
	cloned := request
	cloned.AllowedToolKeys = append([]string(nil), request.AllowedToolKeys...)
	planner.mu.Lock()
	planner.calls = append(planner.calls, cloned)
	planner.mu.Unlock()
	return planexecute.PlannerResponse{
		ResponseID: "planner-response",
		Draft: planexecute.PlanDraft{
			Summary: "Durable plan", Steps: []planexecute.StepDraft{{Title: "Execute", Goal: "execute"}},
		},
	}, nil
}

func (planner *recordingPlanner) callsCopy() []planexecute.PlannerRequest {
	planner.mu.Lock()
	defer planner.mu.Unlock()
	result := append([]planexecute.PlannerRequest(nil), planner.calls...)
	for index := range result {
		result[index].AllowedToolKeys = append([]string(nil), result[index].AllowedToolKeys...)
	}
	return result
}

type planFaultClock struct{ offset time.Duration }

func (clock planFaultClock) Now() time.Time { return planClock{}.Now().Add(clock.offset) }

func newPlannerFaultRuntime(t *testing.T, store kernel.Store, clock kernel.Clock) *kernel.Runtime {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: store, Clock: clock, IDs: &planIDs{}})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func newPlannerFaultRunner(
	t *testing.T,
	runtime *kernel.Runtime,
	planner planexecute.Planner,
	children *fakeAgentRunner,
	relations runrelation.Recorder,
) *planexecute.Runner {
	t.Helper()
	runner, err := planexecute.NewRunner(planexecute.Dependencies{
		Runtime: runtime, Planner: planner, Agent: children, Relations: relations, MaxSteps: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func mustLoadPlanRun(t *testing.T, runner *planexecute.Runner, store kernel.Store, runID string) kernel.Snapshot {
	t.Helper()
	runtime := newPlannerFaultRuntime(t, store, planFaultClock{})
	snapshot, err := runtime.Load(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	_ = runner
	return snapshot
}

func assertPlannerConsumed(
	t *testing.T,
	snapshot kernel.Snapshot,
	planner *recordingPlanner,
	wantCalls int,
) {
	t.Helper()
	if snapshot.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("plan run status = %s", snapshot.Run.Status)
	}
	view := mustView(t, snapshot)
	if view.PlannerInvocation == nil || view.PlannerInvocation.Status != planexecute.PlannerInvocationConsumed ||
		view.PlannerInvocation.ResponseID != "planner-response" || view.PlannerInvocation.CompletedAt == nil ||
		view.PlannerInvocation.ConsumedAt == nil || view.PlannerInvocation.RequestHash == "" ||
		view.PlannerInvocation.Request.InvocationID != view.PlannerInvocation.ID {
		t.Fatalf("planner invocation = %#v", view.PlannerInvocation)
	}
	if got := len(planner.callsCopy()); got != wantCalls {
		t.Fatalf("planner calls = %d, want %d", got, wantCalls)
	}
}

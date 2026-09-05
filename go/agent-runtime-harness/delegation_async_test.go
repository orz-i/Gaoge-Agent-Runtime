package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestAsyncDelegationSurvivesPendingPollsAndRunnerRecreation(t *testing.T) {
	t.Parallel()
	fixture := newAsyncDelegationFixture(t)
	runner, direct := fixture.compose(t, nil)
	started := fixture.start(t, runner)
	rootID := snapshotExecutionRef(t, started)
	for range 2 {
		current, err := fixture.runtime.Load(t.Context(), rootID)
		if err != nil {
			t.Fatal(err)
		}
		pending, err := direct.Resume(t.Context(), rootID, current.Run.Revision)
		if err != nil || pending.Run.Status != kernel.RunStatusRunning {
			t.Fatalf("pending resume = %#v, %v", pending.Run, err)
		}
		view, err := agent.ViewState(pending)
		if err != nil || view.Budget.Usage.ToolCalls != 0 {
			t.Fatalf("pending call consumed budget: %#v, %v", view.Budget, err)
		}
		runner, direct = fixture.compose(t, nil)
	}
	fixture.finishChild(t, kernel.RunStatusCompleted)
	current, err := fixture.runtime.Load(t.Context(), rootID)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := direct.Resume(t.Context(), rootID, current.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("completed resume = %#v, %v", completed.Run, err)
	}
	fixture.assertTerminalItems(t, runner, started.Turn.ID, harness.ItemCompleted)
	fixture.assertSinglePreparedChild(t)
	requests := fixture.model.requestsCopy()
	if len(requests) != 2 {
		t.Fatalf("model resumed before child completion: %d calls", len(requests))
	}
	last := requests[1].Messages[len(requests[1].Messages)-1]
	var output struct {
		Result string `json:"result"`
	}
	if json.Unmarshal([]byte(last.Content), &output) != nil || output.Result != delegationTestSpecialistResult {
		t.Fatalf("late child result missing from parent transcript: %s", last.Content)
	}
}

func TestAsyncDelegationRecordsChildFailureAndCancellation(t *testing.T) {
	t.Parallel()
	for _, status := range []kernel.RunStatus{kernel.RunStatusFailed, kernel.RunStatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			fixture := newAsyncDelegationFixture(t)
			runner, direct := fixture.compose(t, nil)
			started := fixture.start(t, runner)
			fixture.finishChild(t, status)
			root, err := fixture.runtime.Load(t.Context(), snapshotExecutionRef(t, started))
			if err != nil {
				t.Fatal(err)
			}
			failed, err := direct.Resume(t.Context(), root.Run.ID, root.Run.Revision)
			if !errors.Is(err, handoff.ErrChildFailed) || failed.Run.Status != kernel.RunStatusFailed {
				t.Fatalf("failed child = %#v, %v", failed.Run, err)
			}
			fixture.assertTerminalItems(t, runner, started.Turn.ID, harness.ItemStatus(status))
		})
	}
}

func TestAsyncDelegationCancellationPreservesChildTerminalOutcome(t *testing.T) {
	t.Parallel()
	for _, status := range []kernel.RunStatus{kernel.RunStatusCompleted, kernel.RunStatusFailed, kernel.RunStatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			fixture := newAsyncDelegationFixture(t)
			runner, _ := fixture.compose(t, nil)
			started := fixture.start(t, runner)
			fixture.finishChild(t, status)
			cancelled, err := runner.Cancel(t.Context(), started.Turn.ID, "stop")
			if err != nil || cancelled.Turn.Status != harness.TurnCancelled {
				t.Fatalf("parent cancellation = %#v, %v", cancelled.Turn, err)
			}
			fixture.assertTerminalItems(t, runner, started.Turn.ID, harness.ItemStatus(status))
		})
	}
}

func TestAsyncDelegationCancellationRetriesExternalCleanup(t *testing.T) {
	t.Parallel()
	fixture := newAsyncDelegationFixture(t)
	canceller := &asyncDelegationCanceller{runtime: fixture.runtime}
	runner, _ := fixture.compose(t, canceller)
	started := fixture.start(t, runner)
	if _, err := runner.Cancel(t.Context(), started.Turn.ID, "stop"); !errors.Is(err, errAsyncCancelUnavailable) {
		t.Fatalf("remote cancellation error was lost: %v", err)
	}
	child, err := fixture.runtime.Load(t.Context(), fixture.child.id)
	if err != nil || child.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("failed remote cleanup cancelled local shadow: %#v, %v", child.Run, err)
	}
	if _, err = runner.Refresh(t.Context(), started.Turn.ID); err != nil {
		t.Fatal(err)
	}
	runner, _ = fixture.compose(t, canceller)
	for range 2 {
		cancelled, cancelErr := runner.Cancel(t.Context(), started.Turn.ID, "stop")
		if cancelErr != nil || cancelled.Turn.Status != harness.TurnCancelled {
			t.Fatalf("retried cancellation = %#v, %v", cancelled.Turn, cancelErr)
		}
	}
	if canceller.childCalls != 2 {
		t.Fatalf("remote cleanup calls = %d", canceller.childCalls)
	}
	fixture.assertTerminalItems(t, runner, started.Turn.ID, harness.ItemCancelled)
}

type asyncDelegationFixture struct {
	runtime   *kernel.Runtime
	store     harness.Store
	relations *runrelation.Registry
	child     *asyncDelegationChild
	model     *delegationModel
	policy    *asyncDelegationPolicy
}

func newAsyncDelegationFixture(t *testing.T) *asyncDelegationFixture {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), delegationTestClock{})
	if err != nil {
		t.Fatal(err)
	}
	return &asyncDelegationFixture{
		runtime: runtime, store: harness.NewMemoryStore(), relations: relations,
		child: &asyncDelegationChild{runtime: runtime}, model: &delegationModel{}, policy: &asyncDelegationPolicy{},
	}
}

func (fixture *asyncDelegationFixture) compose(t *testing.T, canceller harness.RuntimeCanceller) (*harness.Runner, *agent.Runner) {
	t.Helper()
	handler := harness.NewDelegationToolHandler(fixture.policy)
	registry, err := tools.NewRegistry([]tools.Registration{harness.DelegationToolRegistration(handler)})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := harness.NewFrozenApprovalPolicy(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	timeline, err := harness.NewToolTimelineMiddleware(fixture.store, delegationTestClock{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dependencies := delegationAgentDependencies(fixture.runtime, fixture.model, registry, policy, 4, false)
	dependencies.ToolMiddleware = []plugin.ToolMiddleware{timeline}
	direct, err := agent.NewRunner(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	handoffs, err := handoff.New(fixture.child)
	if err != nil {
		t.Fatal(err)
	}
	harnessDependencies := delegationHarnessDependencies(fixture.runtime, direct, fixture.store, registry, handoffs, fixture.relations, false)
	harnessDependencies.Cancellation = canceller
	runner, err := harness.NewRunner(harnessDependencies)
	if err != nil {
		t.Fatal(err)
	}
	if err = handler.Bind(runner); err != nil {
		t.Fatal(err)
	}
	return runner, direct
}

func (fixture *asyncDelegationFixture) start(t *testing.T, runner *harness.Runner) harness.Snapshot {
	t.Helper()
	snapshot, err := runner.Start(t.Context(), harness.StartRequest{
		HostThread: harness.HostRef{Kind: testThreadKind, ID: "async-thread"},
		HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "async-turn"},
		Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
		Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "async-thread"}, Goal: delegationTestRootGoal,
		Config: harness.ConfigSnapshot{Model: delegationTestModelName, ToolKeys: []string{harness.DelegationToolKey},
			ToolPolicies: []harness.ToolPolicySnapshot{harness.DelegationToolPolicySnapshot()}},
	})
	if err != nil || snapshot.Turn.Status != harness.TurnRunning {
		t.Fatalf("async delegation start = %#v, %v", snapshot.Turn, err)
	}
	return snapshot
}

func (fixture *asyncDelegationFixture) finishChild(t *testing.T, status kernel.RunStatus) {
	t.Helper()
	child, err := fixture.runtime.Load(t.Context(), fixture.child.id)
	if err != nil {
		t.Fatal(err)
	}
	mutation := kernel.Mutation{Status: status, State: child.State,
		Events: []kernel.EventDraft{{Type: "test.child.terminal"}}}
	if status == kernel.RunStatusCompleted {
		mutation.Result = &kernel.Result{ContentType: "text/plain", Content: json.RawMessage(`"specialist result"`)}
	} else {
		mutation.ErrorCode, mutation.ErrorDetail = "test.child.terminal", string(status)
	}
	if _, err = fixture.runtime.Apply(t.Context(), child.Run.ID, child.Run.Revision, mutation); err != nil {
		t.Fatal(err)
	}
}

func (fixture *asyncDelegationFixture) assertTerminalItems(t *testing.T, runner *harness.Runner, turnID string, status harness.ItemStatus) {
	t.Helper()
	snapshot, err := runner.Refresh(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	items := itemsOfKind(snapshot.Items, harness.ItemDelegation)
	if len(items) != 2 || items[0].Status != harness.ItemStarted || items[1].Status != status || items[1].ParentItemID != items[0].ID {
		t.Fatalf("delegation lifecycle = %#v", items)
	}
}

func (fixture *asyncDelegationFixture) assertSinglePreparedChild(t *testing.T) {
	t.Helper()
	if fixture.child.starts != 1 || fixture.policy.calls != 1 || fixture.child.goal != "analyze the evidence\n\nhost-frozen evidence" {
		t.Fatalf("child starts=%d policy calls=%d goal=%q", fixture.child.starts, fixture.policy.calls, fixture.child.goal)
	}
}

type asyncDelegationChild struct {
	runtime  *kernel.Runtime
	id, goal string
	starts   int
}

func (child *asyncDelegationChild) StartRun(ctx context.Context, request agent.StartRequest) (kernel.Snapshot, error) {
	child.starts++
	child.id, child.goal = request.ID, request.Goal
	return child.runtime.Create(ctx, kernel.CreateRequest{ID: request.ID, Kind: "test.async-child",
		Actor: request.Actor, Thread: request.Thread, RequestID: request.RequestID, Goal: request.Goal, State: json.RawMessage(`{}`)})
}

func (child *asyncDelegationChild) LoadRun(ctx context.Context, runID string) (kernel.Snapshot, error) {
	return child.runtime.Load(ctx, runID)
}

type asyncDelegationPolicy struct{ calls int }

func (policy *asyncDelegationPolicy) PrepareDelegation(_ context.Context, _ tools.ExecutionRequest, input harness.DelegateRequest) (harness.DelegateRequest, error) {
	policy.calls++
	input.Goal += "\n\nhost-frozen evidence"
	return input, nil
}

var errAsyncCancelUnavailable = errors.New("remote cancellation temporarily unavailable")

type asyncDelegationCanceller struct {
	runtime    *kernel.Runtime
	childCalls int
}

func (canceller *asyncDelegationCanceller) Cancel(ctx context.Context, runID string, revision uint64, reason string) (kernel.Snapshot, error) {
	snapshot, err := canceller.runtime.Load(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if snapshot.Run.Kind == "test.async-child" {
		canceller.childCalls++
		if canceller.childCalls == 1 {
			return kernel.Snapshot{}, errAsyncCancelUnavailable
		}
	}
	return canceller.runtime.Cancel(ctx, runID, revision, reason)
}

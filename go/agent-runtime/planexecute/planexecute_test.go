package planexecute_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/planexecute"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runrelation"
)

var errChildFailed = errors.New("child failed")

const draftStepTitle = "Draft"

func TestRequiredApprovalExecutesPlanSteps(t *testing.T) {
	t.Parallel()
	runtime := newRuntime(t)
	children := newFakeAgentRunner(childComplete)
	runner := newPlanRunner(t, runtime, children, planexecute.PlanDraft{
		Summary: "Collect and summarize",
		Steps: []planexecute.StepDraft{
			{Title: "Collect", Goal: "collect evidence", ToolKeys: []string{"search"}},
			{Title: "Summarize", Goal: "summarize evidence"},
		},
	})

	waiting, err := runner.StartRun(context.Background(), baseStartRequest(planexecute.ApprovalRequired))
	if err != nil {
		t.Fatalf("start planexecute run: %v", err)
	}
	if waiting.Run.Status != kernel.RunStatusWaitingInput || waiting.Checkpoint == nil {
		t.Fatalf("expected plan approval wait, got %#v", waiting)
	}
	waitingView := mustView(t, waiting)
	if waitingView.Plan.Status != planexecute.PlanProposed || len(waitingView.Plan.Steps) != 2 {
		t.Fatalf("unexpected proposed plan: %#v", waitingView)
	}

	completed, err := runner.ResolveApproval(
		context.Background(), waiting.Run.ID, waiting.Run.Revision,
		planexecute.ApprovalResponse{Decision: planexecute.DecisionApprove},
	)
	if err != nil {
		t.Fatalf("approve plan: %v", err)
	}
	assertCompletedPlan(t, completed, 2)
	if children.startCount != 2 {
		t.Fatalf("expected two child starts, got %d", children.startCount)
	}
}

func TestDeferredApprovalRequiresExplicitResume(t *testing.T) {
	t.Parallel()
	runtime := newRuntime(t)
	children := newFakeAgentRunner(childComplete)
	runner, err := planexecute.NewRunner(planexecute.Dependencies{
		Runtime: runtime, Planner: staticPlanner{draft: planexecute.PlanDraft{
			Summary: "Deferred plan", Steps: []planexecute.StepDraft{{Title: draftStepTitle, Goal: "draft"}},
		}},
		Agent: children, MaxSteps: 4, DeferResumption: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := runner.StartRun(t.Context(), baseStartRequest(planexecute.ApprovalRequired))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := runner.ResolveApproval(t.Context(), waiting.Run.ID, waiting.Run.Revision,
		planexecute.ApprovalResponse{Decision: planexecute.DecisionApprove})
	if err != nil || resolved.Run.Status != kernel.RunStatusRunning || children.startCount != 0 {
		t.Fatalf("resolved=%#v starts=%d err=%v", resolved.Run, children.startCount, err)
	}
	completed, err := runner.Resume(t.Context(), resolved.Run.ID, resolved.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	assertCompletedPlan(t, completed, 1)
}

func TestAutoApprovalResumesExistingChildWithoutDuplicateStart(t *testing.T) {
	t.Parallel()
	runtime := newRuntime(t)
	children := newFakeAgentRunner(childPending)
	runner := newPlanRunner(t, runtime, children, planexecute.PlanDraft{
		Summary: "One recoverable step",
		Steps:   []planexecute.StepDraft{{Title: draftStepTitle, Goal: "draft result"}},
	})

	pending, err := runner.StartRun(context.Background(), baseStartRequest(planexecute.ApprovalAuto))
	if !errors.Is(err, planexecute.ErrStepPending) {
		t.Fatalf("expected pending child, got %v", err)
	}
	view := mustView(t, pending)
	childRunID := view.Plan.Steps[0].ChildRunID
	if childRunID == "" || children.startCount != 1 {
		t.Fatalf("unexpected pending child state: %#v starts=%d", view, children.startCount)
	}
	children.complete(childRunID, json.RawMessage(`"recovered"`))

	completed, err := runner.Resume(context.Background(), pending.Run.ID, pending.Run.Revision)
	if err != nil {
		t.Fatalf("resume planexecute run: %v", err)
	}
	assertCompletedPlan(t, completed, 1)
	if children.startCount != 1 {
		t.Fatalf("resume duplicated child start: %d", children.startCount)
	}
}

func TestChildFailureFailsPlanExecuteRun(t *testing.T) {
	t.Parallel()
	runtime := newRuntime(t)
	children := newFakeAgentRunner(childFail)
	runner := newPlanRunner(t, runtime, children, planexecute.PlanDraft{
		Summary: "Failing step",
		Steps:   []planexecute.StepDraft{{Title: "Fail", Goal: "fail now"}},
	})

	failed, err := runner.StartRun(context.Background(), baseStartRequest(planexecute.ApprovalAuto))
	if !errors.Is(err, planexecute.ErrStepFailure) {
		t.Fatalf("expected step failure, got %v", err)
	}
	if failed.Run.Status != kernel.RunStatusFailed || failed.Run.ErrorCode != "planexecute.step_failed" {
		t.Fatalf("unexpected failed run: %#v", failed)
	}
	view := mustView(t, failed)
	if view.Plan.Status != planexecute.PlanFailed || view.Plan.Steps[0].Status != planexecute.StepFailed {
		t.Fatalf("unexpected failed plan: %#v", view)
	}
}

func TestPlanRejectsStepToolsOutsideFrozenAllowlist(t *testing.T) {
	t.Parallel()
	runtime := newRuntime(t)
	children := newFakeAgentRunner(childComplete)
	runner := newPlanRunner(t, runtime, children, planexecute.PlanDraft{
		Summary: "Escalate privileges",
		Steps: []planexecute.StepDraft{{
			Title: "Use hidden tool", Goal: "mutate protected state", ToolKeys: []string{"story.admin"},
		}},
	})
	request := baseStartRequest(planexecute.ApprovalAuto)
	request.AllowedToolKeys = []string{"story.read"}
	failed, err := runner.StartRun(t.Context(), request)
	if !errors.Is(err, planexecute.ErrInvalidPlan) {
		t.Fatalf("outside-allowlist plan error = %v", err)
	}
	if failed.Run.Status != kernel.RunStatusFailed || children.startCount != 0 {
		t.Fatalf("outside-allowlist plan started a child: run=%#v starts=%d", failed.Run, children.startCount)
	}
}

func TestPlanStepRecordsStableChildRelation(t *testing.T) {
	t.Parallel()
	runtime := newRuntime(t)
	children := newFakeAgentRunner(childComplete)
	relations, err := runrelation.New(memory.NewRunRelationStore(), planClock{})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := planexecute.NewRunner(planexecute.Dependencies{
		Runtime: runtime, Planner: staticPlanner{draft: planexecute.PlanDraft{
			Summary: "Related step", Steps: []planexecute.StepDraft{{Title: draftStepTitle, Goal: "draft"}},
		}},
		Agent: children, Relations: relations, MaxSteps: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := runner.StartRun(t.Context(), baseStartRequest(planexecute.ApprovalAuto))
	if err != nil {
		t.Fatal(err)
	}
	view := mustView(t, completed)
	items, err := relations.ListChildren(t.Context(), completed.Run.ID)
	if err != nil || len(items) != 1 || items[0].Kind != runrelation.KindPlanStep ||
		items[0].OwnerNodeID != view.Plan.Steps[0].ID || items[0].ChildRunID != view.Plan.Steps[0].ChildRunID {
		t.Fatalf("relations = %#v, err=%v", items, err)
	}
}

func baseStartRequest(policy planexecute.ApprovalPolicy) planexecute.StartRequest {
	return planexecute.StartRequest{
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "produce an answer", ApprovalPolicy: policy, MaxSteps: 4,
	}
}

func newRuntime(t *testing.T) *kernel.Runtime {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{
		Store: memory.NewStore(), Clock: planClock{}, IDs: &planIDs{},
	})
	if err != nil {
		t.Fatalf("create kernel: %v", err)
	}
	return runtime
}

func newPlanRunner(
	t *testing.T,
	runtime *kernel.Runtime,
	children *fakeAgentRunner,
	draft planexecute.PlanDraft,
) *planexecute.Runner {
	t.Helper()
	runner, err := planexecute.NewRunner(planexecute.Dependencies{
		Runtime: runtime, Planner: staticPlanner{draft: draft}, Agent: children, MaxSteps: 8,
	})
	if err != nil {
		t.Fatalf("create planexecute runner: %v", err)
	}
	return runner
}

func mustView(t *testing.T, snapshot kernel.Snapshot) planexecute.View {
	t.Helper()
	view, err := planexecute.ViewState(snapshot)
	if err != nil {
		t.Fatalf("decode planexecute view: %v", err)
	}
	return view
}

func assertCompletedPlan(t *testing.T, snapshot kernel.Snapshot, stepCount int) {
	t.Helper()
	if snapshot.Run.Status != kernel.RunStatusCompleted || snapshot.Result == nil {
		t.Fatalf("expected completed planexecute run: %#v", snapshot)
	}
	view := mustView(t, snapshot)
	if view.Plan.Status != planexecute.PlanCompleted || view.NextStep != stepCount {
		t.Fatalf("unexpected completed plan: %#v", view)
	}
	for _, step := range view.Plan.Steps {
		if step.Status != planexecute.StepCompleted || step.ChildRunID == "" || len(step.Result) == 0 {
			t.Fatalf("unexpected completed step: %#v", step)
		}
	}
}

type staticPlanner struct {
	draft planexecute.PlanDraft
}

func (planner staticPlanner) GeneratePlan(
	context.Context,
	planexecute.PlannerRequest,
) (planexecute.PlanDraft, error) {
	return planner.draft, nil
}

type childBehavior int

const (
	childComplete childBehavior = iota
	childPending
	childFail
)

type fakeAgentRunner struct {
	behavior   childBehavior
	runs       map[string]kernel.Snapshot
	startCount int
}

func newFakeAgentRunner(behavior childBehavior) *fakeAgentRunner {
	return &fakeAgentRunner{behavior: behavior, runs: make(map[string]kernel.Snapshot)}
}

func (runner *fakeAgentRunner) StartRun(
	_ context.Context,
	request agent.StartRequest,
) (kernel.Snapshot, error) {
	runner.startCount++
	snapshot := kernel.Snapshot{Run: kernel.Run{
		ID: request.ID, Kind: agent.RunKind, Actor: request.Actor, Thread: request.Thread,
		Goal: request.Goal, Revision: 1, Status: kernel.RunStatusRunning,
	}}
	switch runner.behavior {
	case childComplete:
		snapshot.Run.Status = kernel.RunStatusCompleted
		snapshot.Result = &kernel.Result{ContentType: "text", Content: json.RawMessage(fmt.Sprintf("%q", request.Goal))}
	case childFail:
		snapshot.Run.Status = kernel.RunStatusFailed
		snapshot.Run.ErrorCode = "agent.failed"
		snapshot.Run.ErrorDetail = "child failed"
		runner.runs[request.ID] = snapshot
		return snapshot, errChildFailed
	case childPending:
	}
	runner.runs[request.ID] = snapshot
	return snapshot, nil
}

func (runner *fakeAgentRunner) LoadRun(_ context.Context, runID string) (kernel.Snapshot, error) {
	snapshot, ok := runner.runs[runID]
	if !ok {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	return snapshot, nil
}

func (runner *fakeAgentRunner) complete(runID string, result json.RawMessage) {
	snapshot := runner.runs[runID]
	snapshot.Run.Status = kernel.RunStatusCompleted
	snapshot.Result = &kernel.Result{ContentType: "text", Content: append(json.RawMessage(nil), result...)}
	runner.runs[runID] = snapshot
}

type planClock struct{}

func (planClock) Now() time.Time {
	return time.Date(2026, 8, 6, 3, 0, 0, 0, time.UTC)
}

type planIDs struct{ next int }

func (ids *planIDs) NewID(prefix string) (string, error) {
	ids.next++
	return fmt.Sprintf("%s_%d", prefix, ids.next), nil
}

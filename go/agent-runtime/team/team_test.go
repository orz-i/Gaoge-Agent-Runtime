package team_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/team"
)

const reviewGoal = "review"

func TestSequentialTeamCompletesAllMembers(t *testing.T) {
	t.Parallel()
	runner, children := newTeamRunner(t, childCompletes)
	completed, err := runner.StartRun(context.Background(), teamRequest(team.ExecutionSequential, handoff.JoinAll))
	if err != nil {
		t.Fatalf("start sequential team: %v", err)
	}
	assertCompletedTeam(t, completed, 2)
	if children.startCount() != 2 {
		t.Fatalf("expected two child starts, got %d", children.startCount())
	}
}

func TestCompletedTeamResultDoesNotExposeDelegationOrChildRunIDs(t *testing.T) {
	t.Parallel()
	runner, _ := newTeamRunner(t, childCompletes)

	snapshot, err := runner.StartRun(t.Context(), team.StartRequest{
		ID: "team-public-result", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, RequestID: "request",
		Goal: reviewGoal, Mode: team.ExecutionParallel,
		Members: []team.Member{{ID: "a", Goal: "analysis"}, {ID: "b", Goal: reviewGoal}},
		Join: handoff.Join{Mode: handoff.JoinAll, Quorum: 1, FailurePolicy: handoff.FailureCollect},
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	assertPublicTeamResult(t, snapshot)
}

func assertPublicTeamResult(t *testing.T, snapshot kernel.Snapshot) {
	t.Helper()
	if snapshot.Result == nil {
		t.Fatal("expected team result")
	}
	var result team.Result
	if err := json.Unmarshal(snapshot.Result.Content, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Kind != team.ResultKind || len(result.Members) != 2 ||
		result.Members[0].Content != "analysis" || result.Members[1].Content != reviewGoal {
		t.Fatalf("unexpected public result: %#v", result)
	}
	encoded := string(snapshot.Result.Content)
	for _, forbidden := range []string{"delegation", "childRunID", "run_"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("public result leaked internal identity %q: %s", forbidden, encoded)
		}
	}
}

func TestSequentialAnyStopsAfterFirstSuccess(t *testing.T) {
	t.Parallel()
	runner, children := newTeamRunner(t, childCompletes)
	completed, err := runner.StartRun(context.Background(), teamRequest(team.ExecutionSequential, handoff.JoinAny))
	if err != nil {
		t.Fatalf("start sequential any team: %v", err)
	}
	assertCompletedTeam(t, completed, 1)
	if children.startCount() != 1 {
		t.Fatalf("JoinAny should stop after first success, starts=%d", children.startCount())
	}
}

func TestParallelTeamResumesWithoutDuplicateChildren(t *testing.T) {
	t.Parallel()
	runner, children := newTeamRunner(t, childWaits)
	pending, err := runner.StartRun(context.Background(), teamRequest(team.ExecutionParallel, handoff.JoinAll))
	if !errors.Is(err, team.ErrMemberPending) {
		t.Fatalf("expected pending team, got %v", err)
	}
	view := mustView(t, pending)
	if children.startCount() != 2 || len(view.Members) != 2 {
		t.Fatalf("unexpected parallel fan-out: starts=%d view=%#v", children.startCount(), view)
	}
	for _, member := range view.Members {
		children.complete(member.Delegation.ChildRunID)
	}
	completed, err := runner.Resume(context.Background(), pending.Run.ID, pending.Run.Revision)
	if err != nil {
		t.Fatalf("resume parallel team: %v", err)
	}
	assertCompletedTeam(t, completed, 2)
	if children.startCount() != 2 {
		t.Fatalf("resume duplicated children: %d", children.startCount())
	}
}

func TestFailFastTeamFailsOnMemberFailure(t *testing.T) {
	t.Parallel()
	runner, _ := newTeamRunner(t, childFailsFirst)
	request := teamRequest(team.ExecutionParallel, handoff.JoinAll)
	request.Join.FailurePolicy = handoff.FailureFailFast
	failed, err := runner.StartRun(context.Background(), request)
	if !errors.Is(err, team.ErrTeamFailed) {
		t.Fatalf("expected team failure, got %v", err)
	}
	if failed.Run.Status != kernel.RunStatusFailed {
		t.Fatalf("unexpected failed team: %#v", failed)
	}
	view := mustView(t, failed)
	if view.Join.Status != handoff.JoinFailed || view.Join.Failed != 1 {
		t.Fatalf("unexpected failed join: %#v", view.Join)
	}
}

func TestTeamRecordsStableMemberRelations(t *testing.T) {
	t.Parallel()
	runner, relations := newTeamRunnerWithRelations(t)
	completed, err := runner.StartRun(t.Context(), teamRequest(team.ExecutionParallel, handoff.JoinAll))
	if err != nil {
		t.Fatal(err)
	}
	items, err := relations.ListChildren(t.Context(), completed.Run.ID)
	assertTeamRelations(t, items, err)
}

func newTeamRunnerWithRelations(t *testing.T) (*team.Runner, *runrelation.Registry) {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{
		Store: memory.NewStore(), Clock: teamClock{}, IDs: &teamIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	children := newFakeChildren(childCompletes)
	coordinator, err := handoff.New(children)
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), teamClock{})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := team.NewRunner(team.Dependencies{
		Runtime: runtime, Handoffs: coordinator, Relations: relations, MaxMembers: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner, relations
}

func assertTeamRelations(t *testing.T, items []runrelation.Relation, err error) {
	t.Helper()
	if err != nil || len(items) != 2 || items[0].Kind != runrelation.KindTeamMember ||
		items[0].OwnerNodeID != "researcher" || items[1].OwnerNodeID != "writer" {
		t.Fatalf("relations = %#v, err=%v", items, err)
	}
}

func teamRequest(mode team.ExecutionMode, joinMode handoff.JoinMode) team.StartRequest {
	return team.StartRequest{
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "produce team answer", Mode: mode,
		Members: []team.Member{
			{ID: "researcher", Goal: "research"},
			{ID: "writer", Goal: "write"},
		},
		Join: handoff.Join{Mode: joinMode, Quorum: 1, FailurePolicy: handoff.FailureCollect},
	}
}

func newTeamRunner(t *testing.T, behavior childBehavior) (*team.Runner, *fakeChildren) {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{
		Store: memory.NewStore(), Clock: teamClock{}, IDs: &teamIDs{},
	})
	if err != nil {
		t.Fatalf("create kernel: %v", err)
	}
	children := newFakeChildren(behavior)
	coordinator, err := handoff.New(children)
	if err != nil {
		t.Fatalf("create handoff: %v", err)
	}
	runner, err := team.NewRunner(team.Dependencies{
		Runtime: runtime, Handoffs: coordinator, MaxMembers: 8,
	})
	if err != nil {
		t.Fatalf("create team runner: %v", err)
	}
	return runner, children
}

func mustView(t *testing.T, snapshot kernel.Snapshot) team.View {
	t.Helper()
	view, err := team.ViewState(snapshot)
	if err != nil {
		t.Fatalf("decode team view: %v", err)
	}
	return view
}

func assertCompletedTeam(t *testing.T, snapshot kernel.Snapshot, completedMembers int) {
	t.Helper()
	if snapshot.Run.Status != kernel.RunStatusCompleted || snapshot.Result == nil {
		t.Fatalf("expected completed team: %#v", snapshot)
	}
	view := mustView(t, snapshot)
	if view.Join.Status != handoff.JoinReady || view.Join.Completed != completedMembers {
		t.Fatalf("unexpected completed join: %#v", view.Join)
	}
}

type childBehavior int

const (
	childCompletes childBehavior = iota
	childWaits
	childFailsFirst
)

type fakeChildren struct {
	mu       sync.Mutex
	behavior childBehavior
	runs     map[string]kernel.Snapshot
	starts   int
}

func newFakeChildren(behavior childBehavior) *fakeChildren {
	return &fakeChildren{behavior: behavior, runs: make(map[string]kernel.Snapshot)}
}

func (children *fakeChildren) StartRun(
	_ context.Context,
	request agent.StartRequest,
) (kernel.Snapshot, error) {
	children.mu.Lock()
	defer children.mu.Unlock()
	children.starts++
	snapshot := kernel.Snapshot{Run: kernel.Run{
		ID: request.ID, Kind: agent.RunKind, Actor: request.Actor, Thread: request.Thread,
		Goal: request.Goal, Status: kernel.RunStatusRunning, Revision: 1,
	}}
	switch {
	case children.behavior == childCompletes:
		snapshot.Run.Status = kernel.RunStatusCompleted
		snapshot.Result = &kernel.Result{ContentType: "text", Content: json.RawMessage(fmt.Sprintf("%q", request.Goal))}
	case children.behavior == childFailsFirst && children.starts == 1:
		snapshot.Run.Status = kernel.RunStatusFailed
		snapshot.Run.ErrorCode = "agent.failed"
		snapshot.Run.ErrorDetail = "member failed"
	}
	children.runs[request.ID] = snapshot
	if snapshot.Run.Status == kernel.RunStatusFailed {
		return snapshot, errMemberFailed
	}
	return snapshot, nil
}

func (children *fakeChildren) LoadRun(_ context.Context, runID string) (kernel.Snapshot, error) {
	children.mu.Lock()
	defer children.mu.Unlock()
	snapshot, ok := children.runs[runID]
	if !ok {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	return snapshot, nil
}

func (children *fakeChildren) complete(runID string) {
	children.mu.Lock()
	defer children.mu.Unlock()
	snapshot := children.runs[runID]
	snapshot.Run.Status = kernel.RunStatusCompleted
	snapshot.Result = &kernel.Result{ContentType: "text", Content: json.RawMessage(`"done"`)}
	children.runs[runID] = snapshot
}

func (children *fakeChildren) startCount() int {
	children.mu.Lock()
	defer children.mu.Unlock()
	return children.starts
}

var errMemberFailed = errors.New("member failed")

type teamClock struct{}

func (teamClock) Now() time.Time {
	return time.Date(2026, 8, 6, 11, 0, 0, 0, time.UTC)
}

type teamIDs struct{ next int }

func (ids *teamIDs) NewID(prefix string) (string, error) {
	ids.next++
	return fmt.Sprintf("%s_%d", prefix, ids.next), nil
}

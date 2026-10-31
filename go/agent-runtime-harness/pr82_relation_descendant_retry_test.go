package harness_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

// Physical Team members live in RunRelation/Kernel, not Harness child
// Invocations. A FailFast Team may terminalize while a sibling continues
// running; a settled parent Start grant does not certify sibling completion.
type pr82OrphanMemberChildren struct {
	mu   sync.Mutex
	runs map[string]kernel.Snapshot
}

func (c *pr82OrphanMemberChildren) LoadRun(_ context.Context, id string) (kernel.Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if run, ok := c.runs[id]; ok {
		return run, nil
	}
	return kernel.Snapshot{}, kernel.ErrNotFound
}
func (c *pr82OrphanMemberChildren) StartRun(_ context.Context, r agent.StartRequest) (kernel.Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	status := kernel.RunStatusRunning
	if r.Goal == "fail-fast" {
		status = kernel.RunStatusFailed
	}
	snap := kernel.Snapshot{Run: kernel.Run{ID: r.ID, Kind: agent.RunKind, Actor: r.Actor, Thread: r.Thread, Goal: r.Goal, RequestID: r.RequestID, Status: status, Revision: 1}}
	c.runs[r.ID] = snap
	return snap, nil
}

func TestPR82FailedTeamRootCannotRetryWithLiveRunRelationMember(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	relations := newFeatureInvocationRelations(t)
	children := &pr82OrphanMemberChildren{runs: map[string]kernel.Snapshot{}}
	coordinator, err := handoff.New(children)
	if err != nil {
		t.Fatal(err)
	}
	feature, err := team.NewRunner(team.Dependencies{Runtime: runtime, Handoffs: coordinator, Relations: relations, MaxMembers: 4})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store,
		Clock: featureInvocationClock{}, Teams: feature, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := runner.StartTeamTurn(t.Context(), harness.TeamTurnRequest{
		StartRequest: harness.StartRequest{
			HostThread: harness.HostRef{Kind: testThreadKind, ID: "pr82-orphan-root-thread"},
			HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "pr82-orphan-root-turn"},
			Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
			Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "pr82-orphan-root-thread"},
			RequestID:  "pr82-orphan-start", Goal: "two independent team members",
			Config: harness.ConfigSnapshot{Model: "fixture-model"},
		},
		Mode:    team.ExecutionParallel,
		Members: []team.Member{{ID: "failing", Goal: "fail-fast"}, {ID: "running", Goal: "continues"}},
		Join:    handoff.Join{Mode: handoff.JoinAll, Quorum: 1, FailurePolicy: handoff.FailureFailFast},
	})
	if !errors.Is(err, team.ErrTeamFailed) || failed.Turn.Status != harness.TurnFailed {
		t.Fatalf("expected terminal FailFast Team, got %v %s", err, failed.Turn.Status)
	}
	root, ok := harness.TopLevelInvocation(failed)
	if !ok || root.Status != harness.InvocationFailed {
		t.Fatalf("root status: %+v", root)
	}
	descendants, err := relations.ListChildren(t.Context(), root.ExecutionRefID)
	if err != nil || len(descendants) != 2 {
		t.Fatalf("missing physical member relations: %+v %v", descendants, err)
	}
	living := 0
	for _, child := range descendants {
		snap, loadErr := children.LoadRun(t.Context(), child.ChildRunID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if snap.Run.Status == kernel.RunStatusRunning {
			living++
		}
	}
	if living != 1 {
		t.Fatalf("expected exactly one member still running; got %d", living)
	}
	invocations, err := store.ListInvocations(t.Context(), failed.Turn.ID)
	if err != nil || len(invocations) != 1 {
		t.Fatalf("member unexpectedly has Harness Invocation: %+v %v", invocations, err)
	}
	items, err := store.ListItems(t.Context(), failed.Turn.ID, 0, 500)
	if err != nil || harness.PendingExecutionDispatch(items, "") {
		t.Fatalf("root dispatch grant did not settle: %v", err)
	}
	_, err = store.RetryInvocation(t.Context(), root.ID, root.Revision, "new-root-run-must-not-start", featureInvocationClock{}.Now())
	if !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("old physical member continued after root retry: %v", err)
	}
	unchanged, err := store.GetInvocation(t.Context(), root.ID)
	if err != nil || unchanged.Attempt != root.Attempt || unchanged.ExecutionRefID != root.ExecutionRefID {
		t.Fatalf("failed Retry mutated root: %+v %v", unchanged, err)
	}
}

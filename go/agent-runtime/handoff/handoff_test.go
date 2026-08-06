package handoff_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/text"
)

func TestStartOrLoadReusesStableChildRun(t *testing.T) {
	t.Parallel()
	children := newFakeChildren()
	coordinator, err := handoff.New(children)
	if err != nil {
		t.Fatalf("create handoff coordinator: %v", err)
	}
	parent := kernel.Snapshot{Run: kernel.Run{
		ID: "team_1", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
	}}
	delegation := handoff.Delegation{
		ID: "handoff_1", MemberID: "researcher", ChildRunID: "run_child_1",
		Goal: "research", Status: handoff.StatusQueued,
	}
	first, err := coordinator.StartOrLoad(context.Background(), parent, delegation)
	if !errors.Is(err, handoff.ErrChildPending) || first.Status != handoff.StatusRunning {
		t.Fatalf("expected pending child: %#v, %v", first, err)
	}
	children.complete(delegation.ChildRunID)
	second, err := coordinator.StartOrLoad(context.Background(), parent, delegation)
	if err != nil || second.Status != handoff.StatusCompleted {
		t.Fatalf("expected completed reused child: %#v, %v", second, err)
	}
	if children.starts != 1 {
		t.Fatalf("child started %d times", children.starts)
	}
}

func TestResolveJoinPolicies(t *testing.T) {
	t.Parallel()
	delegations := []handoff.Delegation{
		{ID: "a", MemberID: "a", ChildRunID: "ra", Goal: "a", Status: handoff.StatusCompleted},
		{ID: "b", MemberID: "b", ChildRunID: "rb", Goal: "b", Status: handoff.StatusFailed},
		{ID: "c", MemberID: "c", ChildRunID: "rc", Goal: "c", Status: handoff.StatusRunning},
	}
	anyJoin, err := handoff.ResolveJoin(handoff.Join{
		Mode: handoff.JoinAny, Quorum: 1, FailurePolicy: handoff.FailureCollect,
		Status: handoff.JoinPending,
	}, delegations)
	if err != nil || anyJoin.Status != handoff.JoinReady || len(anyJoin.ResultIDs) != 1 {
		t.Fatalf("unexpected any join: %#v, %v", anyJoin, err)
	}
	failFast, err := handoff.ResolveJoin(handoff.Join{
		Mode: handoff.JoinAll, Quorum: 1, FailurePolicy: handoff.FailureFailFast,
		Status: handoff.JoinPending,
	}, delegations)
	if !errors.Is(err, handoff.ErrJoinFailed) || failFast.Status != handoff.JoinFailed {
		t.Fatalf("unexpected fail-fast join: %#v, %v", failFast, err)
	}
	quorum, err := handoff.ResolveJoin(handoff.Join{
		Mode: handoff.JoinQuorum, Quorum: 2, FailurePolicy: handoff.FailureCollect,
		Status: handoff.JoinPending,
	}, delegations)
	if !errors.Is(err, handoff.ErrJoinPending) || quorum.Status != handoff.JoinPending {
		t.Fatalf("unexpected quorum join: %#v, %v", quorum, err)
	}
}

type fakeChildren struct {
	runs   map[string]kernel.Snapshot
	starts int
}

func newFakeChildren() *fakeChildren {
	return &fakeChildren{runs: make(map[string]kernel.Snapshot)}
}

func (children *fakeChildren) StartRun(
	_ context.Context,
	request text.StartRequest,
) (kernel.Snapshot, error) {
	children.starts++
	snapshot := kernel.Snapshot{Run: kernel.Run{
		ID: request.ID, Kind: kernel.RunKindText, Actor: request.Actor,
		Thread: request.Thread, Goal: request.Goal, Status: kernel.RunStatusRunning, Revision: 1,
	}}
	children.runs[request.ID] = snapshot
	return snapshot, nil
}

func (children *fakeChildren) LoadRun(_ context.Context, runID string) (kernel.Snapshot, error) {
	snapshot, ok := children.runs[runID]
	if !ok {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	return snapshot, nil
}

func (children *fakeChildren) complete(runID string) {
	snapshot := children.runs[runID]
	snapshot.Run.Status = kernel.RunStatusCompleted
	snapshot.Result = &kernel.Result{ContentType: "text", Content: json.RawMessage(`"done"`)}
	children.runs[runID] = snapshot
}

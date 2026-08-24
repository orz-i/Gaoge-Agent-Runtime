package handoff_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
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

func TestRoutedCoordinatorSelectsChildPerDelegation(t *testing.T) {
	t.Parallel()
	local := newFakeChildren()
	remote := newFakeChildren()
	coordinator, err := handoff.NewRouted(handoff.ChildRunnerResolverFunc(func(
		_ context.Context,
		delegation handoff.Delegation,
	) (handoff.ChildRunner, error) {
		if delegation.MemberID == "a2a:remote" {
			return remote, nil
		}
		return local, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if descriptor := coordinator.Descriptor(); len(descriptor.Requires) != 0 {
		t.Fatalf("routed descriptor leaked concrete requirements: %#v", descriptor)
	}
	parent := kernel.Snapshot{Run: kernel.Run{
		ID: "team_1", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
	}}
	delegation := handoff.Delegation{
		ID: "handoff_remote", MemberID: "a2a:remote", ChildRunID: "run_remote",
		Goal: "research remotely", Status: handoff.StatusQueued,
	}
	projected, err := coordinator.StartOrLoad(t.Context(), parent, delegation)
	if !errors.Is(err, handoff.ErrChildPending) || projected.Status != handoff.StatusRunning {
		t.Fatalf("projected=%#v err=%v", projected, err)
	}
	if remote.starts != 1 || local.starts != 0 {
		t.Fatalf("remote starts=%d local starts=%d", remote.starts, local.starts)
	}
}

func TestRoutedCoordinatorFailsClosedWithoutChild(t *testing.T) {
	t.Parallel()
	coordinator, err := handoff.NewRouted(handoff.ChildRunnerResolverFunc(func(
		context.Context,
		handoff.Delegation,
	) (handoff.ChildRunner, error) {
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = coordinator.Refresh(t.Context(), handoff.Delegation{
		ID: "handoff", MemberID: "unknown", ChildRunID: "run", Goal: "goal", Status: handoff.StatusRunning,
	})
	if !errors.Is(err, handoff.ErrChildUnavailable) {
		t.Fatalf("expected unavailable child, got %v", err)
	}
}

func TestResolveJoinAllCollectFailsWhenEveryDelegationFails(t *testing.T) {
	t.Parallel()
	delegations := []handoff.Delegation{
		{ID: "a", MemberID: "a", ChildRunID: "ra", Goal: "a", Status: handoff.StatusFailed},
		{ID: "b", MemberID: "b", ChildRunID: "rb", Goal: "b", Status: handoff.StatusCancelled},
	}
	join, err := handoff.ResolveJoin(handoff.Join{
		Mode: handoff.JoinAll, Quorum: 1, FailurePolicy: handoff.FailureCollect,
		Status: handoff.JoinPending,
	}, delegations)
	if !errors.Is(err, handoff.ErrJoinFailed) || join.Status != handoff.JoinFailed ||
		join.ErrorCode != "handoff.no_success" || join.Completed != 0 || join.Failed != 1 || join.Cancelled != 1 {
		t.Fatalf("unexpected all-failed collect join: %#v, %v", join, err)
	}
}

func TestResolveJoinAllCollectAllowsPartialSuccessAfterAllMembersSettle(t *testing.T) {
	t.Parallel()
	delegations := []handoff.Delegation{
		{ID: "a", MemberID: "a", ChildRunID: "ra", Goal: "a", Status: handoff.StatusCompleted},
		{ID: "b", MemberID: "b", ChildRunID: "rb", Goal: "b", Status: handoff.StatusFailed},
	}
	join, err := handoff.ResolveJoin(handoff.Join{
		Mode: handoff.JoinAll, Quorum: 1, FailurePolicy: handoff.FailureCollect,
		Status: handoff.JoinPending,
	}, delegations)
	if err != nil || join.Status != handoff.JoinReady || join.Completed != 1 || join.Failed != 1 ||
		len(join.ResultIDs) != 1 || join.ResultIDs[0] != "a" {
		t.Fatalf("unexpected partial-success collect join: %#v, %v", join, err)
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
	request agent.StartRequest,
) (kernel.Snapshot, error) {
	children.starts++
	snapshot := kernel.Snapshot{Run: kernel.Run{
		ID: request.ID, Kind: agent.RunKind, Actor: request.Actor,
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

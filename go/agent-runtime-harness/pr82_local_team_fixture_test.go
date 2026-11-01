package harness_test

import (
	"context"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

// This fixed ChildRunner actually creates and reads its members in the exact
// Kernel it attests to. Team owns the real persisted delegation and relation.
type pr82LocalTeamChildren struct{ runtime *kernel.Runtime }

func (c pr82LocalTeamChildren) OwnsKernelRuntime(rt *kernel.Runtime) bool { return c.runtime == rt }
func (c pr82LocalTeamChildren) LoadRun(ctx context.Context, id string) (kernel.Snapshot, error) {
	return c.runtime.Load(ctx, id)
}
func (c pr82LocalTeamChildren) StartRun(ctx context.Context, r agent.StartRequest) (kernel.Snapshot, error) {
	return c.runtime.Create(ctx, kernel.CreateRequest{ID: r.ID, Kind: agent.RunKind, Actor: r.Actor, Thread: r.Thread, Goal: r.Goal, RequestID: r.RequestID, State: []byte("{}")})
}
func pr82LocalTeam(t *testing.T, rt *kernel.Runtime, relations *runrelation.Registry) *team.Runner {
	t.Helper()
	coordinator, err := handoff.New(pr82LocalTeamChildren{rt})
	if err != nil {
		t.Fatal(err)
	}
	feature, err := team.NewRunner(team.Dependencies{Runtime: rt, Handoffs: coordinator, Relations: relations})
	if err != nil {
		t.Fatal(err)
	}
	return feature
}
func pr82LocalMember(t *testing.T, rt *kernel.Runtime, parent kernel.Snapshot) kernel.Snapshot {
	t.Helper()
	view, err := team.ViewState(parent)
	if err != nil || len(view.Members) != 1 {
		t.Fatalf("real Team member: %+v %v", view, err)
	}
	member, err := rt.Load(t.Context(), view.Members[0].Delegation.ChildRunID)
	if err != nil {
		t.Fatal(err)
	}
	return member
}
func pr82StartLocalTeamChild(t *testing.T, runner *harness.Runner, turnID, parentItemID string) harness.Invocation {
	t.Helper()
	snapshot, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: parentItemID, RequestID: "local-team-child", Goal: "real local team", Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "continues"}},
	})
	if err != nil && !errors.Is(err, team.ErrMemberPending) {
		t.Fatal(err)
	}
	return teamChildInvocation(t, snapshot)
}

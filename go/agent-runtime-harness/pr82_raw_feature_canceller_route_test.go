package harness

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

type pr82RawCanceller struct {
	runtime *kernel.Runtime
	calls   int
}

func (c *pr82RawCanceller) OwnsKernelRuntime(r *kernel.Runtime) bool { return c.runtime == r }
func (c *pr82RawCanceller) Cancel(ctx context.Context, id string, rev uint64, reason string) (kernel.Snapshot, error) {
	c.calls++
	return c.runtime.Cancel(ctx, id, rev, reason)
}

type pr82UnprovenRawCanceller struct{ RuntimeCanceller }

func TestPR82RawFeatureRequiresOriginalCancellerBeforeEffect(t *testing.T) {
	for _, mode := range []string{"foreign-kernel", "unproven-local", "owned-local"} {
		t.Run(mode, func(t *testing.T) {
			f := newPR82HandoffFixture(t)
			coordinator, err := handoff.New(&pr82BoundKernelChild{runtime: f.runtime})
			if err != nil {
				t.Fatal(err)
			}
			feature, err := team.NewRunner(team.Dependencies{Runtime: f.runtime, Handoffs: coordinator, Relations: f.runner.relations})
			if err != nil {
				t.Fatal(err)
			}
			f.runner.teams = feature
			f.runner.relationReader = f.runner.relations.(RunRelationSource)
			started, err := f.runner.StartTeamInvocation(t.Context(), f.turn.ID, TeamInvocationRequest{ParentItemID: f.startedItemID, RequestID: "local-team", Goal: "local team", Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "member"}}})
			if err != nil && !errors.Is(err, team.ErrMemberPending) {
				t.Fatal(err)
			}
			var parent Invocation
			for _, inv := range started.Invocations {
				if inv.ExecutionClass == ExecutionTeam {
					parent = inv
				}
			}
			parentRun, err := f.runtime.Load(t.Context(), parent.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			view, err := team.ViewState(parentRun)
			if err != nil {
				t.Fatal(err)
			}
			child, err := f.runtime.Load(t.Context(), view.Members[0].Delegation.ChildRunID)
			if err != nil {
				t.Fatal(err)
			}
			foreign, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
			if err != nil {
				t.Fatal(err)
			}
			_, err = foreign.Create(t.Context(), kernel.CreateRequest{ID: child.Run.ID, Kind: child.Run.Kind, Actor: child.Run.Actor, Thread: child.Run.Thread, RequestID: child.Run.RequestID, Goal: child.Run.Goal, State: child.State})
			if err != nil {
				t.Fatal(err)
			}
			canceller := &pr82RawCanceller{runtime: f.runtime}
			if mode == "foreign-kernel" {
				canceller.runtime = foreign
			}
			f.runner.cancellation = canceller
			if mode == "unproven-local" {
				f.runner.cancellation = pr82UnprovenRawCanceller{canceller}
			}
			_, err = f.runner.CancelSubtask(t.Context(), f.turn.ID, stableID("hsub", child.Run.ID), "stop")
			after, loadErr := foreign.Load(t.Context(), child.Run.ID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			local, loadErr := f.runtime.Load(t.Context(), child.Run.ID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if after.Run.Status != kernel.RunStatusRunning {
				t.Fatal("foreign Run changed")
			}
			if mode == "owned-local" {
				if err != nil || canceller.calls != 1 || local.Run.Status != kernel.RunStatusCancelled {
					t.Fatalf("owned local cancellation rejected: calls=%d status=%s err=%v", canceller.calls, local.Run.Status, err)
				}
			} else {
				if !errors.Is(err, ErrRuntimeCancellationUnverified) || canceller.calls != 0 || local.Run.Status != kernel.RunStatusRunning {
					t.Fatalf("unverified callback executed: calls=%d status=%s err=%v", canceller.calls, local.Run.Status, err)
				}
				items, listErr := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
				if listErr != nil || !ActiveCancellationFence(items) {
					t.Fatalf("lost unresolved cleanup intent: %v", listErr)
				}
			}
		})
	}
}

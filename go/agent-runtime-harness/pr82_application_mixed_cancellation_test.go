package harness

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

func auditMixedNativeChild(t *testing.T, f *pr82HandoffFixture, parentItem, id string) Invocation {
	t.Helper()
	raw, hash, e := marshalInvocationValue(struct{}{})
	if e != nil {
		t.Fatal(e)
	}
	child, _, _, e := f.runner.beginChildInvocation(t.Context(), f.turn.ID, parentItem, id, CapabilityAgent, "1", ExecutionAgent, raw, hash)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.runtime.Create(t.Context(), kernel.CreateRequest{ID: child.ExecutionRefID, Kind: agent.RunKind, Actor: f.parentRun.Run.Actor, Thread: f.parentRun.Run.Thread, RequestID: id, Goal: id, State: []byte(`{}`)})
	if e != nil {
		t.Fatal(e)
	}
	child.Status = InvocationRunning
	child, e = f.store.UpdateInvocation(t.Context(), child, child.Revision)
	if e != nil {
		t.Fatal(e)
	}
	return child
}
func TestAuditPR82ApplicationMixedDescendantCancellationPreservesSibling(t *testing.T) {
	f, parent, parentItem, _ := newPR82NestedApplicationParent(t, InvocationRunning)
	f.runner.cancellation = f.runtime
	f.runner.relationReader = f.runner.relations.(RunRelationSource)
	coordinator, e := handoff.New(&pr82BoundKernelChild{runtime: f.runtime})
	if e != nil {
		t.Fatal(e)
	}
	feature, e := team.NewRunner(team.Dependencies{Runtime: f.runtime, Handoffs: coordinator, Relations: f.runner.relations})
	if e != nil {
		t.Fatal(e)
	}
	f.runner.teams = feature
	started, e := f.runner.StartTeamInvocation(t.Context(), f.turn.ID, TeamInvocationRequest{
		ParentItemID: parentItem, RequestID: "mixed-native-child", Goal: "real local Team", Mode: team.ExecutionSequential,
		Members: []team.Member{{ID: "leaf", Goal: "leaf"}},
	})
	if e != nil && !errors.Is(e, team.ErrMemberPending) {
		t.Fatal(e)
	}
	var child Invocation
	for _, inv := range started.Invocations {
		if inv.ExecutionClass == ExecutionTeam {
			child = inv
		}
	}
	if child.ID == "" {
		t.Fatal("missing real Team child")
	}
	teamRun, e := f.runtime.Load(t.Context(), child.ExecutionRefID)
	if e != nil {
		t.Fatal(e)
	}
	view, e := team.ViewState(teamRun)
	if e != nil || len(view.Members) != 1 {
		t.Fatalf("member view: %+v %v", view, e)
	}
	leaf, e := f.runtime.Load(t.Context(), view.Members[0].Delegation.ChildRunID)
	if e != nil {
		t.Fatal(e)
	}
	sibling := auditMixedNativeChild(t, f, f.startedItemID, "unrelated-native-sibling")
	_, e = f.runner.completeApplicationInvocation(t.Context(), f.turn, parent, nil)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.runner.CancelInvocation(t.Context(), parent.ID, "stop subtree")
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{child.ExecutionRefID, leaf.Run.ID} {
		s, e := f.runtime.Load(t.Context(), id)
		if e != nil || s.Run.Status != kernel.RunStatusCancelled {
			t.Fatalf("target %s status=%s err=%v", id, s.Run.Status, e)
		}
	}
	s, e := f.runtime.Load(t.Context(), sibling.ExecutionRefID)
	if e != nil || s.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("unrelated sibling changed: %s %v", s.Run.Status, e)
	}
}

type auditIntermediateLateClaimStore struct {
	Store
	childID   string
	after     bool
	attempted bool
	claimErr  error
}

func (s *auditIntermediateLateClaimStore) claim(ctx context.Context) {
	s.attempted = true
	v, e := s.GetInvocation(ctx, s.childID)
	if e != nil {
		s.claimErr = e
		return
	}
	v.Status = InvocationRunning
	_, s.claimErr = s.UpdateInvocation(ctx, v, v.Revision)
}
func (s *auditIntermediateLateClaimStore) AppendItem(ctx context.Context, v Item) (Item, bool, error) {
	inject := !s.attempted && v.Kind == ItemDiagnostic && v.Status == ItemStarted && v.ParentItemID == CancellationFenceParentItemID
	if inject && !s.after {
		s.claim(ctx)
	}
	a, b, e := s.Store.AppendItem(ctx, v)
	if inject && s.after && e == nil {
		s.claim(ctx)
	}
	return a, b, e
}
func TestAuditPR82IntermediateApplicationLateClaimFence(t *testing.T) {
	for _, after := range []bool{false, true} {
		name := "before"
		if after {
			name = "after"
		}
		t.Run(name, func(t *testing.T) {
			f, parent, parentItem, _ := newPR82NestedApplicationParent(t, InvocationRunning)
			raw, hash, e := marshalInvocationValue(applicationInvocationInput{Goal: "late child", Input: []byte(`{}`)})
			if e != nil {
				t.Fatal(e)
			}
			child, _, _, e := f.runner.beginChildInvocation(t.Context(), f.turn.ID, parentItem, "late-child", "app.child", "1", ExecutionApplication, raw, hash)
			if e != nil {
				t.Fatal(e)
			}
			_, e = f.runner.completeApplicationInvocation(t.Context(), f.turn, parent, nil)
			if e != nil {
				t.Fatal(e)
			}
			s := &auditIntermediateLateClaimStore{Store: f.store, childID: child.ID, after: after}
			f.runner.store = s
			_, e = f.runner.CancelInvocation(t.Context(), parent.ID, "stop subtree")
			if !s.attempted {
				t.Fatal("missing injection")
			}
			if after {
				if !errors.Is(s.claimErr, ErrConflict) || e != nil {
					t.Fatalf("after intent: claim=%v cancel=%v", s.claimErr, e)
				}
			} else if s.claimErr != nil || !errors.Is(e, ErrApplicationOutcomeUnknown) {
				t.Fatalf("before intent: claim=%v cancel=%v", s.claimErr, e)
			}
		})
	}
}

package harness

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

func TestPR82HandoffParentRequiresOwnedLocalObservation(t *testing.T) {
	for _, role := range []bool{false, true} {
		for _, mode := range []string{"remote-foreign", "remote-identical", "dynamic-local", "fixed-local", "late-route-change"} {
			t.Run(mode+map[bool]string{false: "/member", true: "/role"}[role], func(t *testing.T) {
				f := newPR82HandoffFixture(t)
				d := f.delegation()
				if !role {
					d.RoleID = ""
				}
				started, err := f.runner.recordDelegationItem(t.Context(), f.turn, f.parent, d, ItemStarted, "")
				if err != nil {
					t.Fatal(err)
				}
				if err = f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, d, started); err != nil {
					t.Fatal(err)
				}
				coordinator, err := handoff.New(&pr82BoundKernelChild{runtime: f.runtime})
				if err != nil {
					t.Fatal(err)
				}
				if mode == "fixed-local" || mode == "late-route-change" {
					f.runner.handoffs = coordinator
				}
				_, err = f.runner.dispatchHandoff(t.Context(), f.turn, f.parentRun, d, started)
				if !errors.Is(err, handoff.ErrChildPending) {
					t.Fatal(err)
				}
				child, err := f.store.GetInvocationByExecutionRefID(t.Context(), d.ChildRunID)
				if err != nil {
					t.Fatal(err)
				}
				parentItem := invocationLifecycleItemID(child, child.Status, child.Revision)
				if mode != "fixed-local" && mode != "late-route-change" {
					req := kernel.CreateRequest{ID: d.ChildRunID, Kind: agent.RunKind, Actor: f.parentRun.Run.Actor, Thread: f.parentRun.Run.Thread, RequestID: f.parentRun.Run.ID + ":" + d.ID, Goal: d.Goal, State: []byte("{}")}
					if mode == "remote-foreign" {
						req.Kind = "foreign"
						req.Actor = kernel.ActorRef{TenantID: "foreign", ActorID: "foreign"}
					}
					if _, err = f.runtime.Create(t.Context(), req); err != nil {
						t.Fatal(err)
					}
					if mode == "dynamic-local" {
						f.runner.handoffs, err = handoff.NewRouted(handoff.ChildRunnerResolverFunc(func(context.Context, handoff.Delegation) (handoff.ChildRunner, error) {
							return &pr82BoundKernelChild{runtime: f.runtime}, nil
						}))
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				effects := &pr82NestedEffectCounter{}
				f.runner.applications = effects
				if mode == "late-route-change" {
					wrapped := &pr82HandoffInjectionStore{Store: f.store}
					wrapped.onCreate = func(_ context.Context, inv Invocation) error {
						if inv.ExecutionClass == ExecutionApplication {
							f.runner.handoffs = f.handoff
						}
						return nil
					}
					f.runner.store = wrapped
				}
				_, err = f.runner.StartApplicationInvocation(t.Context(), f.turn.ID, ApplicationInvocationRequest{ParentItemID: parentItem, RequestID: "nested-app", Goal: "nested effect", CapabilityKey: "app.grandchild", DefinitionVersion: "1"})
				if mode == "fixed-local" {
					if err != nil || effects.calls != 1 {
						t.Fatalf("valid local parent rejected: effects=%d err=%v", effects.calls, err)
					}
				} else if !errors.Is(err, ErrConflict) || effects.calls != 0 {
					t.Fatalf("unproven parent dispatched: effects=%d err=%v", effects.calls, err)
				}
			})
		}
	}
}

func TestPR82RawFeatureCannotBorrowUndispatchedHandoffProof(t *testing.T) {
	f := newPR82HandoffFixture(t)
	d := f.delegation()
	if err := f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, d, f.startedItemID); err != nil {
		t.Fatal(err)
	}
	_, err := f.runner.relations.Ensure(t.Context(), runrelation.Draft{ParentRunID: f.parent.ExecutionRefID, ChildRunID: d.ChildRunID, Kind: runrelation.KindTeamMember, OwnerNodeID: "remote-member"})
	if err != nil {
		t.Fatal(err)
	}
	f.runner.relationReader = f.runner.relations.(RunRelationSource)
	if err = f.runner.verifyMissingRelatedCancellation(t.Context(), d.ChildRunID); !errors.Is(err, ErrRuntimeCancellationUnverified) {
		t.Fatalf("missing raw child borrowed Handoff proof: %v", err)
	}
	if _, _, err = f.runner.cancelRuntimeRun(t.Context(), d.ChildRunID, "stop", nil); !errors.Is(err, ErrRuntimeCancellationUnverified) {
		t.Fatalf("direct cancel borrowed Handoff proof: %v", err)
	}
}

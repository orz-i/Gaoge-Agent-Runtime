package handoff_test

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
)

// An arbitrary Host Run middleware may return from its callback while other
// effects remain unfinished. A settled Root snapshot alone must NOT lend its
// cancellation authority to such a composition.
func TestDeferredControlRejectsOpaqueHostCallbackAndForeignRoutedTwin(t *testing.T) {
	rt, original, model := deferredFixture(t, &deferredHost{})
	c, err := handoff.NewDeferredAgent(original)
	if err != nil {
		t.Fatal(err)
	}
	parent := verifiedHandoffParent()
	local := handoff.Delegation{ID: "local", MemberID: "local", ChildRunID: "deferred-local", Goal: "finish", Model: "test", Status: handoff.StatusQueued}
	_, err = c.StartOrLoad(t.Context(), parent, local)
	if !errors.Is(err, handoff.ErrChildPending) || model.calls != 0 {
		t.Fatalf("original Prepare: %v, calls=%d", err, model.calls)
	}
	child, err := rt.Load(t.Context(), local.ChildRunID)
	if err != nil {
		t.Fatal(err)
	}
	if !c.SupportsDeferredAgent(rt) || !c.IsDeferredLocalDelegation(rt, local) || c.VerifiedLocalDelegationControl(t.Context(), rt, local, child) {
		t.Fatalf("arbitrary Host callback claimed physical cancellation")
	}
	routed, err := handoff.NewDeferredAgentWithRouting(original, func(d handoff.Delegation) (handoff.DelegationRoute, error) {
		if d.MemberID == "remote" {
			return handoff.DelegationRouteRemote, nil
		}
		return handoff.DelegationRouteLocal, nil
	}, handoff.ChildRunnerResolverFunc(func(context.Context, handoff.Delegation) (handoff.ChildRunner, error) {
		return original, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	foreign := local
	foreign.MemberID = "remote"
	if routed.IsDeferredLocalDelegation(rt, foreign) || routed.VerifiedLocalDelegationControl(t.Context(), rt, foreign, child) {
		t.Fatal("remote same-Kernel twin borrowed local execution authority")
	}
}

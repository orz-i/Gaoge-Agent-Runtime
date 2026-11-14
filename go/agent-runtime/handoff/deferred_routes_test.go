package handoff_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
)

func TestDeferredRoutesSeparateLocalProofFromRemoteResolution(t *testing.T) {
	rt, original, provider := deferredFixture(t, &deferredHost{})
	remote := newFakeChildren()
	resolves := 0
	classify := func(d handoff.Delegation) (handoff.DelegationRoute, error) {
		if d.MemberID == "remote" {
			return handoff.DelegationRouteRemote, nil
		}
		return handoff.DelegationRouteLocal, nil
	}
	c, err := handoff.NewDeferredAgentWithRouting(original, classify, handoff.ChildRunnerResolverFunc(func(context.Context, handoff.Delegation) (handoff.ChildRunner, error) { resolves++; return remote, nil }))
	if err != nil {
		t.Fatal(err)
	}
	local := handoff.Delegation{ID: "local", MemberID: "local", ChildRunID: "local-child", Goal: "finish", Model: "test", Status: handoff.StatusQueued}
	other := local
	other.ID = "remote"
	other.MemberID = "remote"
	other.ChildRunID = "remote-child"
	if c.ObservesLocalKernelRunner(rt) || c.UsesLocalAgentRunner(rt, original) || c.OwnsLocalKernelRunner(rt) {
		t.Fatal("global routed proof")
	}
	if !c.ObservesLocalDelegation(rt, local) || !c.UsesLocalAgentForDelegation(rt, original, local) || c.OwnsLocalDelegation(rt, local) {
		t.Fatal("local authority separation")
	}
	if c.ObservesLocalDelegation(rt, other) || c.UsesLocalAgentForDelegation(rt, original, other) || c.OwnsLocalDelegation(rt, other) || resolves != 0 {
		t.Fatal("remote proof resolved or accepted")
	}
	parent := verifiedHandoffParent()
	if _, err = c.StartOrLoad(t.Context(), parent, local); !errors.Is(err, handoff.ErrChildPending) || provider.calls != 0 || resolves != 0 {
		t.Fatalf("local Prepare: %v", err)
	}
	if _, err = c.StartOrLoad(t.Context(), parent, other); !errors.Is(err, handoff.ErrChildPending) || remote.starts != 1 || resolves != 1 {
		t.Fatalf("remote Start: %v", err)
	}
	if _, err = c.Refresh(t.Context(), parent, other); !errors.Is(err, handoff.ErrChildPending) || remote.starts != 1 || resolves != 2 {
		t.Fatalf("remote Refresh: %v", err)
	}
	// Even a concrete same-Kernel Agent returned by the remote resolver cannot
	// lend local authority to this remote delegation.
	twin, err := handoff.NewDeferredAgentWithRouting(original, classify, handoff.ChildRunnerResolverFunc(func(context.Context, handoff.Delegation) (handoff.ChildRunner, error) {
		t.Fatal("proof resolved remote")
		return original, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if twin.ObservesLocalDelegation(rt, other) || twin.UsesLocalAgentForDelegation(rt, original, other) || twin.OwnsLocalDelegation(rt, other) {
		t.Fatal("remote twin accepted")
	}
	second, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: &deferredModel{}})
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, _ := deferredFixture(t, nil)
	if c.UsesLocalAgentForDelegation(rt, second, local) || c.ObservesLocalDelegation(foreign, local) {
		t.Fatal("replacement accepted")
	}
}
func TestDeferredRoutesClassifierIsolationAndFailure(t *testing.T) {
	rt, original, _ := deferredFixture(t, nil)
	d := handoff.Delegation{ID: "d", MemberID: "local", MemberRevision: "v1", ChildRunID: "c", Goal: "g", Status: handoff.StatusQueued, ModelOptions: json.RawMessage(`{"x":1}`), ToolKeys: []string{"tool"}}
	before := d
	before.ModelOptions = append(json.RawMessage(nil), d.ModelOptions...)
	before.ToolKeys = append([]string(nil), d.ToolKeys...)
	c, err := handoff.NewDeferredAgentWithRouting(original, func(v handoff.Delegation) (handoff.DelegationRoute, error) {
		v.ModelOptions[0] = '!'
		v.ToolKeys[0] = "changed"
		return handoff.DelegationRouteLocal, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !c.ObservesLocalDelegation(rt, d) || !reflect.DeepEqual(before, d) {
		t.Fatal("classifier mutated source")
	}
	for _, tc := range []struct {
		name string
		kind handoff.DelegationRoute
		err  error
	}{{"error", handoff.DelegationRouteLocal, errors.New("reject")}, {"invalid", 0, nil}, {"remote-unavailable", handoff.DelegationRouteRemote, nil}} {
		t.Run(tc.name, func(t *testing.T) {
			c, e := handoff.NewDeferredAgentWithRouting(original, func(handoff.Delegation) (handoff.DelegationRoute, error) { return tc.kind, tc.err }, nil)
			if e != nil {
				t.Fatal(e)
			}
			if c.ObservesLocalDelegation(rt, d) || c.UsesLocalAgentForDelegation(rt, original, d) || c.OwnsLocalDelegation(rt, d) {
				t.Fatal("invalid route gained proof")
			}
			if _, e = c.StartOrLoad(t.Context(), verifiedHandoffParent(), d); !errors.Is(e, handoff.ErrChildUnavailable) {
				t.Fatalf("invalid route started: %v", e)
			}
		})
	}
}

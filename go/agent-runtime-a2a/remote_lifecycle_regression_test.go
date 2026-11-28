package a2a

import (
	"context"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
)

func TestPR151A2ARevisionSubstitution(t *testing.T) {
	rt := newShadowRuntime(t)
	remote := &shadowRemote{task: completedTask()}
	p, err := NewPlugin(PluginDependencies{Runtime: rt, AttestationKey: testA2AShadowKey(), Bindings: BindingResolverFunc(func(_ context.Context, target, rev string) (Binding, error) {
		return Binding{TargetID: target, Revision: rev, Client: remote, Discovery: Discovery{Descriptor: RemoteAgentDescriptor{Name: "audit-agent", PreferredURL: testShadowRemoteURL, ProtocolVersion: ProtocolVersion, ProtocolBinding: "HTTP+JSON"}}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	c, err := handoff.New(handoff.Config{Remote: p})
	if err != nil {
		t.Fatal(err)
	}
	parent := newShadowParent(t, rt)
	d := handoff.Delegation{Remote: true, ID: "delegate", MemberID: "a2a:remote", MemberRevision: "r1", ChildRunID: "audit-child", Goal: "remote goal", Status: handoff.StatusQueued}
	orig, err := c.StartOrLoad(t.Context(), parent, d)
	if err != nil || orig.Status != handoff.StatusCompleted {
		t.Fatalf("setup %+v err=%v", orig, err)
	}

	fixed, err := p.ResolveChild(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	d.MemberRevision = "r2"
	if _, err := p.ResolveChild(t.Context(), d); err == nil {
		t.Fatal("binding revision substituted")
	}
	wrong, err := handoff.New(handoff.Config{Remote: handoff.ChildRunnerResolverFunc(func(context.Context, handoff.Delegation) (handoff.ChildRunner, error) { return fixed, nil })})
	if err != nil {
		t.Fatal(err)
	}
	for _, coordinator := range []*handoff.Coordinator{c, wrong} {
		if got, err := coordinator.Refresh(t.Context(), parent, d); err == nil || got.ID != "" {
			t.Fatalf("wrong revision refreshed: %+v %v", got, err)
		}
		if got, err := coordinator.StartOrLoad(t.Context(), parent, d); err == nil || got.ID != "" {
			t.Fatalf("wrong revision replayed: %+v %v", got, err)
		}
	}
}

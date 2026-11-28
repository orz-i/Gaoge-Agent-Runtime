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

func TestRoutesSeparateLocalProofFromRemoteResolution(t *testing.T) {
	rt, original, provider := deferredFixture(t, &deferredHost{})
	resolves := 0
	c, err := handoff.New(handoff.Config{Local: original, PrepareLocal: true,
		Remote: handoff.ChildRunnerResolverFunc(func(context.Context, handoff.Delegation) (handoff.ChildRunner, error) {
			resolves++
			return original, nil // A same-Kernel Agent cannot impersonate a remote shadow.
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	local := handoff.Delegation{ID: "local", MemberID: "local", ChildRunID: "local-child", Goal: "finish", Model: "test", Status: handoff.StatusQueued}
	remote := handoff.Delegation{ID: "remote", MemberID: "remote", Remote: true, ChildRunID: "remote-child", Goal: "remote goal", Status: handoff.StatusQueued}
	if !c.ObservesLocalDelegation(rt, local) || c.LocalAgent(rt) != original || original.OwnsKernelRuntime(rt) {
		t.Fatal("local observation/physical authority separation")
	}
	if c.ObservesLocalDelegation(rt, remote) || resolves != 0 {
		t.Fatal("remote proof resolved or accepted")
	}
	parent := verifiedHandoffParent()
	if _, err = c.StartOrLoad(t.Context(), parent, local); !errors.Is(err, handoff.ErrChildPending) || provider.calls != 0 || resolves != 0 {
		t.Fatalf("local Prepare: %v", err)
	}
	if _, err = c.StartOrLoad(t.Context(), parent, remote); !errors.Is(err, handoff.ErrInvalidDelegation) || resolves != 1 || provider.calls != 0 {
		t.Fatalf("remote twin started: %v", err)
	}
	if _, err = c.Refresh(t.Context(), parent, remote); !errors.Is(err, handoff.ErrInvalidDelegation) || resolves != 2 {
		t.Fatalf("remote twin observed: %v", err)
	}
	second, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: &deferredModel{}})
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, _ := deferredFixture(t, nil)
	if c.LocalAgent(rt) == second || c.ObservesLocalDelegation(foreign, local) {
		t.Fatal("replacement accepted")
	}
}

func TestOpaqueResolverIsolationAndUnavailableRoute(t *testing.T) {
	d := handoff.Delegation{ID: "d", MemberID: "opaque", ChildRunID: "c", Goal: "g", Status: handoff.StatusQueued, ModelOptions: json.RawMessage(`{"x":1}`), ToolKeys: []string{"tool"}}
	before := d
	before.ModelOptions = append(json.RawMessage(nil), d.ModelOptions...)
	before.ToolKeys = append([]string(nil), d.ToolKeys...)
	c, err := handoff.New(handoff.Config{Opaque: handoff.ChildRunnerResolverFunc(func(_ context.Context, v handoff.Delegation) (handoff.ChildRunner, error) {
		v.ModelOptions[0], v.ToolKeys[0] = '!', "changed"
		return nil, errors.New("unavailable")
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.StartOrLoad(t.Context(), verifiedHandoffParent(), d); !errors.Is(err, handoff.ErrChildUnavailable) || !reflect.DeepEqual(before, d) {
		t.Fatalf("resolver mutated source or dispatched: %v", err)
	}
}

func TestConfigRejectsAmbiguousOrUnsupportedLocalComposition(t *testing.T) {
	_, local, _ := deferredFixture(t, nil)
	opaque := handoff.ChildRunnerResolverFunc(func(context.Context, handoff.Delegation) (handoff.ChildRunner, error) { return local, nil })
	var absent *agent.Runner
	for _, config := range []handoff.Config{{}, {Local: absent}, {Local: local, Opaque: opaque}, {PrepareLocal: true}, {Local: newFakeChildren(), PrepareLocal: true}} {
		if _, err := handoff.New(config); !errors.Is(err, handoff.ErrInvalidDelegation) {
			t.Fatalf("invalid composition accepted: %+v %v", config, err)
		}
	}
}

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type managedScopePorts struct {
	fail         bool
	prepareCalls int
}

func (p *managedScopePorts) Hydrate(ctx context.Context, i plugin.RunInvocation) (context.Context, BoundedContextHydration, error) {
	return ctx, BoundedContextHydration{RunID: i.RunID, SourceBinding: json.RawMessage(`{"original":"source"}`)}, nil
}
func (p *managedScopePorts) Plan(_ context.Context, r model.Request) (BoundedContextPlan, error) {
	p.prepareCalls++
	if p.fail {
		return BoundedContextPlan{}, errors.New("uncertain preparation")
	}
	return BoundedContextPlan{Request: r}, nil
}
func (*managedScopePorts) PutArtifact(context.Context, runtimecontext.Artifact) (runtimecontext.Artifact, error) {
	panic("unexpected artifact operation")
}
func (*managedScopePorts) CommitCheckpoint(context.Context, BoundedContextPlan, runtimecontext.Checkpoint) (runtimecontext.Checkpoint, error) {
	panic("unexpected checkpoint operation")
}
func (*managedScopePorts) BindCheckpoint(context.Context, string, runtimecontext.Checkpoint) error {
	panic("unexpected binding operation")
}

func TestManagedContextActualEarlyReturnCanSuspendButIsNotCompletion(t *testing.T) {
	ports := &managedScopePorts{}
	adapter, e := NewBoundedContextAdapter(runtimecontext.NewManager(runtimecontext.Dependencies{}), ports)
	if e != nil {
		t.Fatal(e)
	}
	chain, e := plugin.NewRunChain(adapter)
	if e != nil {
		t.Fatal(e)
	}
	lifecycle := callbackscope.New(callbackscope.Binding{RunID: "run", GrantID: "original"}, []any{adapter})
	_, e = chain.Invoke(callbackscope.Bind(t.Context(), lifecycle), plugin.RunInvocation{RunID: "run", Operation: plugin.RunStart}, func(ctx context.Context) (kernel.Snapshot, error) {
		request := model.Request{RunID: "run", Model: "first"}
		if _, ok := callbackscope.ManagedSuspension(ctx, adapter, request); ok {
			t.Fatal("hydration alone certified preparation")
		}
		for _, name := range []string{"first", "second"} {
			request.Model = name
			if _, err := adapter.Prepare(ctx, request); err != nil {
				t.Fatal(err)
			}
			if _, ok := callbackscope.ManagedSuspension(ctx, adapter, request); !ok {
				t.Fatal("actual successful early return not recorded")
			}
		}
		if lifecycle.Complete() {
			t.Fatal("suspended frame claimed whole callback returned")
		}
		return kernel.Snapshot{}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if ports.prepareCalls != 2 || !lifecycle.Complete() {
		t.Fatal("actual return bookkeeping lost")
	}
}
func TestManagedContextFailedPreparationCannotCreateSafePoint(t *testing.T) {
	ports := &managedScopePorts{fail: true}
	adapter, e := NewBoundedContextAdapter(runtimecontext.NewManager(runtimecontext.Dependencies{}), ports)
	if e != nil {
		t.Fatal(e)
	}
	chain, e := plugin.NewRunChain(adapter)
	if e != nil {
		t.Fatal(e)
	}
	scope := callbackscope.New(callbackscope.Binding{RunID: "run"}, []any{adapter})
	_, e = chain.Invoke(callbackscope.Bind(t.Context(), scope), plugin.RunInvocation{RunID: "run", Operation: plugin.RunStart}, func(ctx context.Context) (kernel.Snapshot, error) {
		request := model.Request{RunID: "run"}
		_, err := adapter.Prepare(ctx, request)
		if err == nil {
			t.Fatal("missing fixture failure")
		}
		if _, ok := callbackscope.ManagedSuspension(ctx, adapter, request); ok {
			t.Fatal("uncertain prepare certified")
		}
		return kernel.Snapshot{}, err
	})
	if e == nil {
		t.Fatal("failed Prepare swallowed")
	}
}

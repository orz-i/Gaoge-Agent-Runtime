package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type forgedContextRejection struct{ pressureTestPorts }

func (forgedContextRejection) Plan(context.Context, model.Request) (BoundedContextPlan, error) {
	return BoundedContextPlan{}, &BoundedContextBudgetError{}
}

func TestOwnedBoundedContextReceiptsRequireConcretePreparation(t *testing.T) {
	for _, tc := range []struct {
		name               string
		ports              BoundedContextPorts
		text               string
		complete, rejected bool
	}{
		{"prepared", &managedScopePorts{}, "hello", true, false},
		{"hard rejection", pressureTestPorts{}, strings.Repeat("B", 512), true, true},
		{"forged port rejection", forgedContextRejection{}, "hello", false, true},
		{"unknown port failure", &managedScopePorts{fail: true}, "hello", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := NewBoundedContextAdapter(runtimecontext.NewManager(runtimecontext.Dependencies{}), tc.ports)
			if err != nil {
				t.Fatal(err)
			}
			runner := &Runner{boundedContext: a, rootCallbacks: &rootCallbackState{}}
			scope := callbackscope.New(callbackscope.Binding{RunID: "run", GrantID: "grant"}, []any{a})
			request := model.CloneRequest(model.Request{RunID: "run", Model: "openai/example", Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts(tc.text)}}})
			_, err = runner.invokeContextRun(callbackscope.Bind(t.Context(), scope), plugin.RunInvocation{RunID: "run", Operation: plugin.RunStart}, func(ctx context.Context) (kernel.Snapshot, error) {
				_, err := runner.prepareBoundedContext(ctx, kernel.Snapshot{}, request)
				return kernel.Snapshot{}, err
			})
			if (model.ClassifiedErrorCode(err) == "context.input_budget_exceeded") != (tc.complete && tc.rejected) || errors.Is(err, runtimecontext.ErrBudgetExceeded) != tc.rejected || scope.Complete() != tc.complete || runner.rootCallbacks.unknown.Load() == tc.complete {
				t.Fatalf("incorrect receipt: complete=%v unknown=%v err=%v", scope.Complete(), runner.rootCallbacks.unknown.Load(), err)
			}
		})
	}
}

func TestOwnedBoundedContextCompositionExcludesOtherPreparers(t *testing.T) {
	a, _ := NewBoundedContextAdapter(runtimecontext.NewManager(runtimecontext.Dependencies{}), &managedScopePorts{})
	d := Dependencies{BoundedContext: a}
	if !validBoundedContextComposition(d) || !scopedDeferredRootCallbacks(d) {
		t.Fatal("valid concrete adapter rejected")
	}
	if _, legacy := any(a).(plugin.RunMiddleware); legacy {
		t.Fatal("Context adapter still implements executable middleware")
	}
	d.PrepareModelRequest = a.Prepare
	if validBoundedContextComposition(d) || scopedDeferredRootCallbacks(d) {
		t.Fatal("generic callback was trusted")
	}
	runner := &Runner{boundedContext: a, rootCallbacks: &rootCallbackState{}}
	scope := callbackscope.New(callbackscope.Binding{RunID: "run", GrantID: "grant"}, []any{a})
	_, err := runner.prepareBoundedContext(callbackscope.Bind(t.Context(), scope), kernel.Snapshot{}, model.Request{RunID: "run"})
	if !errors.Is(err, ErrInvalidRequest) || !runner.rootCallbacks.unknown.Load() {
		t.Fatal("missing active hydration accepted")
	}
}

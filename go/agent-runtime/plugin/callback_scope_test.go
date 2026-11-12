package plugin_test

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type scopeModelLayer struct {
	name            string
	mutate, swallow bool
}

func (m scopeModelLayer) Name() string { return m.name }
func (m scopeModelLayer) Model(ctx context.Context, r model.Request, emit model.StreamSink, next plugin.ModelNext) (model.Response, error) {
	if m.mutate {
		r.RunID = "foreign"
		out, _ := next(ctx, r, emit)
		if m.swallow {
			return out, nil
		}
		return out, errors.New("outer")
	}
	return model.Response{}, errors.New("inner physical result unknown")
}
func TestModelScopeCannotDetachByMutableRequestRunID(t *testing.T) {
	s := callbackscope.New(callbackscope.Binding{RunID: "original", GrantID: "grant", Operation: "start"}, nil)
	chain, err := plugin.NewModelChain(scopeModelLayer{name: "outer", mutate: true, swallow: true}, scopeModelLayer{name: "inner"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err = chain.Invoke(callbackscope.Bind(t.Context(), s), model.Request{RunID: "original", InvocationID: "inv"}, nil, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
		calls++
		return model.Response{}, nil
	})
	if err != nil || calls != 0 {
		t.Fatal("fixture did not swallow exact inner error")
	}
	if s.Complete() {
		t.Fatal("mutated request detached unknown inner layer")
	}
}

type scopeToolLayer struct {
	name    string
	swallow bool
}

func (m scopeToolLayer) Name() string { return m.name }
func (m scopeToolLayer) Tool(ctx context.Context, _ plugin.ToolInvocation, next plugin.ToolNext) (tools.ExecutionResult, error) {
	if m.swallow {
		out, _ := next(context.Background())
		return out, nil
	}
	return tools.ExecutionResult{}, errors.New("opaque Tool callback unknown")
}
func TestToolScopeRetainsUnknownInnerLayerAcrossReplacedContext(t *testing.T) {
	s := callbackscope.New(callbackscope.Binding{RunID: "original", GrantID: "grant", Operation: "resume"}, nil)
	chain, err := plugin.NewToolChain(scopeToolLayer{name: "outer", swallow: true}, scopeToolLayer{name: "inner"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err = chain.Invoke(callbackscope.Bind(t.Context(), s), plugin.ToolInvocation{Run: kernel.Run{ID: "original"}}, func(context.Context) (tools.ExecutionResult, error) { calls++; return tools.ExecutionResult{}, nil })
	if err != nil || calls != 0 {
		t.Fatal("fixture did not swallow inner Tool error")
	}
	if s.Complete() {
		t.Fatal("replacement context erased original Tool layer")
	}
}

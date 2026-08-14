package plugin_test

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

func TestRunChainPreservesExplicitOrderAndRejectsDoubleNext(t *testing.T) {
	t.Parallel()
	order := make([]string, 0, 5)
	chain, err := plugin.NewRunChain(
		runMiddleware{name: "outer", order: &order},
		runMiddleware{name: "inner", order: &order, doubleNext: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = chain.Invoke(t.Context(), plugin.RunInvocation{Operation: plugin.RunStart, Kind: "echo"}, func(context.Context) (kernel.Snapshot, error) {
		order = append(order, "terminal")
		return kernel.Snapshot{}, nil
	})
	if !errors.Is(err, plugin.ErrNextAlreadyCalled) {
		t.Fatalf("double next error = %v", err)
	}
	want := []string{"outer:before", "inner:before", "terminal", "inner:after", "outer:after"}
	if len(order) != len(want) {
		t.Fatalf("order = %#v", order)
	}
	for index := range want {
		if order[index] != want[index] {
			t.Fatalf("order = %#v, want %#v", order, want)
		}
	}
}

func TestModelChainClonesRequestAcrossMiddlewareBoundary(t *testing.T) {
	t.Parallel()
	chain, err := plugin.NewModelChain(modelMiddleware{name: "mutator"})
	if err != nil {
		t.Fatal(err)
	}
	request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "original"}}}
	response, err := chain.Invoke(t.Context(), request, nil, func(_ context.Context, next model.Request, _ model.StreamSink) (model.Response, error) {
		if next.Messages[0].Content != "mutated" {
			t.Fatalf("next request = %#v", next)
		}
		return model.Response{Content: "done"}, nil
	})
	if err != nil || response.Content != "done" || request.Messages[0].Content != "original" {
		t.Fatalf("response=%#v request=%#v err=%v", response, request, err)
	}
}

func TestToolChainRejectsDuplicateNames(t *testing.T) {
	t.Parallel()
	_, err := plugin.NewToolChain(toolMiddleware{name: "guard"}, toolMiddleware{name: " guard "})
	if !errors.Is(err, plugin.ErrDuplicateName) {
		t.Fatalf("duplicate error = %v", err)
	}
}

type runMiddleware struct {
	name       string
	order      *[]string
	doubleNext bool
}

func (middleware runMiddleware) Name() string { return middleware.name }

func (middleware runMiddleware) Run(
	ctx context.Context,
	_ plugin.RunInvocation,
	next plugin.RunNext,
) (kernel.Snapshot, error) {
	*middleware.order = append(*middleware.order, middleware.name+":before")
	snapshot, err := next(ctx)
	*middleware.order = append(*middleware.order, middleware.name+":after")
	if err == nil && middleware.doubleNext {
		_, err = next(ctx)
	}
	return snapshot, err
}

type modelMiddleware struct{ name string }

func (middleware modelMiddleware) Name() string { return middleware.name }

func (middleware modelMiddleware) Model(
	ctx context.Context,
	request model.Request,
	emit model.StreamSink,
	next plugin.ModelNext,
) (model.Response, error) {
	request.Messages[0].Content = "mutated"
	return next(ctx, request, emit)
}

type toolMiddleware struct{ name string }

func (middleware toolMiddleware) Name() string { return middleware.name }

func (middleware toolMiddleware) Tool(
	ctx context.Context,
	_ plugin.ToolInvocation,
	next plugin.ToolNext,
) (tools.ExecutionResult, error) {
	return next(ctx)
}

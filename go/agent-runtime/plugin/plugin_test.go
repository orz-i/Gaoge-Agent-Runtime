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

const (
	testPluginFirst  = "first"
	testPluginSecond = "second"
)

var errPolicyFailed = errors.New("policy failed")

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

type approvalPolicy struct {
	name   string
	result plugin.ApprovalRequirement
	err    error
	order  *[]string
}

func (policy approvalPolicy) Name() string { return policy.name }

func (policy approvalPolicy) Approval(context.Context, plugin.ToolInvocation) (plugin.ApprovalRequirement, error) {
	if policy.order != nil {
		*policy.order = append(*policy.order, policy.name)
	}
	return policy.result, policy.err
}

func TestApprovalPolicySetEvaluatesAllPoliciesAndRequiresAny(t *testing.T) {
	t.Parallel()
	order := make([]string, 0, 2)
	set, err := plugin.NewApprovalPolicySet(
		approvalPolicy{name: testPluginFirst, result: plugin.ApprovalRequired, order: &order},
		approvalPolicy{name: testPluginSecond, result: plugin.ApprovalNotRequired, order: &order},
	)
	if err != nil {
		t.Fatal(err)
	}
	required, err := set.RequiresApproval(t.Context(), plugin.ToolInvocation{})
	if err != nil || !required || len(order) != 2 || order[0] != testPluginFirst || order[1] != testPluginSecond {
		t.Fatalf("required=%v order=%#v err=%v", required, order, err)
	}
}

func TestApprovalPolicySetFailsClosedOnPolicyError(t *testing.T) {
	t.Parallel()
	set, err := plugin.NewApprovalPolicySet(
		approvalPolicy{name: "required", result: plugin.ApprovalRequired},
		approvalPolicy{name: "broken", err: errPolicyFailed},
	)
	if err != nil {
		t.Fatal(err)
	}
	required, err := set.RequiresApproval(t.Context(), plugin.ToolInvocation{})
	if required || !errors.Is(err, errPolicyFailed) {
		t.Fatalf("required=%v err=%v", required, err)
	}
}

type mutatingObserver struct {
	name       string
	order      *[]string
	panicAfter bool
}

func (observer mutatingObserver) Name() string { return observer.name }

func (observer mutatingObserver) Observe(_ context.Context, event plugin.Event) {
	*observer.order = append(*observer.order, observer.name)
	if len(event.Data) > 0 {
		event.Data[0] = '['
	}
	if observer.panicAfter {
		panic("observer failure")
	}
}

func TestObserverSetPreservesOrderIsolationAndContainsPanics(t *testing.T) {
	t.Parallel()
	order := make([]string, 0, 2)
	set, err := plugin.NewObserverSet(
		mutatingObserver{name: testPluginFirst, order: &order, panicAfter: true},
		mutatingObserver{name: testPluginSecond, order: &order},
	)
	if err != nil {
		t.Fatal(err)
	}
	event := plugin.Event{RunID: "run-1", Type: "test", Data: []byte(`{"ok":true}`)}
	set.Observe(t.Context(), event)
	if len(order) != 2 || order[0] != testPluginFirst || order[1] != testPluginSecond {
		t.Fatalf("observer order = %#v", order)
	}
	if string(event.Data) != `{"ok":true}` {
		t.Fatalf("observer mutated source event: %s", event.Data)
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

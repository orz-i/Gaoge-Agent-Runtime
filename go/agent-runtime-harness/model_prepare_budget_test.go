package harness

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestModelPreparationFailureReleasesUnsentSearchBudgetForParent(t *testing.T) {
	store, shared, parent := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 2, MaxHostedToolCalls: 1, MaxToolCalls: 1})
	now := time.Now().UTC()
	_, _, err := store.CreateInvocation(t.Context(), Invocation{ID: "rejected-child", TurnID: "hosted-budget-turn", ExecutionClass: ExecutionAgent, ExecutionRefID: "child-run", CapabilityKey: CapabilityAgent, Status: InvocationRunning, Revision: 1, Attempt: 1, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	child := model.CloneRequest(parent)
	child.RunID, child.InvocationID = "child-run", "oversized-child"
	ctx, cancel := context.WithCancel(t.Context())
	_, err = shared.ModelWithPrepare(ctx, child, nil, func(_ context.Context, bounded model.Request) (model.Request, error) {
		if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 1 {
			t.Fatalf("missing frozen allowance: %+v", bounded.HostedToolCallLimit)
		}
		cancel()
		return bounded, runtimecontext.ErrBudgetExceeded
	}, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
		t.Fatal("oversized input reached model dispatch")
		return model.Response{}, nil
	})
	if !errors.Is(err, runtimecontext.ErrBudgetExceeded) {
		t.Fatal(err)
	}
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	reservation := ledger.Reservations["model:"+child.InvocationID]
	if reservation.Status != budget.ReservationReleased || reservation.Dispatched || ledger.View("").UnknownUsage || ledger.View("").Reserved.ToolCalls != 0 || ledger.View("").Usage.LLMCalls != 0 {
		t.Fatalf("unsent child retained its allowance: %+v %+v", reservation, ledger.View(""))
	}
	parent.InvocationID = "parent-takeover"
	_, err = shared.ModelWithPrepare(t.Context(), parent, nil, func(_ context.Context, bounded model.Request) (model.Request, error) { return bounded, nil }, func(_ context.Context, bounded model.Request, _ model.StreamSink) (model.Response, error) {
		if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 1 {
			t.Fatal("parent did not recover its search allowance")
		}
		return model.Response{Content: "parent completed", HostedToolCalls: []model.HostedToolCall{{ID: "takeover-search", ToolKey: "native.search", Status: "completed"}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ledger, err = store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	if view := ledger.View(""); view.Usage.LLMCalls != 1 || view.Usage.ToolCalls != 1 || view.Usage.HostedToolCalls != 1 || view.UnknownUsage {
		t.Fatalf("incorrect takeover accounting: %+v", view)
	}
}

func TestPreparedModelUnknownDispatchStillCannotRetry(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxHostedToolCalls: 1, MaxToolCalls: 1})
	prepare := func(_ context.Context, bounded model.Request) (model.Request, error) { return bounded, nil }
	_, err := shared.ModelWithPrepare(t.Context(), request, nil, prepare, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
		return model.Response{}, context.DeadlineExceeded
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	_, err = shared.ModelWithPrepare(t.Context(), request, nil, func(context.Context, model.Request) (model.Request, error) {
		t.Fatal("uncertain generation was prepared again")
		return model.Request{}, nil
	}, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
		t.Fatal("uncertain generation was dispatched again")
		return model.Response{}, nil
	})
	if !errors.Is(err, budget.ErrWaiting) {
		t.Fatal(err)
	}
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	if view := ledger.View(""); !view.UnknownUsage || view.Reserved.HostedToolCalls != 1 {
		t.Fatalf("unknown dispatch lost its reservation: %+v", view)
	}
}

package harness

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type hostedBudgetClock struct{}

func (hostedBudgetClock) Now() time.Time { return time.Now() }

func hostedBudgetFixture(t *testing.T, limits budget.Limits) (*MemoryStore, *BudgetMiddleware, model.Request) {
	t.Helper()
	store := NewMemoryStore()
	now := time.Now().UTC()
	config, err := SealConfigSnapshot("hosted-budget-turn", ConfigSnapshot{SharedBudget: &limits}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutConfigSnapshot(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CreateTurn(t.Context(), Turn{ID: config.TurnID, SessionID: "session", HostTurn: HostRef{Kind: "test", ID: "turn"}, ConfigSnapshotID: config.ID, Status: TurnRunning, Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CreateInvocation(t.Context(), Invocation{ID: "hosted-invocation", TurnID: config.TurnID, ExecutionClass: ExecutionAgent, ExecutionRefID: "hosted-run", CapabilityKey: CapabilityAgent, Status: InvocationRunning, Revision: 1, Attempt: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	shared, err := NewBudgetMiddleware(BudgetMiddlewareDependencies{Store: store, Clock: hostedBudgetClock{}})
	if err != nil {
		t.Fatal(err)
	}
	return store, shared, model.Request{RunID: "hosted-run", InvocationID: "search-1", Messages: []model.Message{{Role: model.RoleUser, Content: "research"}}, HostedTools: []model.HostedTool{{Key: "native.search"}}, Tools: []tools.Definition{{Key: "local.read", Name: "read"}}}
}

func TestHostedBudgetExhaustionStillAllowsFinalModelResponse(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxHostedToolCalls: 3, MaxToolCalls: 3, MaxLLMCalls: 8})
	for index, want := range []int{3, 0, 0} {
		request.InvocationID = fmt.Sprintf("search-%d", index)
		_, err := shared.Model(t.Context(), request, nil, func(_ context.Context, bounded model.Request, _ model.StreamSink) (model.Response, error) {
			if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != want {
				t.Fatalf("ceiling=%v want=%d", bounded.HostedToolCallLimit, want)
			}
			response := model.Response{Content: "final evidence"}
			for call := range want {
				response.HostedToolCalls = append(response.HostedToolCalls, model.HostedToolCall{ID: fmt.Sprintf("%d-%d", index, call), ToolKey: "native.search"})
			}
			if want == 0 && (len(bounded.HostedTools) != 1 || len(bounded.Tools) != 0 || bounded.RequireToolCall) {
				t.Fatalf("exhausted tools remain: %+v", bounded)
			}
			return response, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	view := ledger.View("")
	if view.Usage.HostedToolCalls != 3 || view.Usage.ToolCalls != 3 || view.Usage.LLMCalls != 3 || view.Reserved.HostedToolCalls != 0 {
		t.Fatalf("usage=%+v", view)
	}
}

func TestHostedReservationLeavesNoLocalToolsInLastSharedSlot(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxHostedToolCalls: 1, MaxToolCalls: 1, MaxLLMCalls: 3})
	request.RequireToolCall = true
	for index, want := range []int{1, 0} {
		request.InvocationID = fmt.Sprintf("last-slot-%d", index)
		_, err := shared.Model(t.Context(), request, nil, func(_ context.Context, bounded model.Request, _ model.StreamSink) (model.Response, error) {
			if len(bounded.Tools) != 0 || bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != want {
				t.Fatalf("native search and local tool can compete for last slot: %+v", bounded)
			}
			response := model.Response{Content: "completed from existing evidence"}
			if want > 0 {
				response.HostedToolCalls = []model.HostedToolCall{{ID: "last-search", ToolKey: "native.search"}}
			}
			return response, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	view := ledger.View("")
	if view.Usage.ToolCalls != 1 || view.Usage.HostedToolCalls != 1 || view.Usage.LLMCalls != 2 || view.Reserved.ToolCalls != 0 {
		t.Fatalf("final model response could not complete within quota: %+v", view)
	}
	if len(request.Messages) != 1 {
		t.Fatal("frozen original request was mutated")
	}
}

func TestHostedBudgetUnknownReceiptHoldsQuotaAcrossRestart(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxHostedToolCalls: 2, MaxToolCalls: 4})
	_, err := shared.Model(t.Context(), request, nil, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
		return model.Response{}, context.DeadlineExceeded
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	restarted, err := NewBudgetMiddleware(BudgetMiddlewareDependencies{Store: store, Clock: hostedBudgetClock{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = restarted.Model(t.Context(), request, nil, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
		t.Fatal("uncertain search redispatched")
		return model.Response{}, nil
	})
	if !errors.Is(err, budget.ErrWaiting) {
		t.Fatalf("restart=%v", err)
	}
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	if view := ledger.View(""); !view.UnknownUsage || view.Reserved.HostedToolCalls != 2 || view.Reserved.ToolCalls != 2 {
		t.Fatalf("lost reservation=%+v", view)
	}
}

func TestHostedBudgetConcurrentRequestsCannotOverReserve(t *testing.T) {
	store, shared, original := hostedBudgetFixture(t, budget.Limits{MaxHostedToolCalls: 3, MaxToolCalls: 3})
	var group sync.WaitGroup
	for index := range 3 {
		group.Go(func() {
			request := model.CloneRequest(original)
			request.InvocationID = fmt.Sprintf("parallel-%d", index)
			_, err := shared.Model(t.Context(), request, nil, func(_ context.Context, bounded model.Request, _ model.StreamSink) (model.Response, error) {
				response := model.Response{Content: "evidence"}
				for call := range *bounded.HostedToolCallLimit {
					response.HostedToolCalls = append(response.HostedToolCalls, model.HostedToolCall{ID: fmt.Sprintf("%d-%d", index, call), ToolKey: "native.search"})
				}
				return response, nil
			})
			if err != nil && !errors.Is(err, budget.ErrWaiting) && !errors.Is(err, budget.ErrExhausted) {
				t.Errorf("admission=%v", err)
			}
		})
	}
	group.Wait()
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	view := ledger.View("")
	if view.Usage.HostedToolCalls+view.Reserved.HostedToolCalls > 3 || view.Usage.ToolCalls+view.Reserved.ToolCalls > 3 {
		t.Fatalf("overspent=%+v", view)
	}
}

func TestHostedBudgetIsOptIn(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{})
	_, err := shared.Model(t.Context(), request, nil, func(_ context.Context, bounded model.Request, _ model.StreamSink) (model.Response, error) {
		if bounded.HostedToolCallLimit != nil || len(bounded.HostedTools) != 1 {
			t.Fatalf("implicit system ceiling: %+v", bounded)
		}
		return model.Response{Content: "unchanged", HostedToolCalls: []model.HostedToolCall{{ID: "unlimited-search", ToolKey: "native.search"}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	if usage := ledger.View("").Usage; usage.ToolCalls != 1 || usage.HostedToolCalls != 1 {
		t.Fatalf("unlimited native execution must still be accounted: %+v", usage)
	}
}

// The shared Tool ceiling is sufficient by itself. A hosted-only ceiling is
// optional, not a prerequisite for native execution admission.
func TestHostedSharedToolLimitBoundsNativeCallsWithoutHostedLimit(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxToolCalls: 1, MaxLLMCalls: 3})
	request.RequireToolCall = true
	for step := range 2 {
		request.InvocationID = fmt.Sprintf("total-only-%d", step)
		_, err := shared.Model(t.Context(), request, nil, func(_ context.Context, bounded model.Request, _ model.StreamSink) (model.Response, error) {
			want := 1 - step
			if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != want {
				t.Fatalf("step %d failed to bound native dispatch under shared tool limit: %v", step, bounded.HostedToolCallLimit)
			}
			if len(bounded.Tools) != 0 || bounded.RequireToolCall {
				t.Fatalf("step %d exposes local calls competing for native budget: %+v", step, bounded)
			}
			answer := model.Response{Content: "done"}
			if step == 0 {
				answer.HostedToolCalls = []model.HostedToolCall{{ID: "one", ToolKey: "native.search", Status: "completed"}}
			}
			return answer, nil
		})
		if err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
	}
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	if usage := ledger.View("").Usage; usage.HostedToolCalls != 1 || usage.ToolCalls != 1 {
		t.Fatalf("shared tool budget was overspent: %+v", usage)
	}
}

// A completed provider response that violates the host's limit is accounted
// truthfully but MUST be rejected, never reported as an admitted success.
func TestHostedSharedToolLimitRejectsOverrunAfterAuthoritativeReceipt(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxToolCalls: 1})
	_, err := shared.Model(t.Context(), request, nil, func(_ context.Context, bounded model.Request, _ model.StreamSink) (model.Response, error) {
		if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 1 {
			t.Fatalf("missing dispatch limit: %v", bounded.HostedToolCallLimit)
		}
		return model.Response{Content: "unexpected extra hosted execution", HostedToolCalls: []model.HostedToolCall{
			{ID: "a", ToolKey: "native.search", Status: "completed"},
			{ID: "b", ToolKey: "native.search", Status: "completed"},
		}}, nil
	})
	if !errors.Is(err, budget.ErrInvalidUsage) {
		t.Fatalf("unreported provider overrun: %v", err)
	}
	ledger, e := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if e != nil {
		t.Fatal(e)
	}
	if got := ledger.View("").Usage.ToolCalls; got != 2 {
		t.Fatalf("authoritative charges were erased: %d", got)
	}
}

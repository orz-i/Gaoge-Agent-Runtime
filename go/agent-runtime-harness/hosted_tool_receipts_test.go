package harness

import (
	"context"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestHostedReceiptWithoutIdentityPreservesTerminalCounts(t *testing.T) {
	for status, want := range map[string]int{"in_progress": 0, "requested": 0, "completed": 2, "failed": 2, "": 2} {
		t.Run(status, func(t *testing.T) {
			calls := []model.HostedToolCall{{ToolKey: "native.search", Status: status}, {ToolKey: "native.search", Status: status}}
			count, receipts, err := hostedReceiptTransfers(budget.Ledger{}, "root", calls)
			if err != nil || count != want || len(receipts) != 0 {
				t.Fatalf("anonymous calls must not be deduplicated or given invented identities: count=%d receipts=%+v err=%v", count, receipts, err)
			}
		})
	}
}

func TestHostedPauseReceiptsHoldAndReconcileAcrossRestart(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxHostedToolCalls: 1, MaxToolCalls: 2, MaxLLMCalls: 6})
	call := model.HostedToolCall{ID: "native-pending", ToolKey: "native.search", Status: "in_progress"}
	for step := range 4 {
		request.InvocationID = string(rune('a' + step))
		if step == 1 {
			var err error
			shared, err = NewBudgetMiddleware(BudgetMiddlewareDependencies{Store: store, Clock: hostedBudgetClock{}})
			if err != nil {
				t.Fatal(err)
			}
		}
		_, err := shared.Model(t.Context(), request, nil, func(_ context.Context, bounded model.Request, _ model.StreamSink) (model.Response, error) {
			want := 1
			if step >= 3 {
				want = 0
			}
			if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != want {
				t.Fatalf("step %d allowance=%v", step, bounded.HostedToolCallLimit)
			}
			if step == 1 && len(bounded.Tools) != 1 {
				t.Fatal("a pending native receipt consumed the same slot twice, hiding the authorized local tool")
			}
			if step >= 2 {
				call.Status = "completed"
			}
			return model.Response{Content: "evidence", Continue: step < 2, Usage: &model.Usage{InputTokens: 17, OutputTokens: 3}, HostedToolCalls: []model.HostedToolCall{call}}, nil
		})
		if err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
		if err != nil {
			t.Fatal(err)
		}
		view := ledger.View("")
		usage := toolUsageFromLedger(ledger)
		if step < 2 {
			if view.Usage.HostedToolCalls != 0 || view.Reserved.HostedToolCalls != 1 || !view.UnknownUsage || !usage.Incomplete {
				t.Fatalf("pending receipt was lost: %+v %+v", view, usage)
			}
		} else if view.Usage.HostedToolCalls != 1 || view.Reserved.HostedToolCalls != 0 || view.UnknownUsage || usage.Incomplete || usage.HostedCallsByKey["native.search"] != 1 {
			t.Fatalf("completion counted twice or not reconciled: %+v %+v", view, usage)
		}
		if view.Usage.InputTokens != int64((step+1)*17) {
			t.Fatalf("known model tokens were not settled: %+v", view)
		}
		// Reusing a logical invocation returns its durable receipt without dispatch.
		_, err = shared.Model(t.Context(), request, nil, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
			t.Fatal("receipt redispatched")
			return model.Response{}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestHostedUnfinishedReceiptSurvivesTerminalRun(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxHostedToolCalls: 1, MaxToolCalls: 1})
	_, err := shared.Model(t.Context(), request, nil, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
		return model.Response{Content: "partial answer", HostedToolCalls: []model.HostedToolCall{{ID: "unresolved", ToolKey: "native.search", Status: "in_progress"}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	request.InvocationID = "final-only"
	zero := 0
	request.HostedToolCallLimit = &zero
	_, err = shared.Model(t.Context(), request, nil, func(_ context.Context, bounded model.Request, _ model.StreamSink) (model.Response, error) {
		if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 0 {
			t.Fatal("final-only sampling resumed uncertain native work")
		}
		return model.Response{Content: "final answer with an explicitly unknown native outcome"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator := budget.Coordinator{Store: store}
	ledger, err := coordinator.FinishRuns(t.Context(), "hosted-budget-turn", request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err = coordinator.Cancel(t.Context(), ledger.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view := ledger.View(""); view.Usage.HostedToolCalls != 0 || view.Reserved.HostedToolCalls != 1 || !view.UnknownUsage || !toolUsageFromLedger(ledger).Incomplete {
		t.Fatalf("terminal/cancel fabricated a completion: %+v", view)
	}
}

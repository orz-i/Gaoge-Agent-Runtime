package harness

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestHostedReceiptWithoutIdentityRejectsTerminalCounts(t *testing.T) {
	for _, status := range []string{"completed", "failed", "error", "cancelled", "canceled"} {
		t.Run(status, func(t *testing.T) {
			calls := []model.HostedToolCall{
				{ToolKey: "native.search", Status: status},
				{ToolKey: "native.search", Status: status},
			}
			count, receipts, err := hostedReceiptTransfers(budget.Ledger{}, "root", calls)
			if !errors.Is(err, budget.ErrInvalidUsage) || count != 0 || len(receipts) != 0 {
				t.Fatalf("anonymous terminal facts certified a duplicate charge: count=%d receipts=%+v err=%v",
					count, receipts, err)
			}
		})
	}
	for _, status := range []string{"", "in_progress", "requested", "provider_unknown"} {
		calls := []model.HostedToolCall{{ToolKey: "native.search", Status: status}}
		count, receipts, err := hostedReceiptTransfers(budget.Ledger{}, "root", calls)
		if !errors.Is(err, budget.ErrInvalidUsage) || count != 0 || len(receipts) != 0 {
			t.Fatalf("anonymous nonterminal %q fabricated a receipt: count=%d receipts=%+v err=%v", status, count, receipts, err)
		}
	}
}

func TestHostedEmptyStatusWithIdentityKeepsQuotaUntilExplicitTerminal(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{
		MaxHostedToolCalls: 1, MaxToolCalls: 1, MaxLLMCalls: 3,
	})
	_, err := shared.Model(t.Context(), request, nil, func(_ context.Context, bounded model.Request, _ model.StreamSink) (model.Response, error) {
		if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 1 {
			t.Fatalf("initial native allowance was not fenced: %+v", bounded)
		}
		return model.Response{Content: "pending native result", HostedToolCalls: []model.HostedToolCall{{
			ID: "pending-native", ToolKey: "native.search", Status: "",
		}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	view := ledger.View("")
	if view.Usage.HostedToolCalls != 0 || view.Reserved.HostedToolCalls != 1 ||
		!view.UnknownUsage || !toolUsageFromLedger(ledger).Incomplete {
		t.Fatalf("empty status was incorrectly paid or released: %+v", view)
	}
	restarted, err := NewBudgetMiddleware(BudgetMiddlewareDependencies{Store: store, Clock: hostedBudgetClock{}})
	if err != nil {
		t.Fatal(err)
	}
	request.InvocationID = "reconcile-native"
	_, err = restarted.Model(t.Context(), request, nil, func(_ context.Context, bounded model.Request, _ model.StreamSink) (model.Response, error) {
		if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 0 {
			t.Fatalf("restart turned unknown native effect into new authorization: %+v", bounded)
		}
		return model.Response{Content: "confirmed", HostedToolCalls: []model.HostedToolCall{{
			ID: "pending-native", ToolKey: "native.search", Status: "completed",
		}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ledger, err = store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	view = ledger.View("")
	if view.Usage.HostedToolCalls != 1 || view.Reserved.HostedToolCalls != 0 ||
		view.UnknownUsage || toolUsageFromLedger(ledger).Incomplete {
		t.Fatalf("exact terminal receipt failed to reconcile pending native effect: %+v", view)
	}
}

func TestHostedAnonymousUnfinishedStatusKeepsDispatchedModelReservation(t *testing.T) {
	for _, status := range []string{"", "in_progress", "unknown-provider-state"} {
		t.Run(status, func(t *testing.T) {
			store, shared, request := hostedBudgetFixture(t, budget.Limits{
				MaxHostedToolCalls: 1, MaxToolCalls: 1, MaxLLMCalls: 2,
			})
			calls := 0
			_, err := shared.Model(t.Context(), request, nil, func(_ context.Context, bounded model.Request, _ model.StreamSink) (model.Response, error) {
				calls++
				if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 1 {
					t.Fatalf("initial native allowance missing: %+v", bounded)
				}
				return model.Response{Content: "incomplete", HostedToolCalls: []model.HostedToolCall{{
					ToolKey: "native.search", Status: status, // Missing provider-owned Call ID.
				}}}, nil
			})
			if !errors.Is(err, budget.ErrInvalidUsage) || calls != 1 {
				t.Fatalf("anonymous unverified native effect was not fenced: err=%v calls=%d", err, calls)
			}
			ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
			if err != nil {
				t.Fatal(err)
			}
			view := ledger.View("")
			if view.Usage.HostedToolCalls != 0 || view.Reserved.HostedToolCalls != 1 ||
				!view.UnknownUsage || !toolUsageFromLedger(ledger).Incomplete {
				t.Fatalf("unidentified effect silently refunded native quota: %+v", view)
			}
			restarted, err := NewBudgetMiddleware(BudgetMiddlewareDependencies{Store: store, Clock: hostedBudgetClock{}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = restarted.Model(t.Context(), request, nil, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
				calls++
				return model.Response{Content: "unsafe replay"}, nil
			})
			if !errors.Is(err, budget.ErrWaiting) || calls != 1 {
				t.Fatalf("unidentified native effect was automatically retried: err=%v calls=%d", err, calls)
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
			// The in-progress receipt holds the only slot until settled.
			// Continuation of that old execution is not a new allowance.
			want := 0
			if step == 0 {
				want = 1
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

// Held receipts authorize completion of the existing effect only. They are
// not another allowance for new provider executions on the next model step.
func TestHostedContinuationHeldReceiptCannotGrantNewNativeCall(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxHostedToolCalls: 1, MaxToolCalls: 1, MaxLLMCalls: 3})
	_, err := shared.Model(t.Context(), request, nil, func(_ context.Context, bounded model.Request, _ model.StreamSink) (model.Response, error) {
		if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 1 {
			t.Fatalf("initial new-call allowance: %v", bounded.HostedToolCallLimit)
		}
		return model.Response{Continue: true, HostedToolCalls: []model.HostedToolCall{{ID: "old", ToolKey: "native.search", Status: "in_progress"}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	request.InvocationID = "finish-old"
	_, err = shared.Model(t.Context(), request, nil, func(_ context.Context, bounded model.Request, _ model.StreamSink) (model.Response, error) {
		if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 0 {
			t.Fatalf("held receipt became a fresh call permit: %v", bounded.HostedToolCallLimit)
		}
		if len(bounded.HostedTools) != 1 || len(bounded.Tools) != 0 {
			t.Fatalf("native replay was removed or a local call was admitted: %+v", bounded)
		}
		return model.Response{Content: "old complete", HostedToolCalls: []model.HostedToolCall{{ID: "old", ToolKey: "native.search", Status: "completed"}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ledger, e := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if e != nil {
		t.Fatal(e)
	}
	view := ledger.View("")
	if view.Usage.HostedToolCalls != 1 || view.Reserved.HostedToolCalls != 0 || view.UnknownUsage {
		t.Fatalf("old receipt was not settled exactly once: %+v", view)
	}
}

// Existing held work and newly available quota are independent. Only unused
// quota may be delegated for NEW native executions.
func TestHostedContinuationReservesOnlyFreshCalls(t *testing.T) {
	_, shared, request := hostedBudgetFixture(t, budget.Limits{MaxHostedToolCalls: 3, MaxToolCalls: 3})
	_, err := shared.Model(t.Context(), request, nil, func(_ context.Context, bounded model.Request, _ model.StreamSink) (model.Response, error) {
		return model.Response{Continue: true, HostedToolCalls: []model.HostedToolCall{
			{ID: "old-1", ToolKey: "native.search", Status: "in_progress"},
			{ID: "old-2", ToolKey: "native.search", Status: "in_progress"},
		}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	request.InvocationID = "fresh-only"
	_, err = shared.Model(t.Context(), request, nil, func(_ context.Context, bounded model.Request, _ model.StreamSink) (model.Response, error) {
		if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 1 {
			t.Fatalf("held calls inflated the new-execution limit: %v", bounded.HostedToolCallLimit)
		}
		return model.Response{Content: "done", HostedToolCalls: []model.HostedToolCall{
			{ID: "old-1", ToolKey: "native.search", Status: "completed"},
			{ID: "old-2", ToolKey: "native.search", Status: "completed"},
			{ID: "new-3", ToolKey: "native.search", Status: "completed"},
		}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Provider final frames can contain both an interim and final observation for
// one named Tool. Their relative order must not strand a paid native receipt.
func TestHostedSameResponseTerminalWinsAndReplaysExactlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name, first, last string
	}{
		{"pending_then_completed", "in_progress", "completed"},
		{"completed_then_pending", "completed", "in_progress"},
		{"running_then_failed", "running", "failed"},
		{"queued_then_cancelled", "queued", "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxHostedToolCalls: 1, MaxToolCalls: 1, MaxLLMCalls: 3})
			_, err := shared.Model(t.Context(), request, nil, func(_ context.Context, bounded model.Request, _ model.StreamSink) (model.Response, error) {
				if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 1 {
					t.Fatalf("missing bounded initial dispatch: %v", bounded.HostedToolCallLimit)
				}
				return model.Response{Content: "final result", HostedToolCalls: []model.HostedToolCall{
					{ID: "call", ToolKey: "native.search", Status: tc.first},
					{ID: "call", ToolKey: "native.search", Status: tc.last},
				}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
			if err != nil {
				t.Fatal(err)
			}
			view := ledger.View("")
			if view.Usage.HostedToolCalls != 1 || view.Reserved.HostedToolCalls != 0 || view.UnknownUsage {
				t.Fatalf("terminal lost / duplicate billed: %+v", view)
			}
			usage := toolUsageFromLedger(ledger)
			if usage.Incomplete || usage.HostedCalls != 1 || usage.HostedCallsByKey["native.search"] != 1 {
				t.Fatalf("host projection disagrees with settled receipt: %+v", usage)
			}
			_, err = shared.Model(t.Context(), request, nil, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
				t.Fatal("replayed model invocation dispatched a second time")
				return model.Response{}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			request.InvocationID = "after-terminal"
			_, err = shared.Model(t.Context(), request, nil, func(_ context.Context, bounded model.Request, _ model.StreamSink) (model.Response, error) {
				if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 0 {
					t.Fatalf("already-charged tool has gained new authorization: %v", bounded.HostedToolCallLimit)
				}
				return model.Response{Content: "done"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// In-progress synonyms and unfamiliar nonempty statuses do not prove a
// completion. All consumers must classify them identically.
func TestHostedNonterminalStatusesPreserveUncertainCharges(t *testing.T) {
	for _, status := range []string{"requested", "pending", "in_progress", "running", "queued", "unknown_vendor_state"} {
		t.Run(status, func(t *testing.T) {
			store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxHostedToolCalls: 1, MaxToolCalls: 1})
			_, err := shared.Model(t.Context(), request, nil, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
				return model.Response{Content: "still pending", HostedToolCalls: []model.HostedToolCall{
					{ID: "unresolved", ToolKey: "native.search", Status: status},
				}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
			if err != nil {
				t.Fatal(err)
			}
			view := ledger.View("")
			usage := toolUsageFromLedger(ledger)
			if view.Usage.HostedToolCalls != 0 || view.Reserved.HostedToolCalls != 1 ||
				!view.UnknownUsage || !usage.Incomplete || usage.HostedCalls != 0 ||
				usage.HostedCallsByKey["native.search"] != 0 || hostedToolItemStatus(status) != ItemStarted {
				t.Fatalf("inconsistent pending status %q: ledger=%+v presentation=%+v timeline=%s",
					status, view, usage, hostedToolItemStatus(status))
			}
		})
	}
}

package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// A terminal status does not make an anonymous provider Tool execution
// identifiable. The same anonymous terminal may be repeated inside one
// response or across separate Model receipts; billing it as two successful
// executions invents a remote identity.
func TestAnonymousHostedTerminalDoesNotSettleModelOrReleaseNativeQuota(t *testing.T) {
	for _, tc := range []struct {
		name  string
		calls []model.HostedToolCall
	}{
		{name: "duplicate-completed", calls: []model.HostedToolCall{
			{ToolKey: "native.search", Status: "completed"},
			{ToolKey: "native.search", Status: "completed"},
		}},
		{name: "failed", calls: []model.HostedToolCall{{ToolKey: "native.search", Status: "failed"}}},
		{name: "mixed-anonymous-and-identified", calls: []model.HostedToolCall{
			{ID: "known-call", ToolKey: "native.search", Status: "completed"},
			{ToolKey: "native.search", Status: "completed"},
		}},
		{name: "whitespace-id", calls: []model.HostedToolCall{
			{ID: "  ", ToolKey: "native.search", Status: "completed"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, shared, request := hostedBudgetFixture(t, budget.Limits{
				MaxHostedToolCalls: 2, MaxToolCalls: 2, MaxLLMCalls: 3,
			})
			physical := 0
			_, err := shared.Model(t.Context(), request, nil, func(_ context.Context, bounded model.Request, _ model.StreamSink) (model.Response, error) {
				physical++
				if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 2 {
					t.Fatalf("unexpected original native allowance: %+v", bounded)
				}
				return model.Response{Content: "provider output retained without identity", Usage: &model.Usage{
					InputTokens: 12, OutputTokens: 2,
				}, HostedToolCalls: tc.calls}, nil
			})
			if !errors.Is(err, budget.ErrInvalidUsage) || physical != 1 {
				t.Fatalf("unverifiable terminal model execution was settled: err=%v physical=%d", err, physical)
			}
			ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
			if err != nil {
				t.Fatal(err)
			}
			view := ledger.View("")
			held, ok := ledger.Reservations["model:"+request.InvocationID]
			if !ok || held.Status != budget.ReservationHeld || !held.Dispatched ||
				view.Usage.HostedToolCalls != 0 || view.Usage.ToolCalls != 0 ||
				view.Usage.LLMCalls != 0 || view.Reserved.HostedToolCalls != 2 ||
				!view.UnknownUsage || !toolUsageFromLedger(ledger).Incomplete {
				t.Fatalf("anonymous terminal changed an already-dispatched model into a charge or released quota: held=%+v view=%+v", held, view)
			}
			for id := range ledger.Reservations {
				if strings.HasPrefix(id, hostedReceiptPrefix) {
					t.Fatalf("mixed anonymous/identified response produced a partial hosted receipt: %s", id)
				}
			}
			restarted, err := NewBudgetMiddleware(BudgetMiddlewareDependencies{Store: store, Clock: hostedBudgetClock{}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = restarted.Model(t.Context(), request, nil, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
				physical++
				return model.Response{Content: "invalid duplicate physical send"}, nil
			})
			if !errors.Is(err, budget.ErrWaiting) || physical != 1 {
				t.Fatalf("same frozen Model request was sent again after unverifiable terminal: err=%v physical=%d", err, physical)
			}
			ledger, err = store.LoadBudget(t.Context(), "hosted-budget-turn")
			if err != nil || ledger.View("").Reserved.HostedToolCalls != 2 || ledger.View("").Usage.HostedToolCalls != 0 {
				t.Fatalf("restart changed uncertain quota: %+v err=%v", ledger.View(""), err)
			}
			// The uncertain original native effects do not prohibit an
			// independent final-only text response. But they do consume
			// all available NEW native execution allowance.
			finalOnly := model.CloneRequest(request)
			finalOnly.InvocationID = "final-only-after-unknown"
			_, err = restarted.Model(t.Context(), finalOnly, nil,
				func(_ context.Context, bounded model.Request, _ model.StreamSink) (model.Response, error) {
					physical++
					if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 0 ||
						len(bounded.Tools) != 0 {
						t.Fatalf("unverified native effect created a fresh Tool grant: %+v", bounded)
					}
					return model.Response{Content: "final answer without new native effects"}, nil
				})
			if err != nil || physical != 2 {
				t.Fatalf("safe final-only Model call blocked or unexpectedly redelivered: err=%v calls=%d", err, physical)
			}
			ledger, err = store.LoadBudget(t.Context(), "hosted-budget-turn")
			if err != nil || ledger.View("").Usage.LLMCalls != 1 ||
				ledger.View("").Reserved.HostedToolCalls != 2 || ledger.View("").Usage.HostedToolCalls != 0 ||
				!ledger.View("").UnknownUsage {
				t.Fatalf("final-only Model mutated unverified native charges: view=%+v err=%v", ledger.View(""), err)
			}
		})
	}
}

// Older pre-hard-cut beta receipts may already claim an anonymous terminal
// count in a settled Model reservation. Preserve the authoritative stored
// numeric total but never invent a positional per-key identity for it.
func TestHistoricalAnonymousHostedReceiptUsageIsIncompleteWithoutSyntheticPerKeyCounts(t *testing.T) {
	raw, err := json.Marshal(model.Response{HostedToolCalls: []model.HostedToolCall{
		{ID: "named", ToolKey: "native.search", Status: "completed"},
		{ToolKey: "native.search", Status: "completed"},
		{ToolKey: "native.search", Status: "completed"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ledger := budget.Ledger{Reservations: map[string]budget.Reservation{
		"model:legacy": {ID: "model:legacy", RunID: "root", Status: budget.ReservationSettled,
			Usage: budget.Usage{LLMCalls: 1, ToolCalls: 3, HostedToolCalls: 3}, Receipt: json.RawMessage(raw)},
	}}
	usage := toolUsageFromLedger(ledger)
	if usage.HostedCalls != 3 || usage.HostedCallsByKey["native.search"] != 1 || !usage.Incomplete {
		t.Fatalf("legacy anonymous receipts created provable per-key charges: %+v", usage)
	}
}

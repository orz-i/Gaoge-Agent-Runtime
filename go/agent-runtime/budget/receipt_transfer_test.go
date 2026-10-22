package budget_test

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

func TestReceiptTransferIsAtomicAndCannotReopenCompletedWork(t *testing.T) {
	c := newLedger(t, budget.Limits{MaxToolCalls: 1, MaxHostedToolCalls: 1})
	model := reservation("model", 0, 0)
	model.Requested.ToolCalls, model.Requested.HostedToolCalls = 1, 1
	if _, err := c.Reserve(t.Context(), "turn", model, true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Dispatch(t.Context(), "turn", model.ID); err != nil {
		t.Fatal(err)
	}
	pending := budget.Reservation{ID: "native", RunID: "root", RequestHash: "native", Requested: budget.Usage{ToolCalls: 1, HostedToolCalls: 1}, Status: budget.ReservationHeld, Dispatched: true, Receipt: []byte(`{"status":"in_progress"}`)}
	wrongRun := pending
	wrongRun.ID, wrongRun.RunID = "wrong", "other-run"
	if _, err := c.SettleWithReceipts(t.Context(), "turn", model.ID, budget.Usage{LLMCalls: 1}, []byte(`{"model":"done"}`), []budget.Reservation{pending, wrongRun}); !errors.Is(err, budget.ErrInvalidUsage) {
		t.Fatalf("cross-run transfer=%v", err)
	}
	ledger, err := c.Store.LoadBudget(t.Context(), "turn")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := ledger.Reservations[pending.ID]; exists || ledger.Reservations[model.ID].Status != budget.ReservationHeld {
		t.Fatal("failed transfer partially committed")
	}
	ledger, err = c.SettleWithReceipts(t.Context(), "turn", model.ID, budget.Usage{LLMCalls: 1}, []byte(`{"model":"done"}`), []budget.Reservation{pending})
	if err != nil {
		t.Fatal(err)
	}
	if view := ledger.View(""); view.Usage.LLMCalls != 1 || view.Reserved.HostedToolCalls != 1 || !view.UnknownUsage {
		t.Fatalf("transfer lost pending quota: %+v", view)
	}
	next := reservation("next", 0, 0)
	if _, err = c.Reserve(t.Context(), "turn", next, true); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Dispatch(t.Context(), "turn", next.ID); err != nil {
		t.Fatal(err)
	}
	pending.Status, pending.Receipt = budget.ReservationSettled, []byte(`{"status":"completed"}`)
	ledger, err = c.SettleWithReceipts(t.Context(), "turn", next.ID, budget.Usage{LLMCalls: 1, ToolCalls: 1, HostedToolCalls: 1}, []byte(`{"native":"done"}`), []budget.Reservation{pending})
	if err != nil {
		t.Fatal(err)
	}
	// Replay the earlier model receipt, which originally contained the pending
	// identity. It must not resurrect it after a later completion.
	pending.Status = budget.ReservationHeld
	ledger, err = c.SettleWithReceipts(t.Context(), "turn", model.ID, budget.Usage{LLMCalls: 1}, []byte(`{"model":"done"}`), []budget.Reservation{pending})
	if err != nil {
		t.Fatal(err)
	}
	if view := ledger.View(""); view.Usage.HostedToolCalls != 1 || view.Reserved.HostedToolCalls != 0 || view.UnknownUsage {
		t.Fatalf("replay reopened a completed native effect: %+v", view)
	}
}

package harness

import (
	"encoding/json"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

const hostedReceiptPrefix = "hosted:"

func pendingHostedAllowance(ledger budget.Ledger, runID string) int {
	count := 0
	for id, receipt := range ledger.Reservations {
		if strings.HasPrefix(id, hostedReceiptPrefix) && receipt.RunID == runID && receipt.Status == budget.ReservationHeld {
			count += receipt.Requested.HostedToolCalls
		}
	}
	return count
}

// Model tokens are final even when native work pauses. Keep an independent
// held receipt for each unresolved identity, and reconcile it only from an
// explicit terminal provider fact. Completed calls remain on model receipts.
func hostedReceiptTransfers(ledger budget.Ledger, runID string, calls []model.HostedToolCall) (int, []budget.Reservation, error) {
	completed := 0
	related := make([]budget.Reservation, 0, len(calls))
	for _, call := range model.CollapseHostedToolCalls(calls) {
		pending := !model.HostedToolTerminalStatus(call.Status)
		if call.ID == "" {
			if pending {
				// No Provider-owned call ID or terminal fact means there is
				// nothing safe to reconcile. Preserve the original model's
				// dispatched reservation instead of minting a fake Tool ID.
				return 0, nil, budget.ErrInvalidUsage
			}
			completed++
			continue
		}
		identity, err := json.Marshal([]string{runID, call.ToolKey, call.ID})
		if err != nil {
			return 0, nil, err
		}
		id := hostedReceiptPrefix + hashInvocationBytes(identity)
		previous, exists := ledger.Reservations[id]
		if exists && previous.Status == budget.ReservationSettled {
			continue
		}
		// Full provider content already belongs to the immutable model receipt;
		// do not copy long native outputs into the ledger a second time.
		raw, err := json.Marshal(model.HostedToolCall{ID: call.ID, ToolKey: call.ToolKey, Status: call.Status})
		if err != nil {
			return 0, nil, err
		}
		status := budget.ReservationSettled
		if pending {
			status = budget.ReservationHeld
		} else {
			completed++
		}
		related = append(related, budget.Reservation{ID: id, RunID: runID, RequestHash: hashInvocationBytes(identity),
			Requested: budget.Usage{ToolCalls: 1, HostedToolCalls: 1}, Status: status, Dispatched: true, Receipt: raw})
	}
	return completed, related, nil
}

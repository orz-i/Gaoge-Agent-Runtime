package budget

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

const hostedReceiptPrefix = "hosted:"

// An identified native call can hold its own receipt while the Model's
// token charge settles. A missing Provider Call ID cannot prove either an
// independent terminal charge or a reconcilable unfinished effect: reject the
// entire transfer and retain the dispatched Model reservation as unknown.
func hostedReceiptTransfers(ledger Ledger, runID string, calls []model.HostedToolCall) (int, []Reservation, error) {
	completed := 0
	related := make([]Reservation, 0, len(calls))
	for _, call := range model.CollapseHostedToolCalls(calls) {
		if strings.TrimSpace(call.ID) == "" {
			// A terminal status proves only the reported lifecycle, NOT
			// a distinct provider-owned execution. Without an actual Call
			// ID, duplicate terminal frames or replayed Model responses
			// cannot be deduplicated. Keep the dispatched Model budget
			// reservation held for explicit Host reconciliation.
			//
			// An anonymous unfinished result has the same limitation.
			// Never invent an index-, response-, or ToolKey-based remote ID.
			return 0, nil, ErrInvalidUsage
		}
		pending := !model.HostedToolTerminalStatus(call.Status)
		identity, err := json.Marshal([]string{runID, call.ToolKey, call.ID})
		if err != nil {
			return 0, nil, err
		}
		digest := sha256.Sum256(identity)
		id := hostedReceiptPrefix + hex.EncodeToString(digest[:])
		previous, exists := ledger.Reservations[id]
		if exists && previous.Status == ReservationSettled {
			continue
		}
		// Full provider content already belongs to the immutable model receipt;
		// do not copy long native outputs into the ledger a second time.
		raw, err := json.Marshal(model.HostedToolCall{ID: call.ID, ToolKey: call.ToolKey, Status: call.Status})
		if err != nil {
			return 0, nil, err
		}
		status := ReservationSettled
		if pending {
			status = ReservationHeld
		} else {
			completed++
		}
		related = append(related, Reservation{ID: id, RunID: runID, RequestHash: hex.EncodeToString(digest[:]),
			Requested: Usage{ToolCalls: 1, HostedToolCalls: 1}, Status: status, Dispatched: true, Receipt: raw})
	}
	return completed, related, nil
}

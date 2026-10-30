package harness

import (
	"encoding/json"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// ToolUsage is a read-only host projection of settled Turn ledger receipts.
// It includes descendants, excludes reservations and is independent of model prose.
// Hosted keys remain opaque: the host decides which tools are searches.
type ToolUsage struct {
	LocalCalls       int
	HostedCalls      int
	HostedCallsByKey map[string]int
	Incomplete       bool
}

func toolUsageFromLedger(ledger budget.Ledger) *ToolUsage {
	result := &ToolUsage{HostedCallsByKey: map[string]int{}}
	completed, pending := map[string]bool{}, map[string]bool{}
	for _, reservation := range ledger.Reservations {
		if reservation.Status == budget.ReservationHeld || reservation.Status == budget.ReservationWaiting {
			result.Incomplete = true
		}
		if reservation.Status != budget.ReservationSettled {
			continue
		}
		if reservation.Usage.LLMCalls == 0 {
			result.LocalCalls += reservation.Usage.ToolCalls
			continue
		}
		result.HostedCalls += reservation.Usage.HostedToolCalls
		var response model.Response
		if json.Unmarshal(reservation.Receipt, &response) != nil {
			result.Incomplete = true
			continue
		}
		for _, call := range model.CollapseHostedToolCalls(response.HostedToolCalls) {
			if strings.TrimSpace(call.ID) == "" {
				// Historical Model receipts may have been charged before
				// the provider-owned Call ID hard cut. Their stored usage
				// total remains accounting history, but no synthetic
				// response-position identity can prove a per-key call.
				result.Incomplete = true
				continue
			}
			identity, _ := json.Marshal([]string{reservation.RunID, call.ToolKey, call.ID})
			id := string(identity)
			if !model.HostedToolTerminalStatus(call.Status) {
				pending[id] = true
				continue
			}
			if completed[id] {
				continue
			}
			completed[id] = true
			if call.ToolKey == "" {
				result.Incomplete = true
				continue
			}
			result.HostedCallsByKey[call.ToolKey]++
		}
	}
	identified := 0
	for _, count := range result.HostedCallsByKey {
		identified += count
	}
	result.Incomplete = result.Incomplete || identified != result.HostedCalls
	for id := range pending {
		result.Incomplete = result.Incomplete || !completed[id]
	}
	return result
}

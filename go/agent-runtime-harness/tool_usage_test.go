package harness

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestToolUsageUsesSettledReceiptsAcrossDescendants(t *testing.T) {
	ledger := budget.Ledger{Limits: budget.Limits{MaxHostedToolCalls: 4}, Reservations: map[string]budget.Reservation{}}
	for _, runID := range []string{"root", "child", "grandchild"} {
		raw, err := json.Marshal(model.Response{Content: "used four searches", HostedToolCalls: []model.HostedToolCall{
			{ID: "search", ToolKey: "native.web_search", Status: "completed"},
			{ID: "search", ToolKey: "native.web_search", Status: "completed"},
			{ID: "pending", ToolKey: "native.web_search", Status: "in_progress"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		ledger.Reservations[runID] = budget.Reservation{RunID: runID, Status: budget.ReservationSettled, Usage: budget.Usage{LLMCalls: 1, HostedToolCalls: 1, ToolCalls: 1}, Receipt: raw}
	}
	ledger.Reservations["local"] = budget.Reservation{Status: budget.ReservationSettled, Usage: budget.Usage{ToolCalls: 5}}
	ledger.Reservations["unsent"] = budget.Reservation{Status: budget.ReservationReleased, Requested: budget.Usage{HostedToolCalls: 1, ToolCalls: 1}}
	usage := toolUsageFromLedger(ledger)
	if usage.LocalCalls != 5 || usage.HostedCalls != 3 || usage.HostedCallsByKey["native.web_search"] != 3 || !usage.Incomplete {
		t.Fatalf("limits, child prose, duplicate events or unsent work affected actual counts: %+v", usage)
	}
	cloned := cloneSnapshot(Snapshot{ToolUsage: usage})
	cloned.ToolUsage.HostedCallsByKey["native.web_search"] = 99
	if usage.HostedCallsByKey["native.web_search"] != 3 {
		t.Fatal("snapshot usage map aliases original")
	}
	raw, err := json.Marshal(Snapshot{ToolUsage: usage})
	if err != nil || strings.Contains(string(raw), "HostedCalls") || strings.Contains(string(raw), "toolUsage") {
		t.Fatalf("host-only projection changed the HTTP contract: %s %v", raw, err)
	}
}

func TestToolUsageSeparatesNativeKindsAndIncompleteAccounting(t *testing.T) {
	ledger := budget.Ledger{Reservations: map[string]budget.Reservation{
		"image": {Status: budget.ReservationSettled, Usage: budget.Usage{LLMCalls: 1, HostedToolCalls: 1}, Receipt: json.RawMessage(`{"hostedToolCalls":[{"id":"image","toolKey":"native.image_generation","status":"completed"}]}`)},
	}}
	usage := toolUsageFromLedger(ledger)
	if usage.Incomplete || usage.HostedCallsByKey["native.web_search"] != 0 || usage.HostedCallsByKey["native.image_generation"] != 1 {
		t.Fatalf("non-search native call misclassified: %+v", usage)
	}
	for _, reservation := range []budget.Reservation{
		{Status: budget.ReservationHeld, Dispatched: true, Requested: budget.Usage{HostedToolCalls: 2}},
		{Status: budget.ReservationWaiting, Requested: budget.Usage{HostedToolCalls: 2}},
		{Status: budget.ReservationSettled, Usage: budget.Usage{LLMCalls: 1, HostedToolCalls: 1}, Receipt: json.RawMessage(`invalid`)},
		{Status: budget.ReservationSettled, Usage: budget.Usage{LLMCalls: 1, HostedToolCalls: 1}, Receipt: json.RawMessage(`{}`)},
	} {
		ledger.Reservations["unresolved"] = reservation
		if usage = toolUsageFromLedger(ledger); !usage.Incomplete || usage.HostedCallsByKey["native.web_search"] != 0 {
			t.Fatalf("unresolved work fabricated a search count: %+v", usage)
		}
	}
}

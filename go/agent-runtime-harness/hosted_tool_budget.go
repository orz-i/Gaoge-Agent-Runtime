package harness

import (
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// Native execution happens inside the model request. Reserve its worst-case
// cost atomically with the model invocation, before any upstream dispatch.
// This is opt-in per Turn/subtree; it does not change deployment defaults.
func reserveHostedModelTools(request model.Request, reservation budget.Reservation, ledger budget.Ledger) (model.Request, budget.Reservation) {
	maximum, limited := remainingHostedCalls(ledger, request.RunID)
	if !limited || len(request.HostedTools) == 0 {
		return request, reservation
	}
	if request.HostedToolCallLimit != nil {
		maximum = min(maximum, max(0, *request.HostedToolCallLimit))
	}
	reservation.Requested.HostedToolCalls = maximum
	reservation.Requested.ToolCalls = maximum
	return applyHostedReservation(request, maximum, ledger, request.RunID), reservation
}

func remainingHostedCalls(ledger budget.Ledger, runID string) (int, bool) {
	maximum, limited := int(^uint(0)>>1), false
	for current := runID; ; current = ledger.Runs[current].ParentRunID {
		view := ledger.View(current)
		if view.Limits.MaxHostedToolCalls > 0 {
			limited = true
			maximum = min(maximum, max(0, view.Limits.MaxHostedToolCalls-view.Usage.HostedToolCalls-view.Reserved.HostedToolCalls))
		}
		if view.Limits.MaxToolCalls > 0 {
			// The shared Tool ceiling also covers provider-hosted executions.
			// A deployment need not configure a separate hosted-only limit.
			limited = true
			maximum = min(maximum, max(0, view.Limits.MaxToolCalls-view.Usage.ToolCalls-view.Reserved.ToolCalls))
		}
		if current == "" {
			return maximum, limited
		}
	}
}

func applyHostedReservation(request model.Request, maximum int, ledger budget.Ledger, runID string) model.Request {
	_, limited := remainingHostedCalls(ledger, runID)
	if !limited || len(request.HostedTools) == 0 {
		return request
	}
	if request.HostedToolCallLimit != nil && *request.HostedToolCallLimit == 0 {
		return request // An explicit final-only repair must not resume native work.
	}
	// This limit governs NEW native executions, not completions of already
	// dispatched calls. Held receipts retain the old calls' budget and can
	// be reconciled by provider replay without authorizing a new call.
	// Adding the held count here would let a provider spend it again.
	request.HostedToolCallLimit = &maximum
	return request
}

// Run after the atomic reservation: its native calls occupy shared tool slots
// even though they have not executed yet. Local calls cannot spend those slots.
func applyLocalToolBudget(request model.Request, ledger budget.Ledger, runID string) model.Request {
	for current := runID; ; current = ledger.Runs[current].ParentRunID {
		view := ledger.View(current)
		if view.Limits.MaxToolCalls > 0 && view.Usage.ToolCalls+view.Reserved.ToolCalls >= view.Limits.MaxToolCalls {
			request.Tools = nil
			request.RequireToolCall = false
			break
		}
		if current == "" {
			break
		}
	}
	return request
}

package agent

import (
	"encoding/json"
	"slices"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// TerminalPhysicalEffectsSettled is a pure durable-state check that a terminal
// Agent has no still-unreconciled Model/Tool/child execution intent. A committed
// Kernel terminal CAS does not stop an already dispatched Host callback.
// Therefore terminal RunStatus alone cannot close a physical cancellation
// intent or certify that external cleanup completed.
//
// This is necessary, not sufficient, evidence for Host physical-effect
// reconciliation: an external provider or Tool without a trusted outcome
// receipt remains outside the authority of Kernel's RunStatus.
func TerminalPhysicalEffectsSettled(snapshot kernel.Snapshot) bool {
	if snapshot.Run.Kind != RunKind ||
		(snapshot.Run.Status != kernel.RunStatusCompleted && snapshot.Result != nil) ||
		(snapshot.Run.Status == kernel.RunStatusCompleted &&
			(snapshot.Result == nil || !json.Valid(snapshot.Result.Content))) {
		return false
	}
	switch snapshot.Run.Status {
	case kernel.RunStatusCompleted, kernel.RunStatusFailed, kernel.RunStatusCancelled:
	case kernel.RunStatusRunning, kernel.RunStatusWaitingInput, kernel.RunStatusPausedBudget:
		return false
	default:
		return false
	}
	state, err := decodeState(snapshot.State)
	if err != nil || len(state.RecoveredTools) != 0 {
		return false
	}
	// Signals remain as idempotency history. Only a unique consumed
	// original Tool result (not the signal itself) can discharge one.
	for i, signal := range state.ExternalSignals {
		if !validToolSignal(signal) || signal.RunID != snapshot.Run.ID || slices.ContainsFunc(state.ExternalSignals[:i], func(s ExternalToolResultSignal) bool {
			return s.CallID == signal.CallID || s.SignalID == signal.SignalID
		}) {
			return false
		}
		results, calls := 0, 0
		for _, inv := range state.ModelInvocations {
			for _, call := range inv.Response.ToolCalls {
				if call.ID == signal.CallID && inv.RunID == signal.RunID && inv.ErrorCode == "" {
					calls++
				}
			}
		}
		for _, m := range state.Messages {
			if m.Role == model.RoleTool && m.ToolCallID == signal.CallID {
				results++
			}
		}
		if calls != 1 || results != 1 {
			return false
		}
	}
	// The immutable signal history is not outstanding physical work.
	state.ExternalSignals = nil
	return knownPhysicalSettlement(state, true)
}

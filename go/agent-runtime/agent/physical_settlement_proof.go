package agent

import (
	"encoding/json"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
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
	if err != nil || state.ToolDispatchID != "" || state.ToolWait != nil ||
		len(state.ExternalSignals) != 0 || len(state.DeferredChildren) != 0 {
		return false
	}
	for _, invocation := range state.ModelInvocations {
		if invocation.Status != ModelInvocationConsumed {
			return false
		}
	}
	return true
}

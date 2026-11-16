package harness_test

import (
	"context"
	"sync/atomic"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

// A Host response handler is still a supported Agent Interaction port,
// independent of the removed Application execution interpreter.
type auditReadOnlyInteractionReconciler struct {
	known atomic.Bool
	reads atomic.Int32
	count atomic.Int32
}

func (h *auditReadOnlyInteractionReconciler) HandleInteractionResponse(_ context.Context, _ harness.InteractionResponseContext) (harness.InteractionResponseResult, error) {
	h.count.Add(1)
	return harness.InteractionResponseResult{OutputRefs: []harness.HostRef{{Kind: "decision", ID: "chosen"}}}, nil
}
func (h *auditReadOnlyInteractionReconciler) ReconcileInteractionResponse(_ context.Context, _ harness.InteractionResponseContext) (harness.InteractionResponseResult, bool, error) {
	h.reads.Add(1)
	if !h.known.Load() {
		return harness.InteractionResponseResult{}, false, nil
	}
	return harness.InteractionResponseResult{OutputRefs: []harness.HostRef{{Kind: "decision", ID: "chosen"}}}, true, nil
}

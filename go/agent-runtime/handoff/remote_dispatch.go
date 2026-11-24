package handoff

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// AdmitRemoteFirstDispatch is the common, original-executor dispatch election
// after Kernel.Create. Create is the durable intent, not authorization to
// perform a later physical send: its Store acknowledgement may be delayed
// while another process commits cancellation.
//
// The protocol's claim signs its own next snapshot and atomically advances
// the original revision. This CAS competes with cancellation in the same
// authoritative Store; it cannot mint a new Host grant or replay an effect.
// A second observation rejects cancellation committed before the claim ACK
// was observed. The claim CAS, not the subsequent wire write, is the
// linearization point for physical *admission*: if it wins first, a later
// cancellation can only be pending/outcome-unknown, not proof that the original
// admitted operation will never reach the peer. A delayed read cannot upgrade
// that unknown cancellation into a definitive no-effect receipt.
//
// An already-committed claim may enter provider I/O before or after a
// concurrent cancellation request. It remains an unresolved one-shot intent
// on crash; recovery must never replay that physical send.
func AdmitRemoteFirstDispatch(
	ctx context.Context,
	rt *kernel.Runtime,
	created kernel.Snapshot,
	verify func(kernel.Snapshot) bool,
	claim func(context.Context, kernel.Snapshot) (kernel.Snapshot, error),
) (kernel.Snapshot, error) {
	if ctx == nil || rt == nil || created.Run.ID == "" || created.Run.Revision == 0 ||
		verify == nil || claim == nil {
		return kernel.Snapshot{}, kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return created, err
	}
	current, err := rt.Load(ctx, created.Run.ID)
	if err != nil {
		return current, err
	}
	if current.Run.Revision != created.Run.Revision ||
		current.Run.Status != kernel.RunStatusRunning || !verify(current) {
		return current, kernel.ErrConflict
	}
	claimed, err := claim(ctx, current)
	if err != nil {
		return claimed, err
	}
	if claimed.Run.Revision != current.Run.Revision+1 ||
		claimed.Run.ID != created.Run.ID || claimed.Run.Status != kernel.RunStatusRunning ||
		!verify(claimed) {
		return claimed, kernel.ErrExecutionConflict
	}
	// A Store may apply a CAS and then delay its success acknowledgement.
	// Never confuse that delayed receipt with a fresh physical-send grant.
	if err := ctx.Err(); err != nil {
		return claimed, err
	}
	latest, err := rt.Load(ctx, created.Run.ID)
	if err != nil {
		return latest, err
	}
	if latest.Run.Revision != claimed.Run.Revision ||
		latest.Run.Status != kernel.RunStatusRunning || !verify(latest) {
		return latest, kernel.ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return latest, err
	}
	return latest, nil
}

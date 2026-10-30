package harness

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// resumeNativeFencedAgentAfterInterruptedStart is NOT a second Harness
// execution owner. The original Start grant is already durable and the Run
// has subsequently acquired a sticky native Kernel execution Epoch. Only
// the native Agent Runner can recover that Run: its Store-backed lease
// Acquire/RequireExecution is the authoritative physical dispatch gate,
// including against concurrent Kernel cancellation. Host-defined runners
// or never-fenced Runs cannot use this path.
func (runner *Runner) resumeNativeFencedAgentAfterInterruptedStart(
	ctx context.Context, invocation Invocation, expectedRevision uint64,
	resumer agentResumer, grantErr error,
) (kernel.Snapshot, error) {
	if !errors.Is(grantErr, ErrConflict) || invocation.ParentItemID != "" ||
		invocation.ExecutionClass != ExecutionAgent || !runner.runtime.SupportsExecution() {
		return kernel.Snapshot{}, grantErr
	}
	if _, native := runner.agent.(*agent.Runner); !native {
		return kernel.Snapshot{}, grantErr
	}
	turn, err := runner.store.GetTurn(ctx, invocation.TurnID)
	if err != nil || terminalTurnStatus(turn.Status) {
		return kernel.Snapshot{}, errors.Join(grantErr, err)
	}
	current, err := runner.runtime.Load(ctx, invocation.ExecutionRefID)
	if err != nil || current.Run.Status != kernel.RunStatusRunning ||
		current.Run.Revision != expectedRevision {
		return kernel.Snapshot{}, grantErr
	}
	// An unbound Runtime must be refused by a sticky native ExecutionStore.
	// A legacy/unleased Run instead returns nil and cannot receive bypass.
	if err = runner.runtime.RequireExecution(ctx, invocation.ExecutionRefID); !errors.Is(err, kernel.ErrExecutionRequired) {
		return kernel.Snapshot{}, grantErr
	}
	items, err := listAllItems(ctx, runner.store, invocation.TurnID)
	if err != nil || ActiveCancellationFence(items) {
		return kernel.Snapshot{}, errors.Join(grantErr, err)
	}
	closed := make(map[string]struct{})
	for _, item := range items {
		if item.Kind == ItemDiagnostic && item.Status == ItemCompleted {
			closed[item.ParentItemID] = struct{}{}
		}
	}
	var interruptedStart Item
	for _, item := range items {
		if item.Kind != ItemDiagnostic || item.Status != ItemStarted ||
			item.ParentItemID != ExecutionDispatchParentItemID || item.InvocationID != invocation.ID {
			continue
		}
		if item.RunID != invocation.ExecutionRefID {
			return kernel.Snapshot{}, ErrConflict
		}
		if _, settled := closed[item.ID]; settled {
			continue
		}
		var phase struct {
			Phase string `json:"phase"`
		}
		if json.Unmarshal(item.Payload, &phase) != nil ||
			(phase.Phase != "start" && phase.Phase != "start-retry") ||
			interruptedStart.ID != "" {
			// An unresolved physical Resume, an unknown format, or multiple
			// Start owners is not proven safe to take over.
			return kernel.Snapshot{}, grantErr
		}
		interruptedStart = item
	}
	if interruptedStart.ID == "" {
		return kernel.Snapshot{}, grantErr
	}
	// A rejected attempt under the previous live lease returns the native
	// execution conflict WITHOUT adding another grant or doing effects.
	// After Store-clock expiry, withRunOwner fences a unique new Epoch.
	resumed, resumeErr := resumer.Resume(ctx, invocation.ExecutionRefID, expectedRevision)
	if resumed.Run.ID != invocation.ExecutionRefID {
		return resumed, resumeErr
	}
	// Once this native segment has returned a real Run snapshot, the
	// interrupted Start's dispatch is no longer an unknown, orphaned
	// authority. Releasing only this same grant preserves other claims.
	return resumed, errors.Join(resumeErr,
		runner.finishPhysicalDispatchGrant(ctx, interruptedStart, "native_kernel_epoch_recovered"))
}

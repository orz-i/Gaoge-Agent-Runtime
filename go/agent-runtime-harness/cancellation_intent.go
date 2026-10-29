package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// cancellationSettlementTimeout bounds cancellation receipt and failure
// timeline writes when the original request has disconnected or expired.
// A single bounded context also covers native no-effect verification.
const cancellationSettlementTimeout = 5 * time.Second

func cancellationSettlementContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cancellationSettlementTimeout)
}

// CancellationFenceParentItemID marks an admitted cancellation in the
// existing append-only Harness Item ledger. All Store implementations MUST
// serialize a new fence with Invocation/Interaction admission under the owning
// Turn lock. No independent cancellation state or SQL schema is introduced.
const CancellationFenceParentItemID = "harness.cancel.fence"

func ActiveCancellationFence(items []Item) bool {
	pending := make(map[string]struct{})
	closed := make(map[string]struct{})
	for _, item := range items {
		if item.Kind != ItemDiagnostic {
			continue
		}
		if item.Status == ItemStarted && item.ParentItemID == CancellationFenceParentItemID {
			pending[item.ID] = struct{}{}
		}
		if item.Status == ItemCompleted {
			closed[item.ParentItemID] = struct{}{}
		}
	}
	for id := range pending {
		if _, settled := closed[id]; !settled {
			return true
		}
	}
	return false
}

func (runner *Runner) beginCancellationFence(ctx context.Context, turn Turn, runID string) (Item, bool, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return Item{}, false, ErrInvalidRequest
	}
	id, err := runner.runtime.NewID("cancel")
	if err != nil {
		return Item{}, false, err
	}
	payload, err := json.Marshal(struct {
		Boundary string `json:"boundary"`
		RunID    string `json:"runID"`
	}{"cancel_scope_pending", runID})
	if err != nil {
		return Item{}, false, err
	}
	now := runner.clock.Now().UTC()
	intent := Item{
		ID:           stableID("hcancel", turn.ID, runID, id),
		TurnID:       turn.ID,
		Kind:         ItemDiagnostic,
		Status:       ItemStarted,
		RunID:        runID,
		ParentItemID: CancellationFenceParentItemID,
		Payload:      payload,
		CreatedAt:    now, UpdatedAt: now,
	}
	saved, created, err := runner.store.AppendItem(ctx, intent)
	if errors.Is(err, ErrConflict) {
		// A previous cancellation can have committed the parent Kernel
		// terminal state and then failed a remote descendant cleanup. The
		// same target may resume cleanup after that authoritative proof,
		// but an uncommitted/running parent remains exclusively fenced.
		resumed, resumeErr := runner.resumeCommittedCancellationFence(ctx, turn, runID)
		return resumed, false, resumeErr
	}
	if err != nil {
		return Item{}, false, err
	}
	if !created {
		return Item{}, false, ErrConflict
	}
	return saved, true, nil
}

func (runner *Runner) resumeCommittedCancellationFence(ctx context.Context, turn Turn, runID string) (Item, error) {
	snapshot, err := runner.runtime.Load(ctx, runID)
	if err != nil || snapshot.Run.Status != kernel.RunStatusCancelled {
		return Item{}, ErrConflict
	}
	items, err := listAllItems(ctx, runner.store, turn.ID)
	if err != nil {
		return Item{}, err
	}
	closed := make(map[string]struct{})
	for _, item := range items {
		if item.Kind == ItemDiagnostic && item.Status == ItemCompleted {
			closed[item.ParentItemID] = struct{}{}
		}
	}
	for _, item := range items {
		if item.Kind != ItemDiagnostic || item.Status != ItemStarted ||
			item.ParentItemID != CancellationFenceParentItemID || item.RunID != runID {
			continue
		}
		if _, done := closed[item.ID]; !done {
			return item, nil
		}
	}
	return Item{}, ErrConflict
}

// finishCancellationFenceAfterRequest bounds durable completion without
// depending on a cancelled caller Context. An expired or lost acknowledgment
// is still an unknown receipt outcome: never infer a safe release from it.
func (runner *Runner) finishCancellationFenceAfterRequest(ctx context.Context, intent Item) error {
	settlementCtx, stopSettlement := cancellationSettlementContext(ctx)
	defer stopSettlement()
	err := runner.finishCancellationFence(settlementCtx, intent)
	return errors.Join(err, settlementCtx.Err())
}

func (runner *Runner) finishCancellationFence(ctx context.Context, intent Item) error {
	now := runner.clock.Now().UTC()
	payload := json.RawMessage(`{"boundary":"cancel_scope_settled"}`)
	_, _, err := runner.store.AppendItem(ctx, Item{
		ID: stableID("hcancelclosed", intent.ID), TurnID: intent.TurnID,
		Kind: ItemDiagnostic, Status: ItemCompleted,
		RunID: intent.RunID, ParentItemID: intent.ID,
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return err
}

// Only a failed read-only preflight can retire its fresh cancellation intent.
// The post-intent claim may contain Host Invocation CAS writes; an attempted
// write makes even a conflict/error with no reply outcome-unknown. Mark that
// boundary BEFORE invoking any write-capable Host Store method.
func (runner *Runner) cancelPreflightWithIntent(
	ctx context.Context, turn Turn, rootRunID string,
	inspect func() error, claim func(writeAttempted *bool) error,
) (Item, error) {
	if err := inspect(); err != nil {
		return Item{}, err
	}
	intent, fresh, err := runner.beginCancellationFence(ctx, turn, rootRunID)
	if err != nil {
		return Item{}, err
	}
	// Admission and the intent commit now compete in the same Store
	// transaction. Re-read all existing owners after sealing so work
	// admitted before this boundary cannot escape the cancellation check.
	writeAttempted := false
	if err = claim(&writeAttempted); err != nil {
		// A read-only failure can retire only this attempt's fresh intent.
		// A Host Store CAS can commit before an error/ACK loss, so never
		// infer no-effect from fresh=true after such a call was attempted.
		if fresh && !writeAttempted {
			err = errors.Join(err, runner.finishCancellationFenceAfterRequest(ctx, intent))
		}
		return Item{}, err
	}
	return intent, nil
}

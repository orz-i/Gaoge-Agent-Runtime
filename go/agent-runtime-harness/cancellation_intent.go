package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

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

// A failed read-only preflight releases the cancellation intent. Once any
// physical cancellation/budget side effect begins the fence remains durable
// until that operation's authoritative outcome has been reconciled.
func (runner *Runner) cancelPreflightWithIntent(
	ctx context.Context, turn Turn, rootRunID string,
	inspect func() error, claim func() error,
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
	if err = claim(); err != nil {
		// Do not release a previous process's partially completed physical
		// cancellation. Only this attempt's fresh, pre-effect intent may
		// be withdrawn on a failed local preflight.
		if fresh {
			err = errors.Join(err, runner.finishCancellationFence(context.WithoutCancel(ctx), intent))
		}
		return Item{}, err
	}
	return intent, nil
}

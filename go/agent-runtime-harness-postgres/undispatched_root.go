package harnesspostgres

import (
	"context"
	"errors"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"gorm.io/gorm"
)

// SettleUncreatedRoot atomically retires a truly undispatched Root
// Invocation and its owning Turn under the same original row lock used
// by Start grant and cancellation admission. There is no transient
// cancelled Invocation / still-running Turn state after a lost ACK.
func (store *Store) SettleUncreatedRoot(ctx context.Context, original harness.Invocation, turn harness.Turn) (
	harness.Invocation, harness.Turn, error,
) {
	if store == nil || store.db == nil || ctx == nil || !harness.VerifiedUncreatedRootWitness(ctx, original, turn) {
		return harness.Invocation{}, harness.Turn{}, harness.ErrConflict
	}
	var finalInvocation harness.Invocation
	var finalTurn harness.Turn
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockAdmissionTurn(tx, turn.ID); err != nil {
			return err
		}
		var rawTurn turnRecord
		if err := tx.Where("id = ?", turn.ID).Take(&rawTurn).Error; err != nil {
			return mapError(err)
		}
		actualTurn := turnFromRecord(rawTurn)
		if actualTurn.ID != turn.ID || actualTurn.Revision != turn.Revision ||
			actualTurn.Status != turn.Status || actualTurn.SessionID != turn.SessionID ||
			actualTurn.HostTurn != turn.HostTurn || actualTurn.ConfigSnapshotID != turn.ConfigSnapshotID {
			return harness.ErrConflict
		}
		var rawInvocation invocationRecord
		if err := tx.Where("id = ?", original.ID).Take(&rawInvocation).Error; err != nil {
			return mapError(err)
		}
		current, err := invocationFromRecord(rawInvocation)
		if err != nil {
			return err
		}
		if !sameInvocation(current, original) || current.ExecutionRefID != original.ExecutionRefID ||
			current.Revision != original.Revision || current.Status != harness.InvocationAccepted ||
			current.TurnID != turn.ID {
			return harness.ErrConflict
		}
		active, err := pendingCancellationFenceTx(tx, turn.ID)
		if err != nil {
			return err
		}
		var childRows []invocationRecord
		if err = tx.Where("turn_id = ? AND id <> ?", turn.ID, original.ID).Find(&childRows).Error; err != nil {
			return err
		}
		children := make([]harness.Invocation, 0, len(childRows))
		for _, row := range childRows {
			child, decodeErr := invocationFromRecord(row)
			if decodeErr != nil {
				return decodeErr
			}
			children = append(children, child)
		}
		// Include global receipt children so a contradictory foreign-Turn
		// receipt can never make a physical dispatch appear absent.
		var rows []itemRecord
		if err = tx.Where("turn_id = ?", turn.ID).Find(&rows).Error; err != nil {
			return err
		}
		items := make([]harness.Item, 0, len(rows))
		seen := map[string]bool{}
		for _, row := range rows {
			item, convertErr := itemFromRecord(row)
			if convertErr != nil {
				return convertErr
			}
			items = append(items, item)
			seen[item.ID] = true
		}
		originalItemCount := len(items)
		for index := 0; index < originalItemCount; index++ {
			originalItem := items[index]
			if originalItem.ParentItemID != harness.ExecutionDispatchParentItemID &&
				originalItem.ParentItemID != harness.CancellationFenceParentItemID &&
				(originalItem.Kind != harness.ItemDelegation || originalItem.Status != harness.ItemStarted) {
				continue
			}
			var children []itemRecord
			query := tx.Where("parent_item_id = ?", originalItem.ID)
			if originalItem.Kind == harness.ItemDelegation {
				query = query.Where("kind = ?", string(harness.ItemDelegation))
			}
			if err = query.Limit(3).Find(&children).Error; err != nil {
				return err
			}
			if len(children) > 2 {
				return harness.ErrConflict
			}
			for _, row := range children {
				item, convertErr := itemFromRecord(row)
				if convertErr != nil {
					return convertErr
				}
				if !seen[item.ID] {
					items = append(items, item)
					seen[item.ID] = true
				}
			}
		}
		var ledger *budget.Ledger
		loaded, readErr := (&Store{db: tx}).LoadBudget(ctx, turn.ID)
		if readErr != nil && !errors.Is(readErr, budget.ErrLedgerNotFound) {
			return readErr
		}
		if readErr == nil {
			ledger = &loaded
		}
		var valid bool
		finalInvocation, finalTurn, valid = harness.ProjectUncreatedRoot(ctx, current, actualTurn, items, active, ledger, children)
		if !valid {
			return harness.ErrConflict
		}
		if finalInvocation.Status == harness.InvocationFailed {
			if err = checkTerminalProjectionTx(ctx, tx, turn.ID, ""); err != nil {
				return err
			}
		}
		now := time.Now().UTC()
		finalInvocation.Revision++
		finalInvocation.UpdatedAt = now
		updateInv := tx.Model(&invocationRecord{}).
			Where("id = ? AND turn_id = ? AND revision = ? AND status = ? AND execution_ref_id = ?",
				current.ID, turn.ID, current.Revision, string(current.Status), current.ExecutionRefID).
			Updates(map[string]any{"status": string(finalInvocation.Status), "revision": finalInvocation.Revision,
				"error_code": finalInvocation.ErrorCode, "error_detail": finalInvocation.ErrorDetail, "updated_at": now})
		if updateInv.Error != nil {
			return updateInv.Error
		}
		if updateInv.RowsAffected != 1 {
			return harness.ErrConflict
		}
		finalTurn.Revision++
		finalTurn.UpdatedAt = now
		updateTurn := tx.Model(&turnRecord{}).Where("id = ? AND revision = ? AND status = ?",
			turn.ID, turn.Revision, string(turn.Status)).
			Updates(map[string]any{"status": string(finalTurn.Status), "revision": finalTurn.Revision,
				"error_code": finalTurn.ErrorCode, "error_detail": finalTurn.ErrorDetail, "updated_at": now})
		if updateTurn.Error != nil {
			return updateTurn.Error
		}
		if updateTurn.RowsAffected != 1 {
			return harness.ErrConflict
		}
		return nil
	})
	if err != nil {
		return harness.Invocation{}, harness.Turn{}, err
	}
	return finalInvocation, finalTurn, nil
}

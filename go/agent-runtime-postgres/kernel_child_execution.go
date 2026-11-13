package postgres

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
)

var _ kernel.ChildExecutionStore = (*KernelStore)(nil)

func kernelPGTerminalStatus(s kernel.RunStatus) bool {
	switch s {
	case kernel.RunStatusCompleted, kernel.RunStatusFailed, kernel.RunStatusCancelled:
		return true
	case kernel.RunStatusRunning, kernel.RunStatusWaitingInput, kernel.RunStatusPausedBudget:
		return false
	default:
		return true
	}
}

// lockLiveChildAncestors acquires parent rows root-to-leaf so ancestor
// termination and child physical admission cannot both commit. The initial
// traversal is untrusted and rechecked after every row is locked.
func (store *KernelStore) lockLiveChildAncestors(tx *gorm.DB, parentID, tenant, actor, threadKind, threadID string) ([]string, error) {
	seen := map[string]struct{}{}
	ids := make([]string, 0, 4)
	for id := parentID; id != ""; {
		if _, duplicate := seen[id]; duplicate || len(ids) >= 64 {
			return nil, kernel.ErrExecutionConflict
		}
		seen[id] = struct{}{}
		var parent models.KernelRunRecord
		if err := tx.Where("run_id = ?", id).First(&parent).Error; err != nil {
			return nil, translateKernelError(err)
		}
		ids = append(ids, id)
		id = parent.ParentRunID
	}
	for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
		ids[i], ids[j] = ids[j], ids[i]
	}
	for i, id := range ids {
		parent, err := lockKernelRun(tx, id)
		if err != nil {
			return nil, err
		}
		if e := pgManagedGate(tx, id); e != nil {
			return nil, e
		}
		if parent.TenantID != tenant || parent.ActorID != actor || parent.ThreadKind != threadKind || parent.ThreadID != threadID ||
			kernelPGTerminalStatus(kernel.RunStatus(parent.Status)) {
			return nil, kernel.ErrExecutionConflict
		}
		if i > 0 && parent.ParentRunID != ids[i-1] {
			return nil, kernel.ErrExecutionConflict
		}
		if i == 0 && parent.ParentRunID != "" {
			return nil, kernel.ErrExecutionConflict
		}
	}
	if len(ids) == 0 {
		return nil, kernel.ErrExecutionConflict
	}
	return ids, nil
}

func (store *KernelStore) BeginChildExecution(ctx context.Context, grant kernel.ChildExecutionGrant) error {
	if store == nil || store.db == nil || ctx == nil || grant.ID == "" || grant.ChildRunID == "" || grant.ExpectedRevision == 0 || grant.IsOwned() {
		return kernel.ErrInvalidInput
	}
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var child models.KernelRunRecord
		if err := tx.Where("run_id = ?", grant.ChildRunID).First(&child).Error; err != nil {
			return translateKernelError(err)
		}
		if child.Revision != grant.ExpectedRevision ||
			(child.Status != string(kernel.RunStatusRunning) && child.Status != string(kernel.RunStatusWaitingInput)) {
			return kernel.ErrExecutionConflict
		}
		var ids []string
		if child.Kind != "workflow" && child.Kind != "agent" {
			return kernel.ErrExecutionRequired
		}
		if child.ParentRunID != "" {
			var ancestorErr error
			ids, ancestorErr = store.lockLiveChildAncestors(tx, child.ParentRunID,
				child.TenantID, child.ActorID, child.ThreadKind, child.ThreadID)
			if ancestorErr != nil {
				return ancestorErr
			}
		}
		// The Store always locks the executing child LAST; persist its OWN
		// fence in addition to every root-to-leaf ancestor so direct Cancel
		// and unbound Apply cannot change it while the external call runs.
		ids = append(ids, child.RunID)
		lockedChild, lockErr := lockKernelRun(tx, grant.ChildRunID)
		if lockErr != nil {
			return lockErr
		}
		if e := pgManagedGate(tx, grant.ChildRunID); e != nil {
			return e
		}
		if lockedChild.Revision != grant.ExpectedRevision ||
			lockedChild.ParentRunID != child.ParentRunID ||
			lockedChild.Kind != child.Kind ||
			lockedChild.TenantID != child.TenantID ||
			lockedChild.ActorID != child.ActorID ||
			lockedChild.ThreadKind != child.ThreadKind ||
			lockedChild.ThreadID != child.ThreadID ||
			(lockedChild.Status != string(kernel.RunStatusRunning) &&
				lockedChild.Status != string(kernel.RunStatusWaitingInput)) {
			return kernel.ErrExecutionConflict
		}
		// Ancestor and child locks serialize contenders; never trust a
		// pre-lock observation or a uniqueness check as the sole guard.
		var active int64
		if err := tx.Model(&models.KernelChildEffectGrantRecord{}).Where("child_run_id = ?", grant.ChildRunID).Count(&active).Error; err != nil {
			return translateKernelError(err)
		}
		if active > 0 {
			return kernel.ErrChildExecutionBusy
		}
		rows := make([]models.KernelChildEffectGrantRecord, 0, len(ids))
		for _, id := range ids {
			rows = append(rows, models.KernelChildEffectGrantRecord{GrantID: grant.ID, ParentRunID: id, ChildRunID: grant.ChildRunID, StartRevision: grant.ExpectedRevision})
		}
		if err := tx.Create(&rows).Error; err != nil {
			return translateKernelApplyError(err)
		}
		return nil
	})
}

// MarkChildExecutionSettled durably records the first-party Agent callback
// completion against the original Grant and the exact child revision. The
// following Finish is a separate recoverable transaction: a crash between
// the two leaves a provably settled marker, whereas a crash before Mark
// leaves the physical effect quarantined.
func (store *KernelStore) MarkChildExecutionSettled(ctx context.Context, grant kernel.ChildExecutionGrant, permit kernel.ChildExecutionSettlement) error {
	if store == nil || store.db == nil || ctx == nil || grant.ID == "" || grant.ChildRunID == "" || grant.ExpectedRevision == 0 {
		return kernel.ErrInvalidInput
	}
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		child, err := lockKernelRun(tx, grant.ChildRunID)
		if err != nil {
			return err
		}
		if e := pgManagedGate(tx, grant.ChildRunID); e != nil {
			return e
		}
		snapshot, snapshotErr := kernelSnapshotFrom(child)
		if snapshotErr != nil {
			return snapshotErr
		}
		if !permit.Matches(grant, snapshot) {
			return kernel.ErrExecutionConflict
		}
		revision := child.Revision
		var records []models.KernelChildEffectGrantRecord
		if err = tx.Where("grant_id = ?", grant.ID).Find(&records).Error; err != nil {
			return translateKernelError(err)
		}
		if len(records) == 0 {
			return kernel.ErrExecutionConflict
		}
		for _, record := range records {
			if pgPhysicalGrant(record) != grant ||
				(record.SettledRevision != 0 && record.SettledRevision != revision) {
				return kernel.ErrExecutionConflict
			}
		}
		if grant.IsOwned() && (len(records) != 1 || records[0].ParentRunID != grant.ChildRunID) {
			return kernel.ErrExecutionConflict
		}
		// Recovery and repeated Mark tolerate expiry, not a replaced native owner.
		if grant.IsOwned() && (child.ExecutionOwner != grant.ExecutionOwnerID || child.ExecutionLeaseID != grant.ExecutionLeaseID || child.ExecutionEpoch != grant.ExecutionEpoch) {
			return kernel.ErrExecutionConflict
		}
		if grant.IsOwned() && permit.IsRecovery() && (records[0].CallbackReturnRevision == 0 || !records[0].CallbackHostCompleted) {
			return kernel.ErrExecutionConflict
		}
		if grant.IsOwned() && records[0].SettledRevision == 0 && !permit.IsRecovery() {
			lease, ok := permit.ExecutionLease()
			now, timeErr := pgStoreNow(tx)
			if timeErr != nil {
				return timeErr
			}
			if !ok || !grant.MatchesExecution(lease) || !pgLeaseMatches(child, lease, now) {
				return kernel.ErrExecutionConflict
			}
		}
		updates := map[string]any{"settled_revision": revision}
		if grant.IsOwned() {
			evidence, evidenceErr := pgOwnedMarkEvidence(records[0], grant, snapshot, permit)
			if evidenceErr != nil {
				return evidenceErr
			}
			updates["settlement_evidence"] = evidence
		}
		result := tx.Model(&models.KernelChildEffectGrantRecord{}).
			Where("grant_id = ? AND child_run_id = ? AND start_revision = ? AND (settled_revision = 0 OR settled_revision = ?)",
				grant.ID, grant.ChildRunID, grant.ExpectedRevision, revision).UpdateColumns(updates)
		if result.Error != nil {
			return translateKernelApplyError(result.Error)
		}
		if result.RowsAffected != int64(len(records)) {
			return kernel.ErrExecutionConflict
		}
		return nil
	})
}

func (store *KernelStore) FinishChildExecution(ctx context.Context, grant kernel.ChildExecutionGrant) error {
	if store == nil || store.db == nil || ctx == nil || grant.ID == "" || grant.ChildRunID == "" || grant.ExpectedRevision == 0 {
		return kernel.ErrInvalidInput
	}
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		child, lockErr := lockKernelRun(tx, grant.ChildRunID)
		if lockErr != nil {
			return lockErr
		}
		if e := pgManagedGate(tx, grant.ChildRunID); e != nil {
			return e
		}
		var records []models.KernelChildEffectGrantRecord
		if err := tx.Where("grant_id = ?", grant.ID).Find(&records).Error; err != nil {
			return translateKernelError(err)
		}
		for _, record := range records {
			if pgPhysicalGrant(record) != grant {
				return kernel.ErrExecutionConflict
			}
		}
		if grant.IsOwned() && (len(records) != 1 || records[0].ParentRunID != grant.ChildRunID) {
			return kernel.ErrExecutionConflict
		}
		var total, count int64
		if countErr := tx.Model(&models.KernelChildEffectGrantRecord{}).
			Where("grant_id = ?", grant.ID).Count(&total).Error; countErr != nil {
			return translateKernelError(countErr)
		}
		if total == 0 || child.Revision == 0 {
			return kernel.ErrExecutionConflict
		}
		if countErr := tx.Model(&models.KernelChildEffectGrantRecord{}).
			Where("grant_id = ? AND child_run_id = ? AND start_revision = ? AND settled_revision = ?", grant.ID, grant.ChildRunID, grant.ExpectedRevision, child.Revision).
			Count(&count).Error; countErr != nil {
			return translateKernelError(countErr)
		}
		if count != total {
			return kernel.ErrExecutionConflict
		}
		if grant.IsOwned() {
			snapshot, e := kernelSnapshotFrom(child)
			if e != nil {
				return e
			}
			if e = pgPersistOwnedRetirement(tx, records[0], grant, snapshot); e != nil {
				return e
			}
		}
		result := tx.Where("grant_id = ? AND child_run_id = ? AND start_revision = ? AND settled_revision = ?",
			grant.ID, grant.ChildRunID, grant.ExpectedRevision, child.Revision).Delete(&models.KernelChildEffectGrantRecord{})
		if result.Error != nil {
			return translateKernelApplyError(result.Error)
		}
		if result.RowsAffected != count {
			return kernel.ErrExecutionConflict
		}
		return nil
	})
}

func (store *KernelStore) LookupChildExecution(ctx context.Context, childID string) (kernel.ChildExecutionGrant, error) {
	if store == nil || store.db == nil || ctx == nil || childID == "" {
		return kernel.ChildExecutionGrant{}, kernel.ErrInvalidInput
	}
	var rows []models.KernelChildEffectGrantRecord
	if err := store.db.WithContext(ctx).Where("child_run_id = ?", childID).Order("grant_id, parent_run_id").Limit(65).Find(&rows).Error; err != nil {
		return kernel.ChildExecutionGrant{}, translateKernelError(err)
	}
	if len(rows) == 0 {
		return kernel.ChildExecutionGrant{}, kernel.ErrNotFound
	}
	original := pgPhysicalGrant(rows[0])
	if original.IsOwned() && (len(rows) != 1 || rows[0].ParentRunID != childID) {
		return kernel.ChildExecutionGrant{}, kernel.ErrExecutionConflict
	}
	for _, row := range rows {
		if pgPhysicalGrant(row) != original || original.ExpectedRevision == 0 || row.SettledRevision != rows[0].SettledRevision {
			return kernel.ChildExecutionGrant{}, kernel.ErrExecutionConflict
		}
	}
	return original, nil
}

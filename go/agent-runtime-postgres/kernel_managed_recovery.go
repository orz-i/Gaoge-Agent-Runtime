package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ kernel.ManagedRecoveryStore = (*KernelStore)(nil)

func pgManagedAuthority(tx *gorm.DB) (kernel.ManagedAuthority, error) {
	var r models.KernelManagedAuthorityRecord
	if e := tx.Where("key = ?", "managed-recovery-authority/v1").First(&r).Error; e != nil {
		return kernel.ManagedAuthority{}, translateKernelError(e)
	}
	a := kernel.ManagedAuthority{Namespace: r.Namespace, Generation: r.Generation, Version: r.Version}
	if !a.Valid() {
		return a, kernel.ErrExecutionConflict
	}
	return a, nil
}
func (s *KernelStore) ManagedRecoveryAuthority(ctx context.Context) (kernel.ManagedAuthority, error) {
	if s == nil || s.db == nil || ctx == nil {
		return kernel.ManagedAuthority{}, kernel.ErrInvalidInput
	}
	return pgManagedAuthority(s.db.WithContext(ctx))
}
func pgLoadManaged(tx *gorm.DB, id string) (kernel.ManagedTransfer, error) {
	var r models.KernelManagedTransferRecord
	if e := tx.Where("run_id = ?", id).First(&r).Error; e != nil {
		return kernel.ManagedTransfer{}, translateKernelError(e)
	}
	var t kernel.ManagedTransfer
	if e := json.Unmarshal([]byte(r.Payload), &t); e != nil {
		return t, kernel.ErrExecutionConflict
	}
	if t.SafePoint.Host.RunID != id || t.SafePoint.ID != r.SuspensionID || t.SafePoint.RecoveryID != r.RecoveryID || t.Revision != r.Revision || !t.SafePoint.Valid() {
		return t, kernel.ErrExecutionConflict
	}
	return t, nil
}
func (s *KernelStore) LoadManagedRecovery(ctx context.Context, id string) (kernel.ManagedTransfer, error) {
	if s == nil || s.db == nil || ctx == nil || id == "" {
		return kernel.ManagedTransfer{}, kernel.ErrInvalidInput
	}
	return pgLoadManaged(s.db.WithContext(ctx), id)
}
func pgSaveManaged(tx *gorm.DB, t kernel.ManagedTransfer) error {
	b, e := json.Marshal(t)
	if e != nil {
		return e
	}
	h := models.KernelManagedHistoryRecord{SuspensionID: t.SafePoint.ID, RecoveryID: t.SafePoint.RecoveryID, RunID: t.SafePoint.Host.RunID, Revision: t.Revision, Payload: string(b)}
	if e = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "suspension_id"}}, DoUpdates: clause.AssignmentColumns([]string{"revision", "payload"})}).Create(&h).Error; e != nil {
		return translateKernelError(e)
	}
	r := models.KernelManagedTransferRecord{RunID: h.RunID, SuspensionID: h.SuspensionID, RecoveryID: h.RecoveryID, Revision: h.Revision, Payload: h.Payload}
	return translateKernelError(tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "run_id"}}, DoUpdates: clause.AssignmentColumns([]string{"suspension_id", "recovery_id", "revision", "payload"})}).Create(&r).Error)
}
func pgManagedGate(tx *gorm.DB, id string) error {
	t, e := pgLoadManaged(tx, id)
	if errors.Is(e, kernel.ErrNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	return kernel.ManagedOrdinaryGate(t)
}
func pgManagedLease(r models.KernelRunRecord) kernel.ExecutionLease {
	l := kernel.ExecutionLease{RunID: r.RunID, OwnerID: r.ExecutionOwner, LeaseID: r.ExecutionLeaseID, Epoch: r.ExecutionEpoch}
	if r.ExecutionExpiresAt != nil {
		l.ExpiresAt = *r.ExecutionExpiresAt
	}
	return l
}
func pgManagedGrant(tx *gorm.DB, id string) (kernel.ChildExecutionGrant, error) {
	var rows []models.KernelChildEffectGrantRecord
	if e := tx.Where("parent_run_id = ? OR child_run_id = ?", id, id).Find(&rows).Error; e != nil {
		return kernel.ChildExecutionGrant{}, translateKernelError(e)
	}
	if len(rows) != 1 || rows[0].ParentRunID != id || rows[0].ChildRunID != id || rows[0].SettledRevision != 0 || rows[0].CallbackReturnRevision != 0 {
		return kernel.ChildExecutionGrant{}, kernel.ErrExecutionConflict
	}
	return pgPhysicalGrant(rows[0]), nil
}
func (s *KernelStore) managedTx(ctx context.Context, id string, f func(*gorm.DB, models.KernelRunRecord, kernel.Snapshot, kernel.ManagedTransfer, time.Time) (kernel.ManagedTransfer, error)) (kernel.ManagedTransfer, error) {
	if s == nil || s.db == nil || ctx == nil || id == "" {
		return kernel.ManagedTransfer{}, kernel.ErrInvalidInput
	}
	var out kernel.ManagedTransfer
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, e := lockKernelRun(tx, id)
		if e != nil {
			return e
		}
		snap, e := kernelSnapshotFrom(r)
		if e != nil {
			return e
		}
		t, e := pgLoadManaged(tx, id)
		if e != nil {
			return e
		}
		a, e := pgManagedAuthority(tx)
		if e != nil {
			return e
		}
		if a != t.SafePoint.Host.KernelAuthority {
			return kernel.ErrExecutionConflict
		}
		now, e := pgStoreNow(tx)
		if e != nil {
			return e
		}
		out, e = f(tx, r, snap, t, now)
		return e
	})
	if e != nil {
		return kernel.ManagedTransfer{}, e
	}
	return out, nil
}
func (s *KernelStore) ParkManagedRecovery(ctx context.Context, q kernel.ManagedParkRequest) (kernel.ManagedTransfer, error) {
	return s.managedTx(ctx, q.RunID, func(tx *gorm.DB, r models.KernelRunRecord, snap kernel.Snapshot, t kernel.ManagedTransfer, now time.Time) (kernel.ManagedTransfer, error) {
		if r.ExecutionQuarantineRevision != 0 {
			return t, kernel.ErrExecutionQuarantined
		}
		a, e := pgManagedAuthority(tx)
		if e != nil {
			return t, e
		}
		g, e := pgManagedGrant(tx, q.RunID)
		if e != nil {
			return t, e
		}
		n, e := kernel.ManagedParkTransfer(t, q, snap, pgManagedLease(r), g, a, now)
		if e != nil {
			return t, e
		}
		if n.Revision == t.Revision {
			return n, nil
		}
		return n, pgSaveManaged(tx, n)
	})
}
func (s *KernelStore) LeaveManagedSafePoint(ctx context.Context, q kernel.ManagedLeaveRequest) (kernel.ManagedTransfer, error) {
	return s.managedTx(ctx, q.RunID, func(tx *gorm.DB, r models.KernelRunRecord, snap kernel.Snapshot, t kernel.ManagedTransfer, now time.Time) (kernel.ManagedTransfer, error) {
		if r.ExecutionQuarantineRevision != 0 {
			return t, kernel.ErrExecutionQuarantined
		}
		g, e := pgManagedGrant(tx, q.RunID)
		if e != nil {
			return t, e
		}
		want, _ := kernel.ManagedCurrentAuthority(t)
		if g != want {
			return t, kernel.ErrExecutionConflict
		}
		n, e := kernel.ManagedLeaveTransfer(t, q, snap, pgManagedLease(r), now)
		if e != nil {
			return t, e
		}
		return n, pgSaveManaged(tx, n)
	})
}
func (s *KernelStore) EnableManagedRecovery(ctx context.Context, q kernel.ManagedEnableRequest) (kernel.ManagedTransfer, error) {
	return s.managedTx(ctx, q.RunID, func(tx *gorm.DB, r models.KernelRunRecord, snap kernel.Snapshot, t kernel.ManagedTransfer, now time.Time) (kernel.ManagedTransfer, error) {
		// Only the SAME caller gets its exact committed activation after ACK loss.
		if kernel.ManagedEnableReplay(t, q, snap, pgManagedLease(r), now) {
			return t, nil
		}
		if r.ExecutionEpoch >= math.MaxInt64 || r.ExecutionQuarantineRevision != 0 {
			return t, kernel.ErrExecutionConflict
		}
		g, e := pgManagedGrant(tx, q.RunID)
		if e != nil {
			return t, e
		}
		want, _ := kernel.ManagedCurrentAuthority(t)
		if g != want {
			return t, kernel.ErrExecutionConflict
		}
		if !g.MatchesExecution(pgManagedLease(r)) {
			return t, kernel.ErrExecutionConflict
		}
		var duplicate int64
		if e = tx.Model(&models.KernelChildEffectGrantRecord{}).Where("grant_id = ?", q.GrantID).Count(&duplicate).Error; e != nil {
			return t, translateKernelError(e)
		}
		if duplicate != 0 {
			return t, kernel.ErrExecutionConflict
		}
		n, e := kernel.ManagedEnabledTransfer(t, q, snap, r.ExecutionEpoch, now)
		if e != nil {
			return t, e
		}
		if e = tx.Where("grant_id = ?", g.ID).Delete(&models.KernelChildEffectGrantRecord{}).Error; e != nil {
			return t, translateKernelError(e)
		}
		row := pgPhysicalRecord(n.SuccessorGrant, q.RunID)
		if e = tx.Create(&row).Error; e != nil {
			return t, translateKernelApplyError(e)
		}
		// Authority-only handover must not invoke GORM UpdatedAt hooks or alter
		// the sealed aggregate needed by same-call ACK replay and Leave.
		if e = tx.Model(&models.KernelRunRecord{}).Where("run_id = ?", q.RunID).UpdateColumns(map[string]any{"execution_epoch": n.SuccessorLease.Epoch, "execution_owner": n.SuccessorLease.OwnerID, "execution_lease_id": n.SuccessorLease.LeaseID, "execution_expires_at": n.SuccessorLease.ExpiresAt}).Error; e != nil {
			return t, translateKernelError(e)
		}
		return n, pgSaveManaged(tx, n)
	})
}
func (s *KernelStore) CancelParkedManagedContinuation(ctx context.Context, q kernel.ManagedCancelRequest) (kernel.ManagedTransfer, error) {
	return s.managedTx(ctx, q.RunID, func(tx *gorm.DB, r models.KernelRunRecord, snap kernel.Snapshot, t kernel.ManagedTransfer, now time.Time) (kernel.ManagedTransfer, error) {
		if kernel.ManagedCancelReplay(t, q, snap) {
			return t, nil
		}
		if r.ExecutionEpoch >= math.MaxInt64 || r.ExecutionQuarantineRevision != 0 {
			return t, kernel.ErrExecutionConflict
		}
		g, e := pgManagedGrant(tx, q.RunID)
		if e != nil {
			return t, e
		}
		want, _ := kernel.ManagedCurrentAuthority(t)
		if g != want {
			return t, kernel.ErrExecutionConflict
		}
		if !g.MatchesExecution(pgManagedLease(r)) {
			return t, kernel.ErrExecutionConflict
		}
		n, m, e := kernel.ManagedCancellationMutation(t, q, snap, now)
		if e != nil {
			return t, e
		}
		record, e := kernelRunRecordFrom(m.Record, 0)
		if e != nil {
			return t, e
		}
		terminal, e := s.applyKernelMutation(tx, q.RunID, snap.Run.Revision, record, m, nil, true, true, false)
		if e != nil {
			return t, e
		}
		n.Terminal = &terminal
		if e = tx.Where("grant_id = ?", g.ID).Delete(&models.KernelChildEffectGrantRecord{}).Error; e != nil {
			return t, translateKernelError(e)
		}
		return n, pgSaveManaged(tx, n)
	})
}

// Called only after ordinary authority validation and inside its transaction.
func pgManagedMutation(tx *gorm.DB, current kernel.Snapshot, m kernel.StoreMutation, lease *kernel.ExecutionLease) error {
	old, e := pgLoadManaged(tx, current.Run.ID)
	if e != nil && !errors.Is(e, kernel.ErrNotFound) {
		return e
	}
	if e == nil {
		if e = kernel.ManagedOrdinaryGate(old); e != nil {
			return e
		}
		n := kernel.ManagedInvalidateMutation(old)
		if n.Revision != old.Revision {
			if e = pgSaveManaged(tx, n); e != nil {
				return e
			}
		}
	}
	if !m.ManagedSafePoint.Present() {
		return nil
	}
	if len(m.Events) != 1 || m.Events[0].Type != "agent.model_invocation.pending" {
		return kernel.ErrExecutionConflict
	}
	if lease == nil {
		return kernel.ErrExecutionConflict
	}
	g, e := pgManagedGrant(tx, current.Run.ID)
	if e != nil {
		return e
	}
	a, e := pgManagedAuthority(tx)
	if e != nil {
		return e
	}
	p := m.ManagedSafePoint.Point()
	if p.Host.KernelAuthority != a || !m.ManagedSafePoint.Matches(current, m.Record, *lease, g) {
		return kernel.ErrExecutionConflict
	}
	var count int64
	if e = tx.Model(&models.KernelManagedHistoryRecord{}).Where("suspension_id = ? OR recovery_id = ?", p.ID, p.RecoveryID).Count(&count).Error; e != nil {
		return translateKernelError(e)
	}
	if count != 0 {
		return kernel.ErrExecutionConflict
	}
	return pgSaveManaged(tx, kernel.ManagedInitialTransfer(p))
}

func (s *KernelStore) LoadManagedRecoveryByID(ctx context.Context, id string) (kernel.ManagedTransfer, error) {
	if s == nil || s.db == nil || ctx == nil || id == "" {
		return kernel.ManagedTransfer{}, kernel.ErrInvalidInput
	}
	var r models.KernelManagedHistoryRecord
	if e := s.db.WithContext(ctx).Where("suspension_id = ?", id).First(&r).Error; e != nil {
		return kernel.ManagedTransfer{}, translateKernelError(e)
	}
	var t kernel.ManagedTransfer
	if e := json.Unmarshal([]byte(r.Payload), &t); e != nil || t.SafePoint.ID != id || t.SafePoint.RecoveryID != r.RecoveryID || t.SafePoint.Host.RunID != r.RunID || t.Revision != r.Revision || !t.SafePoint.Valid() {
		return t, kernel.ErrExecutionConflict
	}
	return t, nil
}
func (s *KernelStore) CompleteManagedRecovery(ctx context.Context, g kernel.ChildExecutionGrant, snapshot kernel.Snapshot, p kernel.ManagedCompletionProof) error {
	if s == nil || s.db == nil || ctx == nil || !g.IsOwned() {
		return kernel.ErrInvalidInput
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, e := lockKernelRun(tx, g.ChildRunID)
		if e != nil {
			return e
		}
		actual, e := kernelSnapshotFrom(r)
		if e != nil {
			return e
		}
		var rows []models.KernelManagedHistoryRecord
		if e = tx.Where("run_id = ?", g.ChildRunID).Find(&rows).Error; e != nil {
			return translateKernelError(e)
		}
		matches := false
		for _, row := range rows {
			var t kernel.ManagedTransfer
			if e = json.Unmarshal([]byte(row.Payload), &t); e != nil || t.SafePoint.ID != row.SuspensionID || t.SafePoint.RecoveryID != row.RecoveryID || t.SafePoint.Host.RunID != row.RunID || t.Revision != row.Revision || !t.SafePoint.Valid() {
				return kernel.ErrExecutionConflict
			}
			if t.SuccessorGrant == g && t.Selection.Valid() {
				matches = true
			}
		}
		if !matches {
			return nil
		}
		authority, e := pgManagedAuthority(tx)
		if e != nil {
			return e
		}
		if kernel.ManagedDigest(actual) != kernel.ManagedDigest(snapshot) || !p.Matches(g, actual) {
			return kernel.ErrExecutionConflict
		}
		if e = pgManagedGate(tx, g.ChildRunID); e != nil {
			return e
		}
		var grantRows []models.KernelChildEffectGrantRecord
		if e = tx.Where("parent_run_id = ? OR child_run_id = ?", g.ChildRunID, g.ChildRunID).Find(&grantRows).Error; e != nil {
			return translateKernelError(e)
		}
		if len(grantRows) != 1 || pgPhysicalGrant(grantRows[0]) != g || grantRows[0].SettledRevision != actual.Run.Revision {
			return kernel.ErrExecutionConflict
		}
		current, ce := pgLoadManaged(tx, g.ChildRunID)
		if ce != nil && !errors.Is(ce, kernel.ErrNotFound) {
			return ce
		}
		for _, row := range rows {
			var t kernel.ManagedTransfer
			if e = json.Unmarshal([]byte(row.Payload), &t); e != nil {
				return kernel.ErrExecutionConflict
			}
			if t.SuccessorGrant != g || !t.Selection.Valid() {
				continue
			}
			if t.Selection.Mode != kernel.ManagedResume || t.SafePoint.Host.KernelAuthority != authority {
				return kernel.ErrExecutionConflict
			}
			if t.State == kernel.ManagedClosed {
				if t.Terminal == nil || kernel.ManagedDigest(*t.Terminal) != kernel.ManagedDigest(actual) {
					return kernel.ErrExecutionConflict
				}
				continue
			}
			if t.State != kernel.ManagedInvalidated && t.State != kernel.ManagedEnabled {
				return kernel.ErrExecutionConflict
			}
			t.State = kernel.ManagedClosed
			t.Revision++
			t.Terminal = &actual
			if ce == nil && current.SafePoint.ID == t.SafePoint.ID {
				if e = pgSaveManaged(tx, t); e != nil {
					return e
				}
			} else {
				b, e := json.Marshal(t)
				if e != nil {
					return e
				}
				if e = tx.Model(&models.KernelManagedHistoryRecord{}).Where("suspension_id = ? AND revision = ?", row.SuspensionID, row.Revision).Updates(map[string]any{"revision": t.Revision, "payload": string(b)}).Error; e != nil {
					return translateKernelError(e)
				}
			}
		}
		return nil
	})
}

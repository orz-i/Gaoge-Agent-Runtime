package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget/sqlledger"
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
	if t.SafePoint.Host.RunID != id || t.SafePoint.ID != r.SuspensionID || t.SafePoint.RecoveryID != r.RecoveryID || t.Revision != r.Revision || !t.Valid() {
		return t, kernel.ErrExecutionConflict
	}
	return t, nil
}
func (s *KernelStore) LoadManagedRecovery(ctx context.Context, id string) (kernel.ManagedTransfer, error) {
	if s == nil || s.db == nil || ctx == nil || id == "" {
		return kernel.ManagedTransfer{}, kernel.ErrInvalidInput
	}
	t, e := pgLoadManaged(s.db.WithContext(ctx), id)
	return pgObserveManaged(s.db.WithContext(ctx), t, e)
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

// A current owned Grant must have crossed native entry before any ordinary
// write or receipt operation. No managed history is consulted.
func pgNativeEntryGate(tx *gorm.DB, id string) error {
	var counts struct{ Pending, Valid int64 }
	if err := tx.Raw(`SELECT count(*) AS pending, COALESCE(SUM(CASE WHEN g.execution_epoch=r.execution_epoch AND r.execution_physical_epoch<g.execution_epoch AND g.execution_owner_id=r.execution_owner AND g.execution_lease_id=r.execution_lease_id AND r.execution_quarantine_revision=0 THEN 1 ELSE 0 END),0) AS valid FROM agent_kernel_child_effect_grants g JOIN agent_kernel_runs r ON r.run_id=g.child_run_id WHERE g.child_run_id=? AND g.parent_run_id=g.child_run_id AND g.execution_epoch<>0 AND (g.execution_epoch<>r.execution_epoch OR g.execution_epoch<>r.execution_physical_epoch)`, id).Scan(&counts).Error; err != nil {
		return translateKernelError(err)
	}
	if counts.Pending == 1 && counts.Valid == 1 {
		return errors.Join(kernel.ErrExecutionConflict, kernel.ErrChildExecutionBusy)
	}
	if counts.Pending != 0 {
		return kernel.ErrExecutionConflict
	}
	return nil
}
func pgManagedLease(r models.KernelRunRecord) kernel.ExecutionLease {
	l := kernel.ExecutionLease{RunID: r.RunID, OwnerID: r.ExecutionOwner, LeaseID: r.ExecutionLeaseID, Epoch: r.ExecutionEpoch}
	if r.ExecutionExpiresAt != nil {
		l.ExpiresAt = r.ExecutionExpiresAt.UTC()
	}
	return l
}
func pgManagedGrant(tx *gorm.DB, id string) (kernel.ChildExecutionGrant, error) {
	var rows []models.KernelChildEffectGrantRecord
	if e := tx.Where("parent_run_id = ? OR child_run_id = ?", id, id).Find(&rows).Error; e != nil {
		return kernel.ChildExecutionGrant{}, translateKernelError(e)
	}
	if len(rows) != 1 || rows[0].ParentRunID != id || rows[0].ChildRunID != id || rows[0].SettledRevision != 0 || pgCallbackCompletion(rows[0]) != (kernel.PhysicalCallbackCompletion{}) {
		return kernel.ChildExecutionGrant{}, kernel.ErrExecutionConflict
	}
	g := pgPhysicalGrant(rows[0])
	if _, err := pgLoadOwnedPhysicalGrant(tx, g); err != nil {
		return kernel.ChildExecutionGrant{}, err
	}
	return g, nil
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
func (s *KernelStore) CommitManagedCancellation(ctx context.Context, q kernel.ManagedCancelRequest) (kernel.ManagedTransfer, error) {
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
		want := kernel.ManagedCurrentGrant(t)
		if g != want {
			return t, kernel.ErrExecutionConflict
		}
		if !g.MatchesExecution(pgManagedLease(r)) {
			return t, kernel.ErrExecutionConflict
		}
		t, e = kernel.PrepareManagedAdmission(t, q.Selection, snap, pgManagedLease(r), g, t.SafePoint.Host.KernelAuthority, r.ExecutionPhysicalEpoch, now)
		if e != nil {
			return t, e
		}
		// Create locks the same ancestor row; inspect original descendant
		// Runs and unresolved Grants before committing native termination.
		var busy bool
		if e = tx.Raw(`WITH RECURSIVE children(id) AS (
			SELECT run_id FROM agent_kernel_runs WHERE parent_run_id=? OR logical_parent_run_id=?
			UNION SELECT r.run_id FROM agent_kernel_runs r JOIN children c ON r.parent_run_id=c.id OR r.logical_parent_run_id=c.id)
			SELECT EXISTS(SELECT 1 FROM children c JOIN agent_kernel_runs r ON r.run_id=c.id
			WHERE r.status NOT IN ('completed','failed','cancelled') OR r.execution_quarantine_revision<>0
			OR EXISTS(SELECT 1 FROM agent_kernel_child_effect_grants g WHERE g.child_run_id=c.id))`, q.RunID, q.RunID).Scan(&busy).Error; e != nil {
			return t, translateKernelError(e)
		}
		if busy {
			return t, kernel.ErrExecutionConflict
		}
		n, m, e := kernel.ManagedCancellationMutation(t, q, snap, now)
		if e != nil {
			return t, e
		}
		accounting := sqlledger.Store{DB: tx.Statement.ConnPool, Lock: tx.Name() == "postgres"}
		ledger, e := kernel.ManagedCancellationLedger(ctx, t, accounting)
		if e != nil {
			return t, e
		}
		if e = accounting.Apply(ctx, q.RunID, ledger); e != nil {
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
		if e = pgConsumeManagedGrant(tx, g); e != nil {
			return t, e
		}
		return n, pgSaveManaged(tx, n)
	})
}

// Prepare only after ordinary authority validation and inside its transaction.
// The caller binds the verified point to the actual committed aggregate after
// applyKernelMutation succeeds, before this same transaction can commit.
func pgManagedMutation(tx *gorm.DB, current kernel.Snapshot, m kernel.StoreMutation, lease *kernel.ExecutionLease) (*kernel.ManagedSafePoint, error) {
	if !m.ManagedSafePoint.Present() {
		return nil, nil
	}
	if lease == nil {
		return nil, kernel.ErrExecutionConflict
	}
	g, e := pgManagedGrant(tx, current.Run.ID)
	if e != nil {
		return nil, e
	}
	a, e := pgManagedAuthority(tx)
	if e != nil {
		return nil, e
	}
	p := m.ManagedSafePoint.Point()
	if p.Host.KernelAuthority != a || !m.ManagedSafePoint.MatchesMutation(current, m.Record, *lease, g, m.Events) {
		return nil, kernel.ErrExecutionConflict
	}
	var count int64
	if e = tx.Model(&models.KernelManagedHistoryRecord{}).Where("suspension_id = ? OR recovery_id = ?", p.ID, p.RecoveryID).Count(&count).Error; e != nil {
		return nil, translateKernelError(e)
	}
	if count != 0 {
		return nil, kernel.ErrExecutionConflict
	}
	return &p, nil
}

func (s *KernelStore) LoadManagedRecoveryByID(ctx context.Context, id string) (kernel.ManagedTransfer, error) {
	if s == nil || s.db == nil || ctx == nil || id == "" {
		return kernel.ManagedTransfer{}, kernel.ErrInvalidInput
	}
	t, e := pgLoadManagedByID(s.db.WithContext(ctx), id)
	return pgObserveManaged(s.db.WithContext(ctx), t, e)
}
func pgLoadManagedByID(tx *gorm.DB, id string) (kernel.ManagedTransfer, error) {
	var r models.KernelManagedHistoryRecord
	if e := tx.Where("suspension_id = ?", id).First(&r).Error; e != nil {
		return kernel.ManagedTransfer{}, translateKernelError(e)
	}
	var t kernel.ManagedTransfer
	if e := json.Unmarshal([]byte(r.Payload), &t); e != nil || t.SafePoint.ID != id || t.SafePoint.RecoveryID != r.RecoveryID || t.SafePoint.Host.RunID != r.RunID || t.Revision != r.Revision || !t.Valid() {
		return t, kernel.ErrExecutionConflict
	}
	return t, nil
}

func pgObserveManaged(tx *gorm.DB, t kernel.ManagedTransfer, e error) (kernel.ManagedTransfer, error) {
	if e != nil || t.SuccessorGrant.ID == "" || t.Terminal != nil {
		return t, e
	}
	r, e := pgLoadOwnedRetirement(tx, t.SuccessorGrant.ID)
	if errors.Is(e, kernel.ErrNotFound) {
		return t, nil
	}
	if e != nil {
		return t, e
	}
	return kernel.ObserveManagedRetirement(t, r)
}

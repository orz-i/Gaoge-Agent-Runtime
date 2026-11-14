package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
)

var _ kernel.OwnedRetirementStore = (*KernelStore)(nil)

func pgDecodeOwnedSettlement(raw string, g kernel.ChildExecutionGrant, s kernel.Snapshot, c kernel.OwnedPhysicalCallbackCompletion) (kernel.OwnedSettlementEvidence, error) {
	var e kernel.OwnedSettlementEvidence
	if err := json.Unmarshal([]byte(raw), &e); err != nil || !e.Valid() || e.Grant != g || kernel.ManagedDigest(e.Snapshot) != kernel.ManagedDigest(s) || e.Completion != c {
		return e, kernel.ErrExecutionConflict
	}
	return e, nil
}
func pgOwnedMarkEvidence(row models.KernelChildEffectGrantRecord, g kernel.ChildExecutionGrant, s kernel.Snapshot, p kernel.ChildExecutionSettlement) (string, error) {
	c := pgCallbackCompletion(row)
	if row.SettlementEvidence != "" {
		if row.SettledRevision != s.Run.Revision {
			return "", kernel.ErrExecutionConflict
		}
		_, e := pgDecodeOwnedSettlement(row.SettlementEvidence, g, s, c)
		return row.SettlementEvidence, e
	}
	origin := "OWNER_RETURN"
	if p.IsRecovery() {
		origin = "RECOVERED_RETURN"
	}
	if c.GrantID == "" {
		origin = "LEGACY_UNKNOWN"
	}
	e := kernel.OwnedSettlementEvidence{Version: 1, Grant: g, Snapshot: s, Completion: c, MarkOrigin: origin}
	if !e.Valid() {
		return "", kernel.ErrExecutionConflict
	}
	b, err := json.Marshal(e)
	return string(b), err
}
func pgOwnedRetirementClosures(tx *gorm.DB, g kernel.ChildExecutionGrant, s kernel.Snapshot) ([]kernel.ManagedTransfer, error) {
	var rows []models.KernelManagedHistoryRecord
	if e := tx.Where("run_id = ?", g.ChildRunID).Find(&rows).Error; e != nil {
		return nil, translateKernelError(e)
	}
	histories := make([]kernel.ManagedTransfer, 0, len(rows))
	for _, row := range rows {
		var t kernel.ManagedTransfer
		if e := json.Unmarshal([]byte(row.Payload), &t); e != nil || !t.SafePoint.Valid() || t.SafePoint.ID != row.SuspensionID || t.SafePoint.RecoveryID != row.RecoveryID || t.SafePoint.Host.RunID != row.RunID || t.Revision != row.Revision {
			return nil, kernel.ErrExecutionConflict
		}
		histories = append(histories, t)
	}
	a, e := pgManagedAuthority(tx)
	if e != nil {
		return nil, e
	}
	return kernel.ValidateOwnedRetirementClosures(g, s, a, histories)
}
func pgPersistOwnedRetirement(tx *gorm.DB, row models.KernelChildEffectGrantRecord, g kernel.ChildExecutionGrant, s kernel.Snapshot) error {
	if row.SettledRevision == 0 || row.SettledRevision != s.Run.Revision {
		return kernel.ErrExecutionConflict
	}
	c := pgCallbackCompletion(row)
	var evidence kernel.OwnedSettlementEvidence
	var e error
	if row.SettlementEvidence != "" {
		evidence, e = pgDecodeOwnedSettlement(row.SettlementEvidence, g, s, c)
		if e != nil {
			return e
		}
	} else {
		evidence = kernel.OwnedSettlementEvidence{Version: 1, Grant: g, Snapshot: s, Completion: c, MarkOrigin: "LEGACY_UNKNOWN"}
		if !evidence.Valid() {
			return kernel.ErrExecutionConflict
		}
	}
	closures, e := pgOwnedRetirementClosures(tx, g, s)
	if e != nil {
		return e
	}
	r := kernel.OwnedRetirementReceipt{Version: 1, Evidence: evidence, Closures: closures, Phase: "RETIRED"}
	if !r.Valid() {
		return kernel.ErrExecutionConflict
	}
	payload, e := json.Marshal(r)
	if e != nil {
		return e
	}
	value := models.KernelOwnedRetirementRecord{GrantID: g.ID, RunID: g.ChildRunID, SettledRevision: s.Run.Revision, Version: r.Version, Phase: r.Phase, Payload: string(payload)}
	e = tx.Create(&value).Error
	// Even an identical old receipt beside an active grant is impossible state.
	if isKernelUniqueConstraint(e) {
		return kernel.ErrExecutionConflict
	}
	return translateKernelError(e)
}
func pgLoadOwnedRetirement(tx *gorm.DB, id string) (kernel.OwnedRetirementReceipt, error) {
	var row models.KernelOwnedRetirementRecord
	if e := tx.Where("grant_id = ?", id).First(&row).Error; e != nil {
		return kernel.OwnedRetirementReceipt{}, translateKernelError(e)
	}
	var r kernel.OwnedRetirementReceipt
	if e := json.Unmarshal([]byte(row.Payload), &r); e != nil || !r.Valid() || r.Evidence.Grant.ID != row.GrantID || r.Evidence.Grant.ChildRunID != row.RunID || r.Evidence.Snapshot.Run.Revision != row.SettledRevision || r.Version != row.Version || r.Phase != row.Phase {
		return r, kernel.ErrExecutionConflict
	}
	return r, nil
}
func (s *KernelStore) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	if s == nil || s.db == nil || ctx == nil || id == "" {
		return kernel.OwnedRetirementReceipt{}, kernel.ErrInvalidInput
	}
	return pgLoadOwnedRetirement(s.db.WithContext(ctx), id)
}
func (s *KernelStore) AcknowledgeOwnedRetirement(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.OwnedRetirementAcknowledgement) (kernel.OwnedRetirementAckResult, error) {
	if s == nil || s.db == nil || ctx == nil || !g.IsOwned() || g.ID == "" {
		return kernel.OwnedRetirementAckResult{}, kernel.ErrInvalidInput
	}
	var out kernel.OwnedRetirementAckResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		run, e := lockKernelRun(tx, g.ChildRunID)
		if e != nil {
			return e
		}
		receipt, e := pgLoadOwnedRetirement(tx, g.ID)
		if e != nil {
			if errors.Is(e, kernel.ErrNotFound) {
				return kernel.ErrExecutionConflict
			}
			return e
		}
		current, e := kernelSnapshotFrom(run)
		if e != nil {
			return e
		}
		var count int64
		if e = tx.Model(&models.KernelChildEffectGrantRecord{}).Where("parent_run_id = ? OR child_run_id = ?", g.ChildRunID, g.ChildRunID).Count(&count).Error; e != nil {
			return translateKernelError(e)
		}
		updated, result, release, e := kernel.ReduceOwnedRetirementACK(receipt, g, p, current, run.ExecutionEpoch, pgManagedLease(run), count != 0)
		if e != nil {
			return e
		}
		out = result
		if result.PreviouslyAcknowledged {
			return nil
		}
		if e = pgManagedGate(tx, g.ChildRunID); e != nil {
			return e
		}
		if run.ExecutionQuarantineRevision != 0 {
			return kernel.ErrExecutionConflict
		}
		if release {
			update := tx.Model(&models.KernelRunRecord{}).Where("run_id = ? AND execution_epoch = ? AND execution_owner = ? AND execution_lease_id = ?", g.ChildRunID, g.ExecutionEpoch, g.ExecutionOwnerID, g.ExecutionLeaseID).UpdateColumns(map[string]any{"execution_owner": "", "execution_lease_id": "", "execution_expires_at": nil})
			if update.Error != nil {
				return translateKernelError(update.Error)
			}
			if update.RowsAffected != 1 {
				return kernel.ErrExecutionConflict
			}
		}
		raw, e := json.Marshal(updated)
		if e != nil {
			return e
		}
		update := tx.Model(&models.KernelOwnedRetirementRecord{}).Where("grant_id = ? AND run_id = ? AND phase = ? AND version = ? AND settled_revision = ?", g.ID, g.ChildRunID, "RETIRED", receipt.Version, receipt.Evidence.Snapshot.Run.Revision).UpdateColumns(map[string]any{"phase": updated.Phase, "payload": string(raw)})
		if update.Error != nil {
			return translateKernelError(update.Error)
		}
		if update.RowsAffected != 1 {
			return kernel.ErrExecutionConflict
		}
		return nil
	})
	if err != nil {
		return kernel.OwnedRetirementAckResult{}, err
	}
	return out, nil
}

func (s *KernelStore) LookupOwnedRetirementForRun(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	if s == nil || s.db == nil || ctx == nil || id == "" {
		return kernel.OwnedRetirementReceipt{}, kernel.ErrInvalidInput
	}
	var out kernel.OwnedRetirementReceipt
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		run, e := lockKernelRun(tx, id)
		if e != nil {
			return e
		}
		if e = pgManagedGate(tx, id); e != nil {
			return e
		}
		if run.ExecutionQuarantineRevision != 0 {
			return kernel.ErrExecutionConflict
		}
		snapshot, e := kernelSnapshotFrom(run)
		if e != nil {
			return e
		}
		var records []models.KernelOwnedRetirementRecord
		if e = tx.Where("run_id = ?", id).Find(&records).Error; e != nil {
			return translateKernelError(e)
		}
		receipts := make([]kernel.OwnedRetirementReceipt, 0, len(records))
		for _, row := range records {
			r, e := pgLoadOwnedRetirement(tx, row.GrantID)
			if e != nil {
				return e
			}
			if r.Evidence.Grant.ChildRunID != id {
				return kernel.ErrExecutionConflict
			}
			receipts = append(receipts, r)
		}
		var count int64
		if e = tx.Model(&models.KernelChildEffectGrantRecord{}).Where("parent_run_id = ? OR child_run_id = ?", id, id).Count(&count).Error; e != nil {
			return translateKernelError(e)
		}
		out, e = kernel.SelectOwnedRetirementForRun(id, receipts, snapshot, run.ExecutionEpoch, pgManagedLease(run), count != 0)
		return e
	})
	return out, e
}

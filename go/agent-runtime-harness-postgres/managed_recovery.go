package harnesspostgres

import (
	"context"
	"encoding/json"
	"errors"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const managedAuthorityKey = "managed-recovery-authority/v1"

type managedAuthorityRecord struct {
	ID         string `gorm:"primaryKey;size:64"`
	Namespace  string `gorm:"size:64;not null"`
	Generation uint64 `gorm:"not null"`
	Version    uint32 `gorm:"not null"`
}

func (managedAuthorityRecord) TableName() string { return "agent_harness_managed_authority" }

type managedReservationRecord struct {
	ClosureHash string `gorm:"size:64;not null;default:''"`
	DispatchID  string `gorm:"primaryKey;size:128"`
	RecoveryID  string `gorm:"uniqueIndex;size:128;not null"`
	TurnID      string `gorm:"index;size:64;not null"`
	RecordJSON  string `gorm:"type:text;not null"`
}

func (managedReservationRecord) TableName() string { return "agent_harness_managed_recovery" }
func initializeManagedAuthority(tx *gorm.DB) error {
	a, err := harness.NewManagedHostAuthority()
	if err != nil {
		return err
	}
	row := managedAuthorityRecord{ID: managedAuthorityKey, Namespace: a.Namespace, Generation: a.Generation, Version: a.Version}
	if err = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	_, err = (&Store{db: tx}).ManagedRecoveryAuthority(tx.Statement.Context)
	return err
}
func (s *Store) ManagedRecoveryAuthority(ctx context.Context) (kernel.ManagedAuthority, error) {
	var row managedAuthorityRecord
	if err := s.db.WithContext(ctx).Where("id = ?", managedAuthorityKey).Take(&row).Error; err != nil {
		return kernel.ManagedAuthority{}, mapError(err)
	}
	a := kernel.ManagedAuthority{Namespace: row.Namespace, Generation: row.Generation, Version: row.Version}
	if !a.Valid() {
		return kernel.ManagedAuthority{}, harness.ErrConflict
	}
	return a, nil
}
func managedRows(tx *gorm.DB, b kernel.ManagedHostBinding, closed ...bool) (harness.Turn, harness.Invocation, []harness.Item, error) {
	s := &Store{db: tx}
	ctx := tx.Statement.Context
	a, err := s.ManagedRecoveryAuthority(ctx)
	if err != nil {
		return harness.Turn{}, harness.Invocation{}, nil, err
	}
	t, err := s.GetTurn(ctx, b.TurnID)
	if err != nil {
		return t, harness.Invocation{}, nil, err
	}
	session, err := s.GetSession(ctx, t.SessionID)
	if err != nil {
		return t, harness.Invocation{}, nil, err
	}
	inv, err := s.GetInvocation(ctx, b.InvocationID)
	if err != nil {
		return t, inv, nil, err
	}
	config, err := s.GetConfigSnapshot(ctx, t.ConfigSnapshotID)
	if err != nil {
		return t, inv, nil, err
	}
	dispatch, found, err := findItem(tx, b.DispatchID)
	if err != nil {
		return t, inv, nil, err
	}
	if !found {
		return t, inv, nil, harness.ErrConflict
	}
	// Include globally keyed children of original dispatch/cancellation facts,
	// so damaged foreign-Turn siblings cannot disappear from verification.
	var rows []itemRecord
	parents := tx.Model(&itemRecord{}).Select("id").Where("turn_id = ?", b.TurnID)
	if err = tx.Where("turn_id = ? OR parent_item_id IN (?)", b.TurnID, parents).Order("seq ASC").Find(&rows).Error; err != nil {
		return t, inv, nil, err
	}
	items := make([]harness.Item, 0, len(rows))
	for _, row := range rows {
		item, e := itemFromRecord(row)
		if e != nil {
			return t, inv, nil, e
		}
		items = append(items, item)
	}
	verifyItems := items
	if len(closed) == 1 && closed[0] {
		verifyItems = nil
	}
	if err = harness.ValidateManagedOriginalRows(a, b, session, t, inv, config, dispatch, verifyItems); err != nil {
		return t, inv, nil, err
	}
	return t, inv, items, nil
}
func (s *Store) ReadManagedOriginalDispatch(ctx context.Context, b kernel.ManagedHostBinding) (kernel.ManagedHostBinding, error) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := lockTurn(tx, b.TurnID); e != nil {
			return e
		}
		_, _, _, e := managedRows(tx, b)
		return e
	})
	if err != nil {
		return kernel.ManagedHostBinding{}, err
	}
	b.Source = append([]byte(nil), b.Source...)
	return b, nil
}
func (s *Store) ReserveManagedRecovery(ctx context.Context, p kernel.ManagedParkedProof) (kernel.ManagedHostReservation, error) {
	b := p.Transfer().SafePoint.Host
	if !p.Matches(b) {
		return kernel.ManagedHostReservation{}, harness.ErrConflict
	}
	var out kernel.ManagedHostReservation
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := lockTurn(tx, b.TurnID); e != nil {
			return e
		}
		t, i, items, e := managedRows(tx, b)
		if e != nil {
			return e
		}
		candidate, e := harness.SelectManagedReservation(p, b, t, i, items)
		if e != nil {
			return e
		}
		var row managedReservationRecord
		e = tx.Where("dispatch_id = ?", b.DispatchID).Take(&row).Error
		if e == nil {
			if json.Unmarshal([]byte(row.RecordJSON), &out) != nil || !harness.SameManagedReservation(out, candidate) || row.RecoveryID != out.RecoveryID || row.TurnID != b.TurnID {
				return harness.ErrConflict
			}
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		raw, e := json.Marshal(candidate)
		if e != nil {
			return e
		}
		row = managedReservationRecord{DispatchID: b.DispatchID, RecoveryID: candidate.RecoveryID, TurnID: b.TurnID, RecordJSON: string(raw)}
		if e = tx.Create(&row).Error; e != nil {
			return e
		}
		out = candidate
		return nil
	})
	return out, err
}
func (s *Store) ReadManagedRecovery(ctx context.Context, b kernel.ManagedHostBinding, id string) (kernel.ManagedHostReservation, error) {
	var out kernel.ManagedHostReservation
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := lockTurn(tx, b.TurnID); e != nil {
			return e
		}
		var row managedReservationRecord
		if e := tx.Where("dispatch_id = ?", b.DispatchID).Take(&row).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return kernel.ErrNotFound
			}
			return e
		}
		if json.Unmarshal([]byte(row.RecordJSON), &out) != nil || !out.Valid() || (id != "" && out.RecoveryID != id) || row.RecoveryID != out.RecoveryID || row.TurnID != b.TurnID || kernel.ManagedDigest(out.Binding) != kernel.ManagedDigest(b) {
			return harness.ErrConflict
		}
		_, _, items, e := managedRows(tx, b, row.ClosureHash != "")
		if e != nil {
			return e
		}
		if e = harness.ValidateManagedReservationRows(out, items, row.ClosureHash != ""); e != nil {
			return e
		}
		return nil
	})
	return out, err
}
func checkManagedReservationTx(tx *gorm.DB, turnID string) error {
	var n int64
	if err := tx.Model(&managedReservationRecord{}).Where("turn_id = ? AND closure_hash = ?", turnID, "").Count(&n).Error; err != nil {
		return err
	}
	if n != 0 {
		return harness.ErrConflict
	}
	return nil
}

func checkManagedCompletionTx(tx *gorm.DB, item harness.Item) error {
	if harness.IsManagedDispatchClosure(item) {
		return harness.ErrConflict
	}
	var rows []managedReservationRecord
	if err := tx.Where("(dispatch_id = ? OR turn_id = ?) AND closure_hash = ?", item.ParentItemID, item.TurnID, "").Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		var r kernel.ManagedHostReservation
		if json.Unmarshal([]byte(row.RecordJSON), &r) != nil || !r.Valid() {
			return harness.ErrConflict
		}
		if item.ParentItemID == r.Binding.DispatchID || (r.Mode == kernel.ManagedCancel && item.ParentItemID == r.Cancellation.IntentID) {
			return harness.ErrConflict
		}
	}
	return nil
}

func (s *Store) CloseManagedRecovery(ctx context.Context, p kernel.ManagedClosureProof) error {
	t := p.Transfer()
	if !p.Matches(t.Selection) {
		return harness.ErrConflict
	}
	b := t.SafePoint.Host
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockTurn(tx, b.TurnID); err != nil {
			return err
		}
		a, err := (&Store{db: tx}).ManagedRecoveryAuthority(ctx)
		if err != nil {
			return err
		}
		if a != b.HostAuthority {
			return harness.ErrConflict
		}
		var row managedReservationRecord
		if err = tx.Where("dispatch_id = ?", b.DispatchID).Take(&row).Error; err != nil {
			return mapError(err)
		}
		var r kernel.ManagedHostReservation
		if json.Unmarshal([]byte(row.RecordJSON), &r) != nil || !p.Matches(r) || row.RecoveryID != r.RecoveryID || row.TurnID != b.TurnID {
			return harness.ErrConflict
		}
		digest := kernel.ManagedDigest(struct {
			Transfer kernel.ManagedTransfer
			Snapshot kernel.Snapshot
		}{t, p.Snapshot()})
		if row.ClosureHash != "" {
			if row.ClosureHash != digest {
				return harness.ErrConflict
			}
			return nil
		}
		if _, _, _, err = managedRows(tx, b); err != nil {
			return err
		}
		additions, err := harness.ManagedClosureItems(p, r)
		if err != nil {
			return err
		}
		for _, item := range additions {
			var n int64
			if err = tx.Model(&itemRecord{}).Where("id = ? OR parent_item_id = ?", item.ID, item.ParentItemID).Count(&n).Error; err != nil {
				return err
			}
			if n != 0 {
				return harness.ErrConflict
			}
		}
		for _, item := range additions {
			item.Seq, err = nextItemSeq(tx, item.TurnID)
			if err != nil {
				return err
			}
			record, e := itemToRecord(item)
			if e != nil {
				return e
			}
			if err = tx.Create(&record).Error; err != nil {
				return err
			}
		}
		result := tx.Model(&managedReservationRecord{}).Where("dispatch_id = ? AND closure_hash = ?", b.DispatchID, "").Update("closure_hash", digest)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return harness.ErrConflict
		}
		return nil
	})
}

package harnesspostgres

import (
	"context"

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
	if !found && !b.DirectNativeCommand() {
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
func (s *Store) VerifyManagedOriginalDispatch(ctx context.Context, b kernel.ManagedHostBinding, recovery bool) error {
	if !b.Valid() {
		return harness.ErrConflict
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := lockTurn(tx, b.TurnID); e != nil {
			return e
		}
		t, i, items, e := managedRows(tx, b)
		if e != nil {
			return e
		}
		if recovery {
			return harness.VerifyManagedRecoveryAdmission(b, t, i, items)
		}
		return nil
	})
}

// Retired reservation records are preserved only as migration history.
func rejectOpenManagedReservations(db *gorm.DB) error {
	if !db.Migrator().HasTable(&managedReservationRecord{}) {
		return nil
	}
	var n int64
	if err := db.Model(&managedReservationRecord{}).Where("closure_hash = ?", "").Count(&n).Error; err != nil {
		return err
	}
	if n != 0 {
		return harness.ErrConflict
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
		_, _, items, err := managedRows(tx, b, true)
		if err != nil {
			return err
		}
		if replay, err := harness.ManagedClosureReplay(p, items); err != nil || replay {
			return err
		}
		if _, _, _, err = managedRows(tx, b); err != nil {
			return err
		}
		if err = harness.ManagedClosureAuthorization(p, items); err != nil {
			return err
		}
		additions, err := harness.ManagedClosureItems(p, t.Selection)
		if err != nil {
			return err
		}
		grant, found, err := findItem(tx, b.DispatchID)
		if err != nil {
			return err
		}
		if !found {
			return harness.ErrConflict
		}
		additions[0].Seq, err = nextItemSeq(tx, b.TurnID)
		if err != nil {
			return err
		}
		decision, insert, err := planOriginalApprovalTx(tx, grant, additions[0])
		if err != nil {
			return err
		}
		if insert {
			additions = append([]harness.Item{decision}, additions...)
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
		return nil
	})
}

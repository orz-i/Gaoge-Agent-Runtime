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
func verifyManagedRows(tx *gorm.DB, b kernel.ManagedHostBinding) error {
	s := &Store{db: tx}
	ctx := tx.Statement.Context
	a, err := s.ManagedRecoveryAuthority(ctx)
	if err != nil {
		return err
	}
	t, err := s.GetTurn(ctx, b.TurnID)
	if err != nil {
		return err
	}
	session, err := s.GetSession(ctx, t.SessionID)
	if err != nil {
		return err
	}
	inv, err := s.GetInvocation(ctx, b.InvocationID)
	if err != nil {
		return err
	}
	config, err := s.GetConfigSnapshot(ctx, t.ConfigSnapshotID)
	if err != nil {
		return err
	}
	dispatch, found, err := findItem(tx, b.DispatchID)
	if err != nil {
		return err
	}
	if found {
		return harness.ErrConflict
	}
	// Include globally keyed children of original dispatch/cancellation facts,
	// so damaged foreign-Turn siblings cannot disappear from verification.
	var rows []itemRecord
	parents := tx.Model(&itemRecord{}).Select("id").Where("turn_id = ?", b.TurnID)
	if err = tx.Where("turn_id = ? OR parent_item_id IN (?)", b.TurnID, parents).Order("seq ASC").Find(&rows).Error; err != nil {
		return err
	}
	items := make([]harness.Item, 0, len(rows))
	for _, row := range rows {
		item, e := itemFromRecord(row)
		if e != nil {
			return e
		}
		items = append(items, item)
	}
	return harness.ValidateManagedOriginalRows(a, b, session, t, inv, config, dispatch, items)
}
func (s *Store) VerifyManagedOriginalDispatch(ctx context.Context, b kernel.ManagedHostBinding) error {
	if !b.Valid() {
		return harness.ErrConflict
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := lockTurn(tx, b.TurnID); e != nil {
			return e
		}
		return verifyManagedRows(tx, b)
	})
}

// The old Host reservation table has no writer in the native contract.
// Reject populated retired tables without deleting or interpreting records.
func rejectManagedReservations(db *gorm.DB) error {
	const table = "agent_harness_managed_recovery"
	if !db.Migrator().HasTable(table) {
		return nil
	}
	var n int64
	if err := db.Table(table).Count(&n).Error; err != nil {
		return err
	}
	if n != 0 {
		return harness.ErrConflict
	}
	return nil
}

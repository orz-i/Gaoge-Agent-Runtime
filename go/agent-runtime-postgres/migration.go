package postgres

import (
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget/sqlledger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrNilDatabase = errors.New("postgres kernel store database is nil")
var ErrUnsupportedSchema = errors.New("unsupported PostgreSQL Kernel profile: stop the previous writer and initialize a fresh profile")

// Models returns the GORM schema models managed by this adapter.
// Hosts must initialize and upgrade databases through Migrate(db) rather than
// calling AutoMigrate(Models()...) directly, which skips managed authority and
// the current profile boundary.
func Models() []interface{} {
	return []interface{}{
		&models.KernelRunRecord{}, &models.KernelRunIdentityRecord{}, &models.KernelEventRecord{}, &models.KernelTransitionOutboxRecord{},
		&models.KernelEffectRecord{}, &models.KernelEffectOwnerRecord{},
		&models.KernelManagedAuthorityRecord{}, &models.KernelManagedHistoryRecord{},
		&models.RunRelationRecord{}, &sqlledger.Record{},
	}
}

func Migrate(db *gorm.DB) error {
	if db == nil {
		return ErrNilDatabase
	}
	return db.Transaction(func(tx *gorm.DB) error {
		// A stopped-writer hard cut, never an implicit upgrade of a live
		// original execution or its permanent identity.
		for _, old := range []string{"agent_kernel_child_effect_grants", "agent_kernel_grant_identities", "agent_kernel_owned_retirements", "agent_kernel_managed_transfers"} {
			if tx.Migrator().HasTable(old) {
				return ErrUnsupportedSchema
			}
		}
		if tx.Migrator().HasTable(&models.KernelRunRecord{}) &&
			(!tx.Migrator().HasTable(&models.KernelEffectRecord{}) || !tx.Migrator().HasTable(&models.KernelEffectOwnerRecord{})) {
			return ErrUnsupportedSchema
		}
		if err := tx.AutoMigrate(Models()...); err != nil {
			return err
		}
		if err := validateNativeBudgetBindings(tx); err != nil {
			return err
		}

		var namespace [32]byte
		if _, err := rand.Read(namespace[:]); err != nil {
			return err
		}
		row := models.KernelManagedAuthorityRecord{Key: "managed-recovery-authority/v1", Namespace: hex.EncodeToString(namespace[:]), Generation: 1, Version: 1}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
	})
}

// During the stopped-writer upgrade, reject existing live Agents registered
// in the shared table without a native Run binding. Terminal legacy history
// and pre-Create registrations without a local Run are retained unchanged.
func validateNativeBudgetBindings(db *gorm.DB) error {
	members := `SELECT b.id AS scope, x.key AS run_id, COALESCE(x.value->>'parentRunID','') AS parent
		FROM agent_harness_budgets b CROSS JOIN LATERAL jsonb_each(COALESCE(NULLIF(b.payload_json::jsonb->'runs','null'::jsonb),'{}'::jsonb)) x`
	if db.Name() == "sqlite" {
		members = `SELECT b.id AS scope, x.key AS run_id, COALESCE(json_extract(x.value,'$.parentRunID'),'') AS parent
			FROM agent_harness_budgets b, json_each(b.payload_json,'$.runs') x`
	}
	var invalid int64
	err := db.Raw(`WITH members AS (` + members + `)
		SELECT COUNT(*) FROM (
			SELECT r.run_id FROM agent_kernel_runs r JOIN members m ON m.run_id=r.run_id
			WHERE (r.budget_scope_id<>'' OR (r.kind='agent' AND r.status IN ('running','waiting_input','paused_budget')))
			  AND (r.budget_scope_id<>m.scope OR r.logical_parent_run_id<>m.parent)
			UNION ALL
			SELECT r.run_id FROM agent_kernel_runs r WHERE r.budget_scope_id<>''
			 AND NOT EXISTS (SELECT 1 FROM members m WHERE m.run_id=r.run_id AND m.scope=r.budget_scope_id AND m.parent=r.logical_parent_run_id)
		) invalid`).Scan(&invalid).Error
	if err != nil {
		return err
	}
	if invalid != 0 {
		return errors.New("postgres kernel migration requires reconciling inconsistent native budget bindings with the previous writer")
	}
	return nil
}

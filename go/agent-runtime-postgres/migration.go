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

// Models returns the GORM schema models managed by this adapter.
// Hosts must initialize and upgrade databases through Migrate(db) rather than
// calling AutoMigrate(Models()...) directly, which skips managed authority and
// permanent Grant identity backfill.
func Models() []interface{} {
	return []interface{}{
		&models.KernelRunRecord{}, &models.KernelEventRecord{}, &models.KernelTransitionOutboxRecord{},
		&models.KernelChildEffectGrantRecord{}, &models.KernelGrantIdentityRecord{},
		&models.KernelOwnedRetirementRecord{},
		&models.KernelManagedAuthorityRecord{}, &models.KernelManagedTransferRecord{}, &models.KernelManagedHistoryRecord{},
		&models.RunRelationRecord{}, &sqlledger.Record{},
	}
}

func Migrate(db *gorm.DB) error {
	if db == nil {
		return ErrNilDatabase
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(Models()...); err != nil {
			return err
		}
		var pending int64
		if err := tx.Model(&models.KernelOwnedRetirementRecord{}).Where("phase <> ?", "ACKNOWLEDGED").Count(&pending).Error; err != nil {
			return err
		}
		if pending != 0 {
			return errors.New("postgres kernel migration requires draining unfinished original retirements with the previous writer")
		}
		state, selection := "m.payload::jsonb->>'State'", "m.payload::jsonb->'Selection'->>'ID'"
		original := "m.payload::jsonb->'SafePoint'->'OriginalGrant'->>'ID'"
		if tx.Name() == "sqlite" {
			state, selection = "json_extract(m.payload,'$.State')", "json_extract(m.payload,'$.Selection.ID')"
			original = "json_extract(m.payload,'$.SafePoint.OriginalGrant.ID')"
		}
		query := "SELECT count(*) FROM agent_kernel_managed_transfers m WHERE COALESCE(" + state + ",'')<>'' AND " + state + " NOT IN ('CLOSED','CANCELLED') AND (" + state + " IN ('PARKED','ENABLED') OR COALESCE(" + selection + ",'')<>'' OR EXISTS (SELECT 1 FROM agent_kernel_child_effect_grants g WHERE g.child_run_id=m.run_id AND g.grant_id=" + original + " AND g.execution_epoch<>0))"
		if err := tx.Raw(query).Scan(&pending).Error; err != nil {
			return err
		}
		if pending != 0 {
			return errors.New("postgres kernel migration requires draining legacy managed execution with the previous writer")
		}
		if err := validateNativeBudgetBindings(tx); err != nil {
			return err
		}
		if err := pgBackfillGrantIdentities(tx); err != nil {
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
			  AND (r.budget_scope_id<>m.scope OR r.budget_parent_run_id<>m.parent)
			UNION ALL
			SELECT r.run_id FROM agent_kernel_runs r WHERE (r.budget_scope_id<>'' OR r.budget_parent_run_id<>'')
			 AND NOT EXISTS (SELECT 1 FROM members m WHERE m.run_id=r.run_id AND m.scope=r.budget_scope_id AND m.parent=r.budget_parent_run_id)
		) invalid`).Scan(&invalid).Error
	if err != nil {
		return err
	}
	if invalid != 0 {
		return errors.New("postgres kernel migration requires reconciling inconsistent native budget bindings with the previous writer")
	}
	return nil
}

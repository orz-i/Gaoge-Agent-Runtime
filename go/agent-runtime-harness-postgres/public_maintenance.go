package harnesspostgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"gorm.io/gorm"
)

var ErrActorActive = errors.New("harness actor has active or undrained data")

// EraseActorData removes all Harness-owned data in a quiesced actor scope.
// Hosts must stop admissions and compose this with Kernel erasure and account
// deletion in a single transaction. It refuses unresolved Turns, Invocations,
// Interactions and budget reservations, including unknown provider effects.
func (store *Store) EraseActorData(ctx context.Context, tenantID, actorID string) error {
	if store == nil || store.db == nil || tenantID == "" || actorID == "" || strings.TrimSpace(tenantID) != tenantID || strings.TrimSpace(actorID) != actorID {
		return harness.ErrInvalidRequest
	}
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Name() == "postgres" {
			if err := tx.Exec("LOCK TABLE agent_harness_sessions, agent_harness_turns, agent_harness_invocations, agent_harness_interactions, agent_harness_config_snapshots, agent_harness_context_checkpoints, agent_harness_context_heads, agent_harness_context_artifacts, agent_harness_items, agent_harness_budgets IN SHARE ROW EXCLUSIVE MODE").Error; err != nil {
				return err
			}
		}
		sessions := func() *gorm.DB {
			return tx.Model(&sessionRecord{}).Select("id").Where("tenant_id = ? AND actor_id = ?", tenantID, actorID)
		}
		turns := func() *gorm.DB { return tx.Model(&turnRecord{}).Select("id").Where("session_id IN (?)", sessions()) }
		terminal := []string{"completed", "failed", "cancelled"}
		checks := []*gorm.DB{
			tx.Model(&turnRecord{}).Where("session_id IN (?) AND status NOT IN ?", sessions(), terminal),
			tx.Model(&invocationRecord{}).Where("turn_id IN (?) AND status NOT IN ?", turns(), terminal),
			tx.Model(&interactionRecord{}).Where("turn_id IN (?) AND status <> ?", turns(), "resolved"),
			tx.Model(&itemRecord{}).Where("turn_id NOT IN (?) AND parent_item_id IN (?)", turns(), tx.Model(&itemRecord{}).Select("id").Where("turn_id IN (?)", turns())),
		}
		for _, query := range checks {
			var count int64
			if err := query.Count(&count).Error; err != nil {
				return err
			}
			if count != 0 {
				return ErrActorActive
			}
		}
		var budgets []budgetRecord
		if err := tx.Where("id IN (?)", turns()).Find(&budgets).Error; err != nil {
			return err
		}
		for _, row := range budgets {
			var ledger budget.Ledger
			if err := json.Unmarshal([]byte(row.PayloadJSON), &ledger); err != nil {
				return harness.ErrConflict
			}
			if ledger.ID != row.ID {
				return harness.ErrConflict
			}
			for _, reservation := range ledger.Reservations {
				if reservation.Active || reservation.Status != budget.ReservationSettled && reservation.Status != budget.ReservationReleased {
					return ErrActorActive
				}
			}
		}
		// Context ownership is the persisted Session, not an Invocation ID.
		// Cross-table identifier equality cannot authorize another scope's deletion.
		for _, row := range []any{&contextHeadRecord{}, &contextCheckpointRecord{}, &contextArtifactRecord{}} {
			if err := tx.Where("scope_id IN (?)", sessions()).Delete(row).Error; err != nil {
				return err
			}
		}
		for _, row := range []any{&itemRecord{}, &interactionRecord{}, &invocationRecord{}, &configRecord{}} {
			if err := tx.Where("turn_id IN (?)", turns()).Delete(row).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("id IN (?)", turns()).Delete(&budgetRecord{}).Error; err != nil {
			return err
		}
		if err := tx.Where("session_id IN (?)", sessions()).Delete(&turnRecord{}).Error; err != nil {
			return err
		}
		return tx.Where("tenant_id = ? AND actor_id = ?", tenantID, actorID).Delete(&sessionRecord{}).Error
	})
}

package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
)

// ErrActorActive means erasure would remove outstanding execution, delivery or
// cross-actor relation authority. The host must cancel and drain, then retry.
var ErrActorActive = errors.New("runtime actor has active or undrained data")

// EraseActorData removes this adapter's actor-owned aggregates, events and
// relations. It never cancels work or discards unacknowledged transition outbox
// entries. Hosts MUST stop new admissions and compose all adapter erasures and
// account deletion in one transaction, passing that transaction to constructors.
// PostgreSQL table locks prevent concurrent writes during this bounded
// maintenance transaction; host admission fencing must persist after commit.
// Terminal status and an empty transition outbox do not prove physical
// quiescence. The host must independently reconcile unknown provider/tool
// effects, execution ownership/quarantine and dispatch/cancellation receipts
// before invoking this API; this adapter does not establish those proofs.
func (store *KernelStore) EraseActorData(ctx context.Context, tenantID, actorID string) error {
	if store == nil || store.db == nil || !validScope(tenantID) || actorID == "" || strings.TrimSpace(actorID) != actorID {
		return kernel.ErrInvalidInput
	}
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Name() == "postgres" {
			if err := tx.Exec("LOCK TABLE agent_kernel_runs, agent_kernel_events, agent_kernel_transition_outbox, agent_run_relations IN SHARE ROW EXCLUSIVE MODE").Error; err != nil {
				return err
			}
		}
		ids := func() *gorm.DB {
			return tx.Model(&models.KernelRunRecord{}).Select("run_id").Where("tenant_id = ? AND actor_id = ?", tenantID, actorID)
		}
		var count int64
		if err := tx.Model(&models.KernelRunRecord{}).Where("tenant_id = ? AND actor_id = ? AND status NOT IN ?", tenantID, actorID, []string{"completed", "failed", "cancelled"}).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return ErrActorActive
		}
		if err := tx.Model(&models.KernelTransitionOutboxRecord{}).Where("run_id IN (?)", ids()).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return ErrActorActive
		}
		// A foreign/missing endpoint cannot be erased as though owned by this actor.
		if err := tx.Model(&models.RunRelationRecord{}).Where("(parent_run_id IN (?) AND child_run_id NOT IN (?)) OR (child_run_id IN (?) AND parent_run_id NOT IN (?))", ids(), ids(), ids(), ids()).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return ErrActorActive
		}
		if err := tx.Where("run_id IN (?)", ids()).Delete(&models.KernelEventRecord{}).Error; err != nil {
			return err
		}
		if err := tx.Where("parent_run_id IN (?) OR child_run_id IN (?)", ids(), ids()).Delete(&models.RunRelationRecord{}).Error; err != nil {
			return err
		}
		return tx.Where("tenant_id = ? AND actor_id = ?", tenantID, actorID).Delete(&models.KernelRunRecord{}).Error
	})
}

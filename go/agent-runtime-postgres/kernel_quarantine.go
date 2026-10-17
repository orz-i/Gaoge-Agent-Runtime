package postgres

import (
	"context"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// QuarantinedTransition is a transport-level diagnostic for an intact,
// unacknowledged invalid Outbox record. It is NOT an Agent Run quarantine.
type QuarantinedTransition struct {
	WorkID        string
	RunID         string
	FailureCode   string
	QuarantinedAt time.Time
}

// ListQuarantinedTransitions reads bounded diagnostic facts; a host must
// investigate/repair the source, never blindly release poisoned projections.
func (store *KernelStore) ListQuarantinedTransitions(ctx context.Context, limit int) ([]QuarantinedTransition, error) {
	if store == nil || store.db == nil || ctx == nil || limit < 1 || limit > 1000 {
		return nil, kernel.ErrInvalidInput
	}
	var rows []models.KernelTransitionOutboxRecord
	err := store.db.WithContext(ctx).
		Where("quarantine_code <> ?", "").
		Order("quarantined_at ASC, id ASC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, translateKernelError(err)
	}
	result := make([]QuarantinedTransition, 0, len(rows))
	for _, record := range rows {
		if record.QuarantinedAt == nil || strings.TrimSpace(record.QuarantineCode) == "" {
			return nil, kernel.ErrInvalidInput
		}
		result = append(result, QuarantinedTransition{
			WorkID: record.ID, RunID: record.RunID, FailureCode: record.QuarantineCode,
			QuarantinedAt: record.QuarantinedAt.UTC(),
		})
	}
	return result, nil
}

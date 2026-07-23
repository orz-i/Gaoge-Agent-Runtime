package postgres

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const hostProjectionRepairBatchSize = 100

var ErrNilHostProjectionRepairer = errors.New("agent runtime host projection repairer is nil")

// MarkHostProjectionRepaired records a successful host metadata projection.
func (r *Repository) MarkHostProjectionRepaired(ctx context.Context, runID string) error {
	result := r.dbFor(ctx).Model(&models.RunRecord{}).
		Where("run_id = ?", runID).
		Update("host_projection_version", currentHostProjectionVersion)
	if result.Error != nil {
		return translateError(result.Error)
	}
	if result.RowsAffected != 1 {
		return agentruntime.ErrNotFound
	}
	return nil
}

// RepairPendingHostProjections replays terminal Runtime metadata through the
// neutral host port. A missing or stale host projection is reported and left
// pending without preventing the application from starting.
func (r *Repository) RepairPendingHostProjections(
	ctx context.Context,
	repairer agentruntime.TurnProjectionRepairer,
	warn func(string, error),
) error {
	if repairer == nil {
		return ErrNilHostProjectionRepairer
	}
	var afterID uint
	for {
		rows, err := r.pendingHostProjectionRows(ctx, afterID)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		afterID, err = r.repairHostProjectionBatch(ctx, repairer, warn, rows)
		if err != nil {
			return err
		}
	}
}

func (r *Repository) repairHostProjectionBatch(
	ctx context.Context,
	repairer agentruntime.TurnProjectionRepairer,
	warn func(string, error),
	rows []models.RunRecord,
) (uint, error) {
	var afterID uint
	for _, row := range rows {
		afterID = row.ID
		if _, err := repairer.RepairTurn(ctx, hostProjectionRepairRequest(row)); err != nil {
			if warn != nil {
				warn(row.RunID, err)
			}
			continue
		}
		if err := r.MarkHostProjectionRepaired(ctx, row.RunID); err != nil {
			return afterID, err
		}
	}
	return afterID, nil
}

func (r *Repository) pendingHostProjectionRows(ctx context.Context, afterID uint) ([]models.RunRecord, error) {
	var rows []models.RunRecord
	err := r.dbFor(ctx).
		Where("id > ? AND host_projection_version < ? AND status IN ?", afterID, currentHostProjectionVersion,
			[]string{domain.RunStatusCompleted, domain.RunStatusFailed, domain.RunStatusCancelled}).
		Order("id").Limit(hostProjectionRepairBatchSize).Find(&rows).Error
	return rows, translateError(err)
}

func hostProjectionRepairRequest(row models.RunRecord) agentruntime.RepairTurnRequest {
	return agentruntime.RepairTurnRequest{
		Actor:      domain.ActorRef{TenantID: row.TenantID, ActorID: row.ActorID},
		Thread:     domain.ThreadRef{Kind: row.ThreadKind, ID: row.ThreadID},
		RunID:      row.RunID,
		Projection: agentruntime.TurnProjection{Input: domain.ProjectionRef{Kind: row.InputProjectionKind, ID: row.InputProjectionID}, Output: domain.ProjectionRef{Kind: row.OutputProjectionKind, ID: row.OutputProjectionID}},
		Outcome:    row.Status,
		Usage: agentruntime.TurnUsage{
			InputTokens: row.InputTokens, OutputTokens: row.OutputTokens,
			CacheReadTokens: row.CacheReadTokens, CacheWriteTokens: row.CacheWriteTokens,
			ReasoningTokens: row.ReasoningTokens, LatencyMS: row.TotalLatencyMS,
			BilledCurrency: row.BilledCurrency, BilledNanousd: row.BilledNanousd,
			PricingSnapshot: row.LastBillingSnapshotJSON,
		},
		ErrorCode: row.ErrorCode, ErrorMessage: row.ErrorMessage,
	}
}

var _ agentruntime.HostProjectionTracker = (*Repository)(nil)

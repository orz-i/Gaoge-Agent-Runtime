package postgres

import (
	"context"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"

	"gorm.io/gorm"
)

type outputPresentationRow struct {
	OutputRef                                      models.RuntimeOutputRefRecord `gorm:"embedded"`
	SourceRunGoal, SourceRunStatus, SourceRunModel string
	ThreadKind, ThreadID                           string
}

func outputPresentationQuery(db *gorm.DB, actor domain.ActorRef, outputID string) *gorm.DB {
	query := db.Table("agent_output_refs").
		Select("agent_output_refs.*, agent_runs.goal AS source_run_goal, agent_runs.status AS source_run_status, agent_runs.platform_model_name AS source_run_model, agent_runs.thread_kind, agent_runs.thread_id").
		Joins("JOIN agent_output_identities ON agent_output_identities.id = agent_output_refs.identity_id").
		Joins("JOIN agent_runs ON agent_runs.run_id = agent_output_refs.run_id").
		Where("agent_output_identities.tenant_id = ? AND agent_output_identities.actor_id = ?", actor.TenantID, actor.ActorID)
	if outputID != "" {
		query = query.Where("agent_output_refs.output_id = ?", outputID)
	}
	return query
}

func (r *Repository) GetOutputVersion(ctx context.Context, actor domain.ActorRef, outputID string, version int) (*domain.OutputListItem, error) {
	var row outputPresentationRow
	query := outputPresentationQuery(r.dbFor(ctx), actor, outputID)
	if version > 0 {
		query = query.Where("agent_output_refs.version = ?", version)
	} else {
		query = query.Order("agent_output_refs.version DESC")
	}
	if err := query.Take(&row).Error; err != nil {
		return nil, translateError(err)
	}
	item := outputListItem(row)
	return &item, nil
}

func (r *Repository) ListOutputVersions(ctx context.Context, actor domain.ActorRef, outputID string, beforeVersion, limit int) ([]domain.OutputListItem, bool, error) {
	limit = boundedOutputLimit(limit)
	var rows []outputPresentationRow
	query := outputPresentationQuery(r.dbFor(ctx), actor, outputID)
	if beforeVersion > 0 {
		query = query.Where("agent_output_refs.version < ?", beforeVersion)
	}
	if err := query.Order("agent_output_refs.version DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, false, translateError(err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	items := make([]domain.OutputListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, outputListItem(row))
	}
	return items, hasMore, nil
}

func boundedOutputLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func outputListItem(row outputPresentationRow) domain.OutputListItem {
	return domain.OutputListItem{OutputRef: toOutputDomain(row.OutputRef), SourceRunGoal: row.SourceRunGoal, SourceRunStatus: row.SourceRunStatus, SourceRunModel: row.SourceRunModel, Thread: domain.ThreadRef{Kind: row.ThreadKind, ID: row.ThreadID}}
}

func (r *Repository) CreateEvidence(ctx context.Context, item *domain.Evidence) error {
	row := models.EvidenceSelection{EvidenceID: item.EvidenceID, SourceKind: item.SourceKind, SourceID: item.SourceID, TenantID: item.Actor.TenantID, ActorID: item.Actor.ActorID, ProjectionKind: item.Projection.Kind, ProjectionID: item.Projection.ID, Kind: item.Kind, SelectorJSON: item.SelectorJSON, Title: item.Title, Excerpt: item.Excerpt, ContentHash: item.ContentHash, SourceContentHash: item.SourceContentHash}
	if err := r.dbFor(ctx).Create(&row).Error; err != nil {
		return translateError(err)
	}
	item.CreatedAt, item.UpdatedAt = row.CreatedAt, row.UpdatedAt
	return nil
}

func (r *Repository) GetEvidenceByIDs(ctx context.Context, actor domain.ActorRef, evidenceIDs []string) ([]domain.Evidence, error) {
	if len(evidenceIDs) == 0 {
		return []domain.Evidence{}, nil
	}
	var rows []models.EvidenceSelection
	err := r.dbFor(ctx).Where("tenant_id = ? AND actor_id = ? AND evidence_id IN ?", actor.TenantID, actor.ActorID, evidenceIDs).Find(&rows).Error
	if err != nil {
		return nil, translateError(err)
	}
	items := make([]domain.Evidence, 0, len(rows))
	for _, row := range rows {
		items = append(items, evidenceDomain(row))
	}
	return items, nil
}

func evidenceDomain(row models.EvidenceSelection) domain.Evidence {
	return domain.Evidence{EvidenceID: row.EvidenceID, SourceKind: row.SourceKind, SourceID: row.SourceID, Actor: domain.ActorRef{TenantID: row.TenantID, ActorID: row.ActorID}, Projection: domain.ProjectionRef{Kind: row.ProjectionKind, ID: row.ProjectionID}, Kind: row.Kind, SelectorJSON: row.SelectorJSON, Title: row.Title, Excerpt: row.Excerpt, ContentHash: row.ContentHash, SourceContentHash: row.SourceContentHash, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

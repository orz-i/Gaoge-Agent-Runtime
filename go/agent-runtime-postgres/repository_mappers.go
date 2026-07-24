package postgres

import (
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func toRunModel(item *domain.Run) models.RunRecord {
	if item == nil {
		return models.RunRecord{}
	}
	return models.RunRecord{RunID: item.RunID, RequestID: item.RequestID, TenantID: item.Actor.TenantID, ActorID: item.Actor.ActorID, ThreadKind: item.Thread.Kind, ThreadID: item.Thread.ID, InputProjectionKind: item.InputProjection.Kind, InputProjectionID: item.InputProjection.ID, OutputProjectionKind: item.OutputProjection.Kind, OutputProjectionID: item.OutputProjection.ID, EnvironmentKind: item.Environment.Kind, EnvironmentID: item.Environment.ID, EnvironmentRevision: item.Environment.Revision, AgentManifestID: item.AgentManifest.ID, AgentManifestRevision: item.AgentManifest.Revision, AgentName: item.AgentName, RootRunID: item.RootRunID, ParentRunID: item.ParentRunID, HandoffID: item.HandoffID, Depth: item.Depth, Goal: item.Goal, RunConfigSnapshotJSON: item.RunConfigSnapshotJSON, RequestFingerprint: item.RequestFingerprint, CurrentStepID: item.CurrentStepID, CurrentPlanID: item.CurrentPlanID, PendingInteractionID: item.PendingInteractionID, StatusReason: item.StatusReason, LastEventSeq: item.LastEventSeq, LastPresentationEventSeq: item.LastPresentationEventSeq, StateProjectionVersion: currentStateProjectionVersion, StartedBy: item.StartedBy, Endpoint: item.Endpoint, Provider: item.Provider, ProviderProtocol: item.ProviderProtocol, RequestedModelName: item.RequestedModelName, PlatformModelName: item.PlatformModelName, RoutedBindingCode: item.RoutedBindingCode, ModelVendor: item.ModelVendor, ModelIcon: item.ModelIcon, UpstreamModelName: item.UpstreamModelName, InputTokens: item.InputTokens, OutputTokens: item.OutputTokens, CacheReadTokens: item.CacheReadTokens, CacheWriteTokens: item.CacheWriteTokens, ReasoningTokens: item.ReasoningTokens, LLMCallsCount: item.LLMCallsCount, ToolCallsCount: item.ToolCallsCount, BilledCurrency: item.BilledCurrency, BilledNanousd: item.BilledNanousd, LastBillingSnapshotJSON: item.LastBillingSnapshotJSON, FirstTokenLatencyMS: item.FirstTokenLatencyMS, TotalLatencyMS: item.TotalLatencyMS, Status: item.Status, ErrorCode: item.ErrorCode, ErrorMessage: item.ErrorMessage, StartedAt: item.StartedAt, EndedAt: item.EndedAt}
}

func applyRunModel(item *domain.Run, row models.RunRecord) { *item = toRunDomain(row) }

func toRunDomain(row models.RunRecord) domain.Run {
	return domain.Run{RunID: row.RunID, RequestID: row.RequestID, Actor: domain.ActorRef{TenantID: row.TenantID, ActorID: row.ActorID}, Thread: domain.ThreadRef{Kind: row.ThreadKind, ID: row.ThreadID}, InputProjection: domain.ProjectionRef{Kind: row.InputProjectionKind, ID: row.InputProjectionID}, OutputProjection: domain.ProjectionRef{Kind: row.OutputProjectionKind, ID: row.OutputProjectionID}, Environment: domain.ResourceRef{Kind: row.EnvironmentKind, ID: row.EnvironmentID, Revision: row.EnvironmentRevision}, AgentManifest: domain.ResourceRef{Kind: domain.AgentManifestKind, ID: row.AgentManifestID, Revision: row.AgentManifestRevision}, AgentName: row.AgentName, RootRunID: row.RootRunID, ParentRunID: row.ParentRunID, HandoffID: row.HandoffID, Depth: row.Depth, Goal: row.Goal, RunConfigSnapshotJSON: row.RunConfigSnapshotJSON, RequestFingerprint: row.RequestFingerprint, CurrentStepID: row.CurrentStepID, CurrentPlanID: row.CurrentPlanID, PendingInteractionID: row.PendingInteractionID, StatusReason: row.StatusReason, LastEventSeq: row.LastEventSeq, LastPresentationEventSeq: row.LastPresentationEventSeq, StartedBy: row.StartedBy, Endpoint: row.Endpoint, Provider: row.Provider, ProviderProtocol: row.ProviderProtocol, RequestedModelName: row.RequestedModelName, PlatformModelName: row.PlatformModelName, RoutedBindingCode: row.RoutedBindingCode, ModelVendor: row.ModelVendor, ModelIcon: row.ModelIcon, UpstreamModelName: row.UpstreamModelName, InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, CacheReadTokens: row.CacheReadTokens, CacheWriteTokens: row.CacheWriteTokens, ReasoningTokens: row.ReasoningTokens, LLMCallsCount: row.LLMCallsCount, ToolCallsCount: row.ToolCallsCount, BilledCurrency: row.BilledCurrency, BilledNanousd: row.BilledNanousd, LastBillingSnapshotJSON: row.LastBillingSnapshotJSON, FirstTokenLatencyMS: row.FirstTokenLatencyMS, TotalLatencyMS: row.TotalLatencyMS, Status: row.Status, ErrorCode: row.ErrorCode, ErrorMessage: row.ErrorMessage, StartedAt: row.StartedAt, EndedAt: row.EndedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func toRunDomains(rows []models.RunRecord) []domain.Run {
	items := make([]domain.Run, 0, len(rows))
	for _, row := range rows {
		items = append(items, toRunDomain(row))
	}
	return items
}

func toStepModel(item *domain.Step) models.RunStep {
	if item == nil {
		return models.RunStep{}
	}
	var startedAt = &item.StartedAt
	if item.StartedAt.IsZero() {
		startedAt = nil
	}
	return models.RunStep{StepID: item.StepID, RunID: item.RunID, ParentStepID: item.ParentStepID, PlanID: item.PlanID, StepIndex: item.StepIndex, Attempt: item.Attempt, Kind: item.Kind, Title: item.Title, Description: item.Description, Status: item.Status, DependsOnJSON: item.DependsOnJSON, ExpectedToolsJSON: item.ExpectedToolsJSON, ResourceRefsJSON: item.ResourceRefsJSON, ApprovalRequired: item.ApprovalRequired, ResultSummary: item.ResultSummary, InputJSON: item.InputJSON, OutputJSON: item.OutputJSON, ErrorJSON: item.ErrorJSON, StartedAt: startedAt, EndedAt: item.EndedAt}
}

func applyStepModel(item *domain.Step, row models.RunStep) { *item = toStepDomain(row) }
func toStepDomain(row models.RunStep) domain.Step {
	var startedAt = domain.Step{}.StartedAt
	if row.StartedAt != nil {
		startedAt = *row.StartedAt
	}
	return domain.Step{StepID: row.StepID, RunID: row.RunID, ParentStepID: row.ParentStepID, PlanID: row.PlanID, StepIndex: row.StepIndex, Attempt: row.Attempt, Kind: row.Kind, Title: row.Title, Description: row.Description, Status: row.Status, DependsOnJSON: row.DependsOnJSON, ExpectedToolsJSON: row.ExpectedToolsJSON, ResourceRefsJSON: row.ResourceRefsJSON, ApprovalRequired: row.ApprovalRequired, ResultSummary: row.ResultSummary, InputJSON: row.InputJSON, OutputJSON: row.OutputJSON, ErrorJSON: row.ErrorJSON, StartedAt: startedAt, EndedAt: row.EndedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}
func toStepDomains(rows []models.RunStep) []domain.Step {
	items := make([]domain.Step, 0, len(rows))
	for _, row := range rows {
		items = append(items, toStepDomain(row))
	}
	return items
}

func toEventModel(item domain.Event) models.EventRecord {
	return models.EventRecord{RunID: item.RunID, TenantID: item.Actor.TenantID, ActorID: item.Actor.ActorID, ThreadKind: item.Thread.Kind, ThreadID: item.Thread.ID, ProjectionKind: item.Projection.Kind, ProjectionID: item.Projection.ID, EventScope: runEventScope, EventID: item.EventID, EventType: item.EventType, SchemaVersion: item.SchemaVersion, StepID: item.StepID, Visibility: item.Visibility, Phase: item.Phase, Stage: item.Stage, RoundID: item.RoundID, ParentEventID: item.ParentEventID, Status: item.Status, Title: item.Title, Summary: item.Summary, ContentMarkdown: item.ContentMarkdown, PayloadJSON: item.PayloadJSON, Seq: item.Seq, ToolCallID: item.ToolCallID, ToolName: item.ToolName, LatencyMS: item.LatencyMS, InputJSON: item.InputJSON, OutputJSON: item.OutputJSON, ErrorJSON: item.ErrorJSON, StartedAt: item.StartedAt, EndedAt: item.EndedAt}
}
func toEventDomain(row models.EventRecord) domain.Event {
	return domain.Event{EventID: row.EventID, RunID: row.RunID, Actor: domain.ActorRef{TenantID: row.TenantID, ActorID: row.ActorID}, Thread: domain.ThreadRef{Kind: row.ThreadKind, ID: row.ThreadID}, Projection: domain.ProjectionRef{Kind: row.ProjectionKind, ID: row.ProjectionID}, EventType: row.EventType, StepID: row.StepID, Visibility: row.Visibility, SchemaVersion: row.SchemaVersion, Phase: row.Phase, Stage: row.Stage, RoundID: row.RoundID, ParentEventID: row.ParentEventID, Status: row.Status, Title: row.Title, Summary: row.Summary, ContentMarkdown: row.ContentMarkdown, PayloadJSON: row.PayloadJSON, Seq: row.Seq, ToolCallID: row.ToolCallID, ToolName: row.ToolName, LatencyMS: row.LatencyMS, InputJSON: row.InputJSON, OutputJSON: row.OutputJSON, ErrorJSON: row.ErrorJSON, StartedAt: row.StartedAt, EndedAt: row.EndedAt, CreatedAt: row.CreatedAt}
}
func toEventDomains(rows []models.EventRecord) []domain.Event {
	items := make([]domain.Event, 0, len(rows))
	for _, row := range rows {
		items = append(items, toEventDomain(row))
	}
	return items
}

func toContextSnapshotModel(item *domain.ContextSnapshot) models.ContextRecord {
	return models.ContextRecord{RecordType: "snapshot", SnapshotID: item.SnapshotID, RunID: item.RunID, TenantID: item.Actor.TenantID, ActorID: item.Actor.ActorID, ThreadKind: item.Thread.Kind, ThreadID: item.Thread.ID, InputProjectionKind: item.InputProjection.Kind, InputProjectionID: item.InputProjection.ID, SchemaVersion: item.SchemaVersion, ThreadPathHash: item.ThreadPathHash, ContentJSON: item.ContentJSON, ContentHash: item.ContentHash, TokenEstimate: item.TokenEstimate, FileCount: item.FileCount, RAGCount: item.RAGCount, SkillCount: item.SkillCount, MemoryCount: item.MemoryCount, OutputCount: item.OutputCount, EvidenceCount: item.EvidenceCount, RetrievalFallbackCount: item.RetrievalFallbackCount, SkippedCount: item.SkippedCount}
}
func toContextSnapshotDomain(row models.ContextRecord) domain.ContextSnapshot {
	return domain.ContextSnapshot{SnapshotID: row.SnapshotID, RunID: row.RunID, ThreadPathHash: row.ThreadPathHash, ContentJSON: row.ContentJSON, ContentHash: row.ContentHash, SchemaVersion: row.SchemaVersion, Actor: domain.ActorRef{TenantID: row.TenantID, ActorID: row.ActorID}, Thread: domain.ThreadRef{Kind: row.ThreadKind, ID: row.ThreadID}, InputProjection: domain.ProjectionRef{Kind: row.InputProjectionKind, ID: row.InputProjectionID}, TokenEstimate: row.TokenEstimate, FileCount: row.FileCount, RAGCount: row.RAGCount, SkillCount: row.SkillCount, MemoryCount: row.MemoryCount, OutputCount: row.OutputCount, EvidenceCount: row.EvidenceCount, RetrievalFallbackCount: row.RetrievalFallbackCount, SkippedCount: row.SkippedCount, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}
func toContextArtifactModel(item domain.ContextArtifact) models.ContextRecord {
	return models.ContextRecord{RecordType: "artifact", ArtifactID: item.ArtifactID, SnapshotID: item.SnapshotID, RunID: item.RunID, ResourceKind: item.Resource.Kind, ResourceID: item.Resource.ID, ResourceRevision: item.Resource.Revision, SourceType: item.SourceType, SourceID: item.SourceID, SourceTitle: item.SourceTitle, Content: item.Content, ContentJSON: item.ContentJSON, ContentHash: item.ContentHash, MetadataJSON: item.MetadataJSON, TokenEstimate: item.TokenEstimate, Score: item.Score, ExpiresAt: item.ExpiresAt}
}
func toContextArtifactDomain(row models.ContextRecord) domain.ContextArtifact {
	return domain.ContextArtifact{ArtifactID: row.ArtifactID, SnapshotID: row.SnapshotID, RunID: row.RunID, Kind: domain.ContextArtifactKind(row.ResourceKind), Resource: domain.ResourceRef{Kind: row.ResourceKind, ID: row.ResourceID, Revision: row.ResourceRevision}, SourceType: row.SourceType, SourceID: row.SourceID, SourceTitle: row.SourceTitle, Content: row.Content, ContentJSON: row.ContentJSON, ContentHash: row.ContentHash, MetadataJSON: row.MetadataJSON, TokenEstimate: row.TokenEstimate, Score: row.Score, ExpiresAt: row.ExpiresAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func toCheckpointModel(item *domain.Checkpoint) models.RunCheckpoint {
	if item == nil {
		return models.RunCheckpoint{}
	}
	return models.RunCheckpoint{CheckpointID: item.CheckpointID, RunID: item.RunID, ParentCheckpointID: item.ParentCheckpointID, EventSeq: item.EventSeq, StepID: item.StepID, ToolCallID: item.ToolCallID, ContextSnapshotID: item.ContextSnapshotID, MessagePathHash: item.ContextHash, ManifestHash: item.ManifestHash, Kind: item.Kind, Status: item.Status, ResumeStateJSON: item.ResumeStateJSON, ResumeRequestID: item.ResumeRequestID, ResumeFingerprint: item.ResumeFingerprint}
}
func applyCheckpointModel(item *domain.Checkpoint, row models.RunCheckpoint) {
	*item = toCheckpointDomain(row)
}
func toCheckpointDomain(row models.RunCheckpoint) domain.Checkpoint {
	return domain.Checkpoint{CheckpointID: row.CheckpointID, RunID: row.RunID, ParentCheckpointID: row.ParentCheckpointID, EventSeq: row.EventSeq, StepID: row.StepID, ToolCallID: row.ToolCallID, ContextSnapshotID: row.ContextSnapshotID, ContextHash: row.MessagePathHash, ManifestHash: row.ManifestHash, Kind: row.Kind, Status: row.Status, ResumeStateJSON: row.ResumeStateJSON, ResumeRequestID: row.ResumeRequestID, ResumeFingerprint: row.ResumeFingerprint, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func toPlanModel(item *domain.Plan) models.RuntimePlanRecord {
	if item == nil {
		return models.RuntimePlanRecord{}
	}
	return models.RuntimePlanRecord{PlanID: item.PlanID, RunID: item.RunID, Revision: item.Revision, Status: item.Status, Goal: item.Goal, Summary: item.Summary, PayloadJSON: item.PayloadJSON, ApprovedAt: item.ApprovedAt}
}
func applyPlanModel(item *domain.Plan, row models.RuntimePlanRecord) { *item = toPlanDomain(row) }
func toPlanDomain(row models.RuntimePlanRecord) domain.Plan {
	return domain.Plan{PlanID: row.PlanID, RunID: row.RunID, Revision: row.Revision, Status: row.Status, Goal: row.Goal, Summary: row.Summary, PayloadJSON: row.PayloadJSON, CreatedAt: row.CreatedAt, ApprovedAt: row.ApprovedAt, UpdatedAt: row.UpdatedAt}
}
func toPlanDomains(rows []models.RuntimePlanRecord) []domain.Plan {
	items := make([]domain.Plan, 0, len(rows))
	for _, row := range rows {
		items = append(items, toPlanDomain(row))
	}
	return items
}

func toInteractionModel(item *domain.Interaction) models.RunInteraction {
	if item == nil {
		return models.RunInteraction{}
	}
	return models.RunInteraction{InteractionID: item.InteractionID, RunID: item.RunID, StepID: item.StepID, ToolCallID: item.ToolCallID, Type: item.Type, Status: item.Status, RequestPayloadJSON: item.RequestPayloadJSON, ResponseSchemaJSON: item.ResponseSchemaJSON, ResponseJSON: item.ResponseJSON, ResolveRequestID: item.ResolveRequestID, ResumeFingerprint: item.ResumeFingerprint, RequestedAt: item.RequestedAt, ExpiresAt: item.ExpiresAt, ResolvedAt: item.ResolvedAt, ResolvedByTenantID: item.ResolvedBy.TenantID, ResolvedByActorID: item.ResolvedBy.ActorID}
}
func applyInteractionModel(item *domain.Interaction, row models.RunInteraction) {
	*item = toInteractionDomain(row)
}
func toInteractionDomain(row models.RunInteraction) domain.Interaction {
	return domain.Interaction{InteractionID: row.InteractionID, RunID: row.RunID, StepID: row.StepID, ToolCallID: row.ToolCallID, Type: row.Type, Status: row.Status, RequestPayloadJSON: row.RequestPayloadJSON, ResponseSchemaJSON: row.ResponseSchemaJSON, ResponseJSON: row.ResponseJSON, ResolveRequestID: row.ResolveRequestID, ResumeFingerprint: row.ResumeFingerprint, RequestedAt: row.RequestedAt, ExpiresAt: row.ExpiresAt, ResolvedAt: row.ResolvedAt, ResolvedBy: domain.ActorRef{TenantID: row.ResolvedByTenantID, ActorID: row.ResolvedByActorID}, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}
func toInteractionDomains(rows []models.RunInteraction) []domain.Interaction {
	items := make([]domain.Interaction, 0, len(rows))
	for _, row := range rows {
		items = append(items, toInteractionDomain(row))
	}
	return items
}

func toOutputModel(item *domain.OutputRef) models.RuntimeOutputRefRecord {
	if item == nil {
		return models.RuntimeOutputRefRecord{}
	}
	return models.RuntimeOutputRefRecord{OutputID: item.OutputID, RunID: item.RunID, StepID: item.StepID, ToolCallID: item.ToolCallID, SourceToolCallID: item.SourceToolCallID, SourceEventID: item.SourceEventID, Kind: item.Kind, Title: item.Title, Summary: item.Summary, FileID: item.FileID, ProjectionKind: item.Projection.Kind, ProjectionID: item.Projection.ID, PreviewJSON: item.PreviewJSON, Version: item.Version, Status: item.Status, SourceSnapshotID: item.SourceSnapshotID, FileSHA256: item.FileSHA256, FileMIMEType: item.FileMIMEType}
}
func toOutputDomain(row models.RuntimeOutputRefRecord) domain.OutputRef {
	return domain.OutputRef{OutputID: row.OutputID, RunID: row.RunID, StepID: row.StepID, ToolCallID: row.ToolCallID, SourceToolCallID: row.SourceToolCallID, SourceEventID: row.SourceEventID, Kind: row.Kind, Title: row.Title, Summary: row.Summary, FileID: row.FileID, Projection: domain.ProjectionRef{Kind: row.ProjectionKind, ID: row.ProjectionID}, PreviewJSON: row.PreviewJSON, Version: row.Version, Status: row.Status, SourceSnapshotID: row.SourceSnapshotID, FileSHA256: row.FileSHA256, FileMIMEType: row.FileMIMEType, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}
func toOutputDomains(rows []models.RuntimeOutputRefRecord) []domain.OutputRef {
	items := make([]domain.OutputRef, 0, len(rows))
	for _, row := range rows {
		items = append(items, toOutputDomain(row))
	}
	return items
}

func toWorkbenchDomain(row models.RuntimeWorkbenchProjectionRecord) domain.WorkbenchProjection {
	return domain.WorkbenchProjection{RunID: row.RunID, ProjectionVersion: row.ProjectionVersion, SourcePresentationEventSeq: row.SourcePresentationEventSeq, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}
func toPhaseModel(item domain.PhaseProjection) models.RuntimePhaseProjectionRecord {
	return models.RuntimePhaseProjectionRecord{PhaseID: item.PhaseID, RunID: item.RunID, Kind: item.Kind, Title: item.Title, Summary: item.Summary, Status: item.Status, StartSeq: item.StartSeq, EndSeq: item.EndSeq, StepIDsJSON: item.StepIDsJSON, ToolCallIDsJSON: item.ToolCallIDsJSON, OutputIDsJSON: item.OutputIDsJSON, StartedAt: item.StartedAt, EndedAt: item.EndedAt}
}
func toPhaseDomain(row models.RuntimePhaseProjectionRecord) domain.PhaseProjection {
	return domain.PhaseProjection{PhaseID: row.PhaseID, RunID: row.RunID, Kind: row.Kind, Title: row.Title, Summary: row.Summary, Status: row.Status, StartSeq: row.StartSeq, EndSeq: row.EndSeq, StepIDsJSON: row.StepIDsJSON, ToolCallIDsJSON: row.ToolCallIDsJSON, OutputIDsJSON: row.OutputIDsJSON, StartedAt: row.StartedAt, EndedAt: row.EndedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}
func toPhaseDomains(rows []models.RuntimePhaseProjectionRecord) []domain.PhaseProjection {
	items := make([]domain.PhaseProjection, 0, len(rows))
	for _, row := range rows {
		items = append(items, toPhaseDomain(row))
	}
	return items
}

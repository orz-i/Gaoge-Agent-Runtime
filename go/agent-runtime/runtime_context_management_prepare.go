package agentruntime

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

type preparedContextCompaction struct {
	covered         model.ProjectionRef
	coveredPathHash string
	summary         contextSummaryResult
	artifact        model.ContextArtifact
	messages        []textRunContextMessageSnapshot
}

func (s *Engine) loadBaselineContextState(ctx context.Context, run model.Run) (*model.ContextSnapshot, textRunContextSnapshotPayload, error) {
	baseline, err := s.repo.GetRunContextSnapshot(ctx, run.Actor, run.RunID)
	if err != nil {
		return nil, textRunContextSnapshotPayload{}, err
	}
	if baseline.Revision > 1 && baseline.ManagementStatus == model.ContextManagementStatusManaged {
		return nil, textRunContextSnapshotPayload{}, errContextAlreadyManaged
	}
	var payload textRunContextSnapshotPayload
	if json.Unmarshal([]byte(baseline.ContentJSON), &payload) != nil || payload.RunID != run.RunID {
		return nil, textRunContextSnapshotPayload{}, ErrRunSnapshotIncompatible
	}
	return baseline, payload, nil
}

func (s *Engine) loadManagedThreadPath(ctx context.Context, run model.Run, config ContextConfig, baseline textRunContextSnapshotPayload) (ThreadPath, []string, string, error) {
	path, err := s.threadContext.LoadThreadPath(ctx, LoadThreadPathRequest{
		Actor: run.Actor, Thread: run.Thread, Head: &run.InputProjection, BranchReason: valueDefault572954E1, MaxDepth: maxContextPathDepth(config.MaxTurns),
	})
	if err != nil {
		return ThreadPath{}, nil, "", err
	}
	path.Messages = filterContextManagerMessages(path.Messages)
	messagePath := textRunContextMessagePath(path.Messages)
	if len(messagePath) == 0 {
		messagePath = append([]string(nil), baseline.MessagePath...)
	}
	return path, messagePath, hashTextRunContextStrings(messagePath), nil
}

func (s *Engine) prepareContextManagementRoute(ctx context.Context, run model.Run, effective effectiveTextRunConfig) (*LLMRoute, []ToolDefinition, []HostedTool, error) {
	if s.llmGateway == nil {
		return nil, nil, nil, ErrModelRouteNotConfigured
	}
	route, err := s.llmGateway.PrepareTextRoute(ctx, LLMRouteInput{PlatformModelName: effective.PlatformModelName, TaskType: LLMTaskTypeText, Scope: LLMRouteScopeUser, Actor: run.Actor, Thread: run.Thread, RequestID: run.RequestID + ":context-manager"})
	if err != nil {
		return nil, nil, nil, err
	}
	_, tools := collectEffectiveToolPolicies(effective)
	hostedTools, err := hostedToolsForProtocol(effective, route.Protocol)
	return route, tools, hostedTools, err
}

func (s *Engine) prepareContextCompaction(ctx context.Context, run model.Run, effective effectiveTextRunConfig, config ContextConfig, state *contextManagementState, cut int) (preparedContextCompaction, error) {
	covered := state.path.Messages[cut-1].Projection
	coveredPath := state.messagePath
	if len(coveredPath) > cut {
		coveredPath = coveredPath[:cut]
	}
	coveredPathHash := hashTextRunContextStrings(coveredPath)
	previousSummary, previousCoveredIndex := s.reusableContextSummary(ctx, run, state.path.Messages[:cut])
	summarySource := contextSummaryDelta(state.path.Messages, previousCoveredIndex, cut)
	summary := s.generateContextSummary(ctx, run, effective, state.route, state.pathHash, previousSummary, summarySource, config.SummaryMaxTokens)
	if strings.TrimSpace(summary.Content) == "" {
		summary = fallbackContextSummary(previousSummary, summarySource, config.SummaryMaxTokens)
	}
	summary.Content = truncateContextSummary(summary.Content, config.SummaryMaxTokens)
	metadata := contextSummaryMetadata{CoveredThrough: covered, CoveredPathHash: coveredPathHash, Strategy: summary.Strategy, FromTurn: 1, ToTurn: countContextTurns(state.path.Messages[:cut])}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return preparedContextCompaction{}, err
	}
	sourceID := boundedContextArtifactSourceID(covered.ID)
	artifact := model.ContextArtifact{
		RunID: run.RunID, SnapshotID: contextSnapshotID(run.RunID, state.baseline.Revision+1), Kind: model.ContextArtifactSummary,
		Resource: model.ResourceRef{Kind: string(model.ContextArtifactSummary), ID: sourceID}, Projection: covered,
		SourceType: "context_manager", SourceID: sourceID, SourceTitle: "Thread summary", Content: summary.Content,
		ContentHash: contextArtifactHash(model.ContextArtifactSummary, sourceID, summary.Content), MetadataJSON: string(metadataJSON),
		TokenEstimate: estimateTokens(summary.Content), CreatedAt: s.now(), UpdatedAt: s.now(),
	}
	artifact = sealContextArtifacts(run.RunID, artifact.SnapshotID, []model.ContextArtifact{artifact})[0]
	return preparedContextCompaction{
		covered: covered, coveredPathHash: coveredPathHash, summary: summary, artifact: artifact,
		messages: managedSnapshotMessages(state.payload.Messages, config.PreserveRecentTurns, summary.Content, metadata),
	}, nil
}

func contextSummaryDelta(messages []ContextMessage, previousCoveredIndex, cut int) []ContextMessage {
	if previousCoveredIndex >= 0 && previousCoveredIndex+1 < cut {
		return messages[previousCoveredIndex+1 : cut]
	}
	if previousCoveredIndex == cut-1 {
		return nil
	}
	return messages[:cut]
}

func compactedContextTrace(state *contextManagementState, prepared preparedContextCompaction, assessment ContextBudgetAssessment, cut int, startedAt time.Time) ContextManagementTrace {
	trace := state.trace
	trace.SnapshotRevision = state.baseline.Revision + 1
	trace.RawTokenEstimate, trace.AdjustedTokenEstimate, trace.TokenCountSource = assessment.RawTokenEstimate, assessment.AdjustedTokenEstimate, assessment.TokenCountSource
	trace.RetainedMessageCount, trace.SummarizedMessageCount = len(state.path.Messages)-cut, cut
	trace.TrimmedMessageCount = len(state.fullMessages) - len(prepared.messages)
	if trace.TrimmedMessageCount < 0 {
		trace.TrimmedMessageCount = 0
	}
	trace.SummaryArtifactID, trace.SummaryStrategy, trace.CoveredThrough = prepared.artifact.ArtifactID, prepared.summary.Strategy, prepared.covered
	trace.CoveredPathHash, trace.SummaryTokenEstimate, trace.Fallback = prepared.coveredPathHash, prepared.artifact.TokenEstimate, prepared.summary.Fallback
	if trace.LoadedMessageCount > 0 {
		trace.CompressionRatio = float64(trace.SummarizedMessageCount) / float64(trace.LoadedMessageCount)
	}
	trace.DurationMS = time.Since(startedAt).Milliseconds()
	return trace
}

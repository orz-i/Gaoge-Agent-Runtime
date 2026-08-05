package agentruntime

import (
	"encoding/json"
	"strings"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func textRunContextSummaryFromSnapshot(snapshot *model.ContextSnapshot, managementMode string) *TextRunContextSummary {
	if snapshot == nil {
		return nil
	}
	revision := snapshot.Revision
	if revision <= 0 {
		revision = 1
	}
	result := &TextRunContextSummary{
		SnapshotID: snapshot.SnapshotID, Revision: revision, SupersedesSnapshotID: snapshot.SupersedesSnapshotID,
		Mode: normalizeContextConfig(ContextConfig{ManagementMode: managementMode}).ManagementMode, ManagementStatus: snapshot.ManagementStatus,
		SemanticVersion: snapshot.SchemaVersion, ContentHash: snapshot.ContentHash,
		FileCount: snapshot.FileCount, RAGCount: snapshot.RAGCount, SkillCount: snapshot.SkillCount, MemoryCount: snapshot.MemoryCount,
		OutputCount: snapshot.OutputCount, EvidenceCount: snapshot.EvidenceCount, RetrievalFallbackCount: snapshot.RetrievalFallbackCount,
		SkippedCount: snapshot.SkippedCount, CompiledAt: snapshot.CreatedAt,
	}
	if strings.TrimSpace(result.ManagementStatus) == "" {
		result.ManagementStatus = model.ContextManagementStatusBaseline
	}
	var payload textRunContextSnapshotPayload
	if json.Unmarshal([]byte(snapshot.ContentJSON), &payload) != nil || payload.Management == nil {
		return result
	}
	trace := payload.Management
	result.Mode = firstNonEmptyString(trace.Mode, result.Mode)
	result.HardInputTokens, result.SoftInputTokens = trace.HardInputTokens, trace.SoftInputTokens
	result.RawTokenEstimate, result.AdjustedTokenEstimate = trace.RawTokenEstimate, trace.AdjustedTokenEstimate
	result.TokenCountSource = string(trace.TokenCountSource)
	result.LoadedMessageCount, result.RetainedMessageCount = trace.LoadedMessageCount, trace.RetainedMessageCount
	result.SummarizedMessageCount, result.TrimmedMessageCount = trace.SummarizedMessageCount, trace.TrimmedMessageCount
	result.SummaryArtifactID, result.SummaryStrategy = trace.SummaryArtifactID, trace.SummaryStrategy
	if trace.CoveredThrough.ID != "" {
		covered := trace.CoveredThrough
		result.CoveredThrough = &covered
	}
	result.SummaryTokenEstimate = trace.SummaryTokenEstimate
	return result
}

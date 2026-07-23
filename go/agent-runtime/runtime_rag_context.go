// Package agentruntime owns Agent Runtime use cases and policy.
package agentruntime

import (
	"context"
	"fmt"
	"strings"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueMessage5959AD4D = "message"
)

type runtimeRAGContextResult struct {
	fallbacks          []ragFallbackEvidence
	retrievalFallbacks []ragFallbackEvidence
	chunks             []model.RAGChunk
}

func (s *Engine) resolveRunRAGContext(
	ctx context.Context,
	input RuntimeInput,
	cfg Config,
	fileContextPlan conversationFileContextPlan,
	ragQuery string,
	assembler *ContextAssembler,
	traceRecorder *messageTraceRecorder,
) runtimeRAGContextResult {
	result := runtimeRAGContextResult{
		fallbacks: ragFallbackEvidencesFromAttachments(
			filterAttachmentsByContextMode(fileContextPlan.FullAttachments, fileContextModeRAGFallback),
			"rag_unavailable",
			"",
		),
	}
	if !cfg.Retrieval.Enabled || len(fileContextPlan.RAGAttachments) == 0 {
		return result
	}

	readyObjs := fileContextPlanRAGObjects(fileContextPlan.RAGAttachments)
	emitEvent(input.OnEvent, "rag_search", map[string]interface{}{
		valueMessage5959AD4D: "正在检索相关内容…",
	})
	ragResult, ragErr := s.retrieveRunRAG(ctx, input, cfg, ragQuery, readyObjs)
	ragChunks := assembler.DeduplicateRAGChunks(conversationRAGChunksFromKnowledge(ragResult.Chunks))
	switch {
	case ragErr != nil:
		return s.runtimeRAGRetrievalErrorFallback(ctx, input.Actor, result, fileContextPlan, cfg, ragQuery, readyObjs, ragResult, ragErr, traceRecorder)
	case len(ragChunks) == 0:
		return s.runtimeRAGRetrievalEmptyFallback(result, fileContextPlan, cfg, ragQuery, readyObjs, ragResult, traceRecorder)
	default:
		if traceRecorder != nil {
			summary, markdown, payload := buildRAGProcessTrace(ragQuery, readyObjs, ragChunks)
			traceRecorder.appendProcessSection(summary, markdown, payload)
		}
		result.chunks = append(result.chunks, ragChunks...)
		return result
	}
}

func (s *Engine) retrieveRunRAG(
	ctx context.Context,
	input RuntimeInput,
	cfg Config,
	ragQuery string,
	readyObjs []FileAsset,
) (RetrievalResult, error) {
	ragCtx, ragSpan := s.startSpan(ctx, "agentruntime.rag.retrieve",
		String("thread.id", input.Thread.ID),
		String("actor.id", input.Actor.ActorID),
		Int("conversation.rag.file_count", len(readyObjs)),
	)
	ragCallCtx, ragCancel := runtimeRAGCallContext(ragCtx, cfg)
	ragResult, ragErr := s.ragSvc.Retrieve(ragCallCtx, input.Actor, ragQuery, readyObjs)
	ragCancel()
	recordSpanError(ragSpan, ragErr)
	ragSpan.SetAttributes(runtimeRAGTraceAttributes(ragResult)...)
	ragSpan.End()
	return ragResult, ragErr
}

func runtimeRAGCallContext(ctx context.Context, cfg Config) (context.Context, context.CancelFunc) {
	if cfg.Retrieval.WaitReadyMS <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, time.Duration(cfg.Retrieval.WaitReadyMS)*time.Millisecond)
}

func runtimeRAGTraceAttributes(result RetrievalResult) []LogField {
	return []LogField{
		String("conversation.rag.status", string(result.Status)),
		String("conversation.rag.reason", strings.TrimSpace(result.Reason)),
		Int("conversation.rag.candidate_count", result.CandidateCount),
		Int("conversation.rag.filtered_count", result.FilteredCount),
		Float64("conversation.rag.max_score", float64(result.MaxScore)),
		Bool("conversation.rag.cached", result.Cached),
	}
}

func (s *Engine) runtimeRAGRetrievalErrorFallback(
	ctx context.Context,
	actor model.ActorRef,
	result runtimeRAGContextResult,
	fileContextPlan conversationFileContextPlan,
	cfg Config,
	ragQuery string,
	readyObjs []FileAsset,
	ragResult RetrievalResult,
	ragErr error,
	traceRecorder *messageTraceRecorder,
) runtimeRAGContextResult {
	if s.logger != nil {
		s.logger.Warn("rag_retrieval_failed",
			String("trace_id", s.traceID(ctx)),
			String("actor_id", actor.ActorID),
			Error(ragErr),
		)
	}
	fallbacks, skipped := splitRetrievalFallbackAttachments(fileContextPlan.RAGAttachments, cfg)
	fallbackReason := normalizeRAGFallbackReason(ragResult.Status, "rag_error")
	runtimeRAGFallbackTrace(traceRecorder, "内容检索未完成", "检索未完成", ragQuery, readyObjs, ragResult, fallbackReason, ragErr, fallbacks)
	evidences := ragFallbackEvidencesFromAttachments(fallbacks, fallbackReason, strings.TrimSpace(ragErr.Error()))
	result.fallbacks = append(result.fallbacks, evidences...)
	result.retrievalFallbacks = append(result.retrievalFallbacks, evidences...)
	appendRAGFallbackSkippedTrace(traceRecorder, skipped, fallbackReason)
	return result
}

func (s *Engine) runtimeRAGRetrievalEmptyFallback(
	result runtimeRAGContextResult,
	fileContextPlan conversationFileContextPlan,
	cfg Config,
	ragQuery string,
	readyObjs []FileAsset,
	ragResult RetrievalResult,
	traceRecorder *messageTraceRecorder,
) runtimeRAGContextResult {
	fallbacks, skipped := splitRetrievalFallbackAttachments(fileContextPlan.RAGAttachments, cfg)
	ragStatus := normalizeRAGFallbackReason(ragResult.Status, "rag_empty")
	missLabel := "未检索到相关片段"
	if ragResult.Status == RetrievalStatusLowScore {
		missLabel = "检索结果低于相似度阈值"
	}
	runtimeRAGFallbackTrace(traceRecorder, "未检索到相关片段", missLabel, ragQuery, readyObjs, ragResult, ragStatus, nil, fallbacks)
	evidences := ragFallbackEvidencesFromAttachments(fallbacks, ragStatus, "")
	result.fallbacks = append(result.fallbacks, evidences...)
	result.retrievalFallbacks = append(result.retrievalFallbacks, evidences...)
	appendRAGFallbackSkippedTrace(traceRecorder, skipped, ragStatus)
	return result
}

func runtimeRAGFallbackTrace(
	traceRecorder *messageTraceRecorder,
	summaryPrefix string,
	detailLabel string,
	ragQuery string,
	readyObjs []FileAsset,
	ragResult RetrievalResult,
	reason string,
	err error,
	fallbacks []AttachmentInput,
) {
	if traceRecorder == nil {
		return
	}
	fallbackLabel := runtimeRAGFallbackLabel(fallbacks)
	traceRecorder.appendProcessSection(
		summaryPrefix+"，"+fallbackLabel,
		formatTraceStep("内容检索", fmt.Sprintf("文件已检索，%s，%s。", detailLabel, fallbackLabel)),
		buildRAGFallbackProcessTracePayload(ragQuery, readyObjs, ragResult, reason, len(fallbacks) > 0, err),
	)
}

func runtimeRAGFallbackLabel(fallbacks []AttachmentInput) string {
	if len(fallbacks) == 0 {
		return "没有可用全文"
	}
	return "已改用全文"
}

package agentruntime

import (
	"math"
	"strings"
)

const (
	valueFallback76D289BC  = "fallback"
	valueFileCount2ED0CEA0 = "file_count"
	valueFileNames41617573 = "file_names"
	valueKind8B4205EC      = "kind"
	valueQuery7806681D     = "query"
	valueReason7BD490A5    = "reason"
	valueStatusB31BAB10    = "status"
)

func emitEvent(onEvent func(string, map[string]interface{}) error, eventType string, payload map[string]interface{}) {
	if onEvent != nil {
		_ = onEvent(eventType, payload)
	}
}

func normalizeRAGFallbackReason(status RetrievalStatus, fallback string) string {
	value := strings.TrimSpace(string(status))
	if value == "" || value == string(RetrievalStatusHit) {
		return fallback
	}
	return value
}

func processTraceRetrievalStatus(reason string) string {
	switch strings.TrimSpace(reason) {
	case string(RetrievalStatusLowScore):
		return processTraceStatusLowScore
	case string(RetrievalStatusEmpty):
		return processTraceStatusEmpty
	default:
		return processTraceStatusIncomplete
	}
}

func processTraceFallbackMode(hasFullText bool) string {
	if hasFullText {
		return processTraceFallbackFullText
	}
	return processTraceFallbackUnavailable
}

func ragFileObjectNames(items []FileAsset) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.FileName)
		if name == "" {
			name = strings.TrimSpace(item.FileID)
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func buildRAGFallbackProcessTracePayload(
	query string,
	fileObjs []FileAsset,
	result RetrievalResult,
	reason string,
	hasFullTextFallback bool,
	err error,
) map[string]interface{} {
	stage := map[string]interface{}{
		valueKind8B4205EC:      processTraceKindRetrieval,
		valueStatusB31BAB10:    processTraceRetrievalStatus(reason),
		valueFallback76D289BC:  processTraceFallbackMode(hasFullTextFallback),
		valueFileCount2ED0CEA0: len(fileObjs),
		"candidate_count":      result.CandidateCount,
		"filtered_count":       result.FilteredCount,
		"max_score":            result.MaxScore,
	}
	if normalizedReason := strings.TrimSpace(firstNonEmptyString(reason, result.Reason)); normalizedReason != "" {
		stage[valueReason7BD490A5] = normalizedReason
	}
	payload := map[string]interface{}{
		valueQuery7806681D:       compactSnippet(query, 240),
		valueFileNames41617573:   ragFileObjectNames(fileObjs),
		valueStatusB31BAB10:      strings.TrimSpace(reason),
		valueReason7BD490A5:      strings.TrimSpace(result.Reason),
		"candidate_count":        result.CandidateCount,
		"filtered_count":         result.FilteredCount,
		"max_score":              result.MaxScore,
		processTracePayloadStage: stage,
	}
	if err != nil {
		payload["error"] = err.Error()
	}
	return payload
}

func traceUintID(value uint) int64 {
	if uint64(value) > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(value)
}

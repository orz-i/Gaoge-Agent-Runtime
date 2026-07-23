// Package agentruntime owns Agent Runtime use cases and policy.
package agentruntime

import (
	"context"
	"sort"
	"strings"
)

const (
	fileCategoryImage = "image"
	fileCategoryPDF   = "pdf"
)

func parseAllowedMIMETypes(raw string) map[string]struct{} {
	items := strings.Split(raw, ",")
	result := make(map[string]struct{}, len(items))
	for _, item := range items {
		value := strings.ToLower(strings.TrimSpace(item))
		if value == "" {
			continue
		}
		result[value] = struct{}{}
	}
	return result
}

type chatFileCapability struct {
	RAGAvailable           bool
	RAGAvailabilityReason  string
	CapabilityMode         string
	EffectiveImageMaxBytes int64
	EffectiveDocMaxBytes   int64
}

func minPositiveInt64(values ...int64) int64 {
	var result int64
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if result == 0 || value < result {
			result = value
		}
	}
	return result
}

func sortedAllowedMIMETypes(raw string) []string {
	result := make([]string, 0)
	for value := range parseAllowedMIMETypes(raw) {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (s *Engine) resolveChatFileCapability(ctx context.Context) chatFileCapability {
	cfg := s.cfg.Snapshot()
	capability := chatFileCapability{
		EffectiveImageMaxBytes: minPositiveInt64(cfg.Files.MaxUploadBytes, cfg.Files.ImageMaxBytes),
		EffectiveDocMaxBytes:   minPositiveInt64(cfg.Files.MaxUploadBytes, cfg.Files.DocumentMaxBytes),
	}

	ragAvailable, reason := s.ragSvc.FileIndexAvailable(ctx)
	capability.RAGAvailable = ragAvailable
	capability.RAGAvailabilityReason = reason
	capability.CapabilityMode = "full_context_and_rag"
	if !ragAvailable {
		capability.CapabilityMode = "full_context_only"
	}
	return capability
}

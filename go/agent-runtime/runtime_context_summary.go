package agentruntime

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const contextSummaryArtifactMarker = "artifact"

type deterministicSummaryEntry struct {
	text     string
	priority bool
}

func deterministicContextSummary(previous string, source []ContextMessage, maxTokens int) string {
	entries := deterministicContextSummaryEntries(source)
	perEntryTokens := maxTokens / 3
	if perEntryTokens < 8 {
		perEntryTokens = 8
	}
	parts := make([]string, 0, len(entries)+1)
	if strings.TrimSpace(previous) != "" {
		parts = append(parts, truncateContextSummary("Prior summary: "+strings.TrimSpace(previous), perEntryTokens))
	}
	seen := make(map[int]struct{}, len(entries))
	parts = appendDeterministicSummaryEntries(parts, entries, seen, true, perEntryTokens, maxTokens)
	parts = appendDeterministicSummaryEntries(parts, entries, seen, false, perEntryTokens, maxTokens)
	return truncateContextSummary(strings.Join(parts, "\n"), maxTokens)
}

func deterministicContextSummaryEntries(source []ContextMessage) []deterministicSummaryEntry {
	entries := make([]deterministicSummaryEntry, 0, len(source))
	for _, message := range source {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		entries = append(entries, deterministicSummaryEntry{
			text:     fmt.Sprintf("%s [%s:%s]: %s", message.Role, message.Projection.Kind, message.Projection.ID, content),
			priority: contextSummaryPriorityContent(content),
		})
	}
	return entries
}

func appendDeterministicSummaryEntries(parts []string, entries []deterministicSummaryEntry, seen map[int]struct{}, priorityOnly bool, perEntryTokens, maxTokens int) []string {
	for index := len(entries) - 1; index >= 0; index-- {
		if _, ok := seen[index]; ok || priorityOnly && !entries[index].priority {
			continue
		}
		entry := truncateContextSummary(entries[index].text, perEntryTokens)
		candidate := strings.Join(append(append([]string(nil), parts...), entry), "\n")
		if maxTokens > 0 && estimateTokens(candidate) > int64(maxTokens) {
			continue
		}
		parts, seen[index] = append(parts, entry), struct{}{}
	}
	return parts
}

func contextSummaryPriorityContent(content string) bool {
	content = strings.ToLower(content)
	for _, marker := range []string{
		contextSummaryDecisionMarker, "decided", "constraint", "must", "todo", "pending", "unresolved", "unfinished", contextSummaryArtifactMarker, valueToolCCF14517, "result", valueError9B7383E5,
		"决定", "决策", "约束", "必须", "待办", "未完成", "未解决", "制品", "工具", "结果", "失败",
	} {
		if strings.Contains(content, marker) {
			return true
		}
	}
	return false
}

func truncateContextSummary(value string, maxTokens int) string {
	value = strings.TrimSpace(value)
	if maxTokens <= 0 || estimateTokens(value) <= int64(maxTokens) {
		return value
	}
	var builder strings.Builder
	for len(value) > 0 {
		_, size := utf8.DecodeRuneInString(value)
		if size <= 0 {
			break
		}
		candidate := builder.String() + value[:size] + "…"
		if estimateTokens(candidate) > int64(maxTokens) {
			break
		}
		builder.WriteString(value[:size])
		value = value[size:]
	}
	result := strings.TrimSpace(builder.String())
	if estimateTokens(result+"…") <= int64(maxTokens) {
		result += "…"
	}
	return result
}

package context

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const summaryTurnID = "context_summary"

func compactToolResults(runID string, prompt Prompt, budget Budget) (Prompt, []Artifact, []TrimAction) {
	managed := clonePrompt(prompt)
	artifacts := make([]Artifact, 0)
	actions := make([]TrimAction, 0)
	for index := range managed.Items {
		item := &managed.Items[index]
		if item.Kind != ItemToolResult || len([]byte(item.Content)) <= budget.MaxToolResultBytes {
			continue
		}
		artifact := newToolArtifact(runID, *item)
		item.Content = compactedToolReplacement(artifact, budget.MaxToolResultBytes)
		artifacts = append(artifacts, artifact)
		actions = append(actions, TrimAction{
			Kind: "compact_tool_result", TurnID: item.TurnID, ItemCount: 1,
			ArtifactID: artifact.ID, ContentHash: artifact.ContentHash,
		})
	}
	return managed, artifacts, actions
}

func summarizeOldTurns(
	runID string,
	currentTurnID string,
	prompt Prompt,
	budget Budget,
	assessment Assessment,
) (Prompt, *Artifact, *Summary, []TrimAction) {
	if assessment.AdjustedTokenEstimate <= assessment.SoftInputTokens {
		return prompt, nil, nil, nil
	}
	eligible := summarizableTurns(prompt.Items, currentTurnID, budget.PreserveRecentTurns)
	if len(eligible) == 0 {
		return prompt, nil, nil, nil
	}
	coveredItems := itemsForTurns(prompt.Items, eligible)
	content := deterministicSummary(coveredItems, budget.MaxSummaryTokens)
	if strings.TrimSpace(content) == "" {
		return prompt, nil, nil, nil
	}
	coveredThrough := eligible[len(eligible)-1]
	artifact := newSummaryArtifact(runID, coveredThrough, content)
	summaryItem := Item{
		ID: "ctxi_" + artifact.ContentHash[:24], TurnID: summaryTurnID, Kind: ItemSummary,
		Role: RoleSystem, Content: "<sum>\n" + content + "\n</sum>",
		ProjectionID: coveredThrough, Required: true,
	}
	managed := clonePrompt(prompt)
	managed.Items = removeTurns(managed.Items, eligible)
	managed.Items = insertSummary(managed.Items, summaryItem)
	summary := &Summary{
		ArtifactID: artifact.ID, CoveredTurns: len(eligible), CoveredItems: len(coveredItems),
		CoveredThrough: coveredThrough, TokenEstimate: artifact.TokenEstimate, Strategy: "deterministic",
	}
	action := TrimAction{
		Kind: "summarize_complete_turns", TurnID: coveredThrough, ItemCount: len(coveredItems),
		ArtifactID: artifact.ID, ContentHash: artifact.ContentHash,
	}
	return managed, &artifact, summary, []TrimAction{action}
}

func summarizableTurns(items []Item, currentTurnID string, preserveRecent int) []string {
	ordered := orderedTurnIDs(items)
	protected := recentTurnSet(items, preserveRecent)
	result := make([]string, 0)
	for _, turnID := range ordered {
		if turnID == currentTurnID || turnID == summaryTurnID || turnRequired(items, turnID) {
			continue
		}
		if _, keep := protected[turnID]; keep {
			continue
		}
		result = append(result, turnID)
	}
	return result
}

func recentTurnSet(items []Item, preserveRecent int) map[string]struct{} {
	ordered := orderedTurnIDs(items)
	result := make(map[string]struct{}, preserveRecent)
	for index := len(ordered) - 1; index >= 0 && len(result) < preserveRecent; index-- {
		turnID := ordered[index]
		if turnID == summaryTurnID {
			continue
		}
		result[turnID] = struct{}{}
	}
	return result
}

func itemsForTurns(items []Item, turnIDs []string) []Item {
	selected := make(map[string]struct{}, len(turnIDs))
	for _, turnID := range turnIDs {
		selected[turnID] = struct{}{}
	}
	result := make([]Item, 0)
	for _, item := range items {
		if _, ok := selected[item.TurnID]; ok {
			result = append(result, item)
		}
	}
	return result
}

func removeTurns(items []Item, turnIDs []string) []Item {
	removed := make(map[string]struct{}, len(turnIDs))
	for _, turnID := range turnIDs {
		removed[turnID] = struct{}{}
	}
	result := make([]Item, 0, len(items))
	for _, item := range items {
		if _, drop := removed[item.TurnID]; drop {
			continue
		}
		result = append(result, item)
	}
	return result
}

func insertSummary(items []Item, summary Item) []Item {
	index := 0
	for index < len(items) && items[index].Required && items[index].Role == RoleSystem {
		index++
	}
	result := make([]Item, 0, len(items)+1)
	result = append(result, items[:index]...)
	result = append(result, summary)
	result = append(result, items[index:]...)
	return result
}

func deterministicSummary(items []Item, maxTokens int) string {
	if maxTokens <= 0 || len(items) == 0 {
		return ""
	}
	perItem := maxTokens / 3
	if perItem < 8 {
		perItem = 8
	}
	parts := make([]string, 0, len(items))
	for index := len(items) - 1; index >= 0; index-- {
		item := items[index]
		entry := fmt.Sprintf("%s [%s:%s]: %s", item.Role, item.TurnID, item.ID, item.Content)
		entry = truncateSummary(entry, perItem)
		candidate := append([]string{entry}, parts...)
		if estimatedTokens([]byte(strings.Join(candidate, "\n"))) > int64(maxTokens) {
			continue
		}
		parts = candidate
	}
	return truncateSummary(strings.Join(parts, "\n"), maxTokens)
}

func truncateSummary(value string, maxTokens int) string {
	value = strings.TrimSpace(value)
	if maxTokens <= 0 || estimatedTokens([]byte(value)) <= int64(maxTokens) {
		return value
	}
	maxBytes := maxTokens * 4
	if maxBytes <= 1 {
		return "…"
	}
	return validUTF8Prefix(value, maxBytes-1) + "…"
}

func newToolArtifact(runID string, item Item) Artifact {
	contentHash := hashBytes([]byte(item.Content))
	metadata, _ := canonicalJSON(map[string]string{
		"toolCallID": item.ToolCallID,
		"toolName":   item.ToolName,
		"turnID":     item.TurnID,
	})
	return Artifact{
		ID:   stableID("ctxa", runID, string(ArtifactToolResult), item.ToolCallID, contentHash),
		Kind: ArtifactToolResult, RunID: runID, SourceID: item.ToolCallID, SourceTitle: item.ToolName,
		Content: item.Content, ContentJSON: metadata, ContentHash: contentHash,
		TokenEstimate: estimatedTokens([]byte(item.Content)),
	}
}

func newSummaryArtifact(runID string, coveredThrough string, content string) Artifact {
	contentHash := hashBytes([]byte(content))
	metadata, _ := canonicalJSON(map[string]string{"coveredThrough": coveredThrough, "strategy": "deterministic"})
	return Artifact{
		ID:   stableID("ctxa", runID, string(ArtifactSummary), coveredThrough, contentHash),
		Kind: ArtifactSummary, RunID: runID, SourceID: coveredThrough, SourceTitle: "Thread summary",
		Content: content, ContentJSON: metadata, ContentHash: contentHash,
		TokenEstimate: estimatedTokens([]byte(content)),
	}
}

func compactedToolReplacement(artifact Artifact, maxBytes int) string {
	headTail := maxBytes / 4
	if headTail < 128 {
		headTail = 128
	}
	head := validUTF8Prefix(artifact.Content, headTail)
	tail := validUTF8Suffix(artifact.Content, headTail)
	return fmt.Sprintf(
		"[tool_result_compacted artifact_id=%s sha256=%s bytes=%d]\n<head>\n%s\n</head>\n<tail>\n%s\n</tail>",
		artifact.ID,
		artifact.ContentHash,
		len([]byte(artifact.Content)),
		head,
		tail,
	)
}

func validUTF8Prefix(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

func validUTF8Suffix(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	start := len(value) - maxBytes
	for start < len(value) && !utf8.ValidString(value[start:]) {
		start++
	}
	return value[start:]
}

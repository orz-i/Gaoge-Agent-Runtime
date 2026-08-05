package agentruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	minimumCompactedToolResultBytes = 2_048
	contextTrimCompactToolResult    = "compact_tool_result"
)

func compactOversizedToolResults(run model.Run, messages []Message, hardTokens int64, actions []PromptTrimAction) ([]Message, []model.ContextArtifact, []PromptTrimAction) {
	if len(messages) == 0 {
		return messages, nil, actions
	}
	maxBytes := contextTransportByteBudget(hardTokens) / 4
	if maxBytes < minimumCompactedToolResultBytes {
		maxBytes = minimumCompactedToolResultBytes
	}
	result := cloneLLMMessages(messages)
	var artifacts []model.ContextArtifact
	for messageIndex := range result {
		var messageArtifacts []model.ContextArtifact
		result[messageIndex].ToolResults, messageArtifacts, actions = compactToolResults(run, result[messageIndex].ToolResults, maxBytes, actions)
		artifacts = append(artifacts, messageArtifacts...)
		result[messageIndex].ToolCalls, messageArtifacts, actions = compactToolCalls(run, result[messageIndex].ToolCalls, maxBytes, actions)
		artifacts = append(artifacts, messageArtifacts...)
	}
	return result, artifacts, actions
}

func compactToolResults(run model.Run, results []ToolResult, maxBytes int64, actions []PromptTrimAction) ([]ToolResult, []model.ContextArtifact, []PromptTrimAction) {
	return compactToolOutputs(run, results, maxBytes, actions, func(result *ToolResult) (string, string, *string) {
		return result.ToolCallID, result.ToolName, &result.OutputJSON
	})
}

func compactToolCalls(run model.Run, calls []ToolCall, maxBytes int64, actions []PromptTrimAction) ([]ToolCall, []model.ContextArtifact, []PromptTrimAction) {
	return compactToolOutputs(run, calls, maxBytes, actions, func(call *ToolCall) (string, string, *string) {
		return call.ToolCallID, call.ToolName, &call.OutputJSON
	})
}

func compactToolOutputs[T any](run model.Run, values []T, maxBytes int64, actions []PromptTrimAction, output func(*T) (string, string, *string)) ([]T, []model.ContextArtifact, []PromptTrimAction) {
	result := append([]T(nil), values...)
	artifacts := make([]model.ContextArtifact, 0)
	for index := range result {
		toolCallID, toolName, content := output(&result[index])
		if int64(len(*content)) <= maxBytes {
			continue
		}
		artifact, replacement := compactToolArtifact(run, toolCallID, toolName, *content, maxBytes)
		artifacts, *content = append(artifacts, artifact), replacement
		actions = append(actions, PromptTrimAction{Action: contextTrimCompactToolResult, ArtifactID: artifact.ArtifactID, ContentHash: artifact.ContentHash})
	}
	return result, artifacts, actions
}

func compactToolArtifact(run model.Run, toolCallID, toolName, content string, maxBytes int64) (model.ContextArtifact, string) {
	digest := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(digest[:])
	artifactID := "ctxa_" + hashTextRunContextStrings([]string{run.RunID, string(model.ContextArtifactToolResult), toolCallID, hash})[:32]
	headTailBytes := int(maxBytes / 4)
	if headTailBytes < 256 {
		headTailBytes = 256
	}
	head, tail := content, ""
	if len(content) > headTailBytes*2 {
		head, tail = content[:headTailBytes], content[len(content)-headTailBytes:]
	}
	replacement := fmt.Sprintf("[tool_result_compacted artifact_id=%s sha256=%s bytes=%d]\n<head>\n%s\n</head>\n<tail>\n%s\n</tail>", artifactID, hash, len(content), head, tail)
	sourceID := boundedContextArtifactSourceID(toolCallID)
	artifact := model.ContextArtifact{
		ArtifactID: artifactID, RunID: run.RunID, Kind: model.ContextArtifactToolResult,
		Resource:   model.ResourceRef{Kind: string(model.ContextArtifactToolResult), ID: firstNonEmptyString(sourceID, hash)},
		Projection: run.OutputProjection, SourceType: "tool_result", SourceID: sourceID, SourceTitle: strings.TrimSpace(toolName),
		Content: content, ContentHash: hash, TokenEstimate: estimateTokens(content), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	return artifact, replacement
}

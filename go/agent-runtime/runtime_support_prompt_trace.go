package agentruntime

import (
	"strings"
)

const (
	valueKindFEBE6019  = "kind"
	valueMode9251166E  = "mode"
	valueTitleFB9F13D2 = "title"
)

type messagePromptTraceInput struct {
	Plan               PromptTrace
	Mode               string
	PromptFingerprint  string
	StatefulDecision   statefulResponseDecision
	SentMessages       []Message
	FullMessages       []Message
	PreviousResponseID string
}

// buildMessagePromptTrace 将应用层 PromptPlan 转成消息 trace 可展示的稳定结构。
func buildMessagePromptTrace(input messagePromptTraceInput) *MessagePromptTrace {
	shape := summarizePromptShape(input.Mode, input.SentMessages, input.FullMessages, input.PreviousResponseID)
	blocks := make([]MessagePromptTraceBlock, 0, len(input.Plan.Blocks))
	for _, block := range input.Plan.Blocks {
		blocks = append(blocks, MessagePromptTraceBlock{
			Kind:          string(block.Kind),
			Title:         strings.TrimSpace(block.Title),
			TokenEstimate: block.TokenEstimate,
			Cacheable:     block.Cacheable,
			SourceCount:   block.SourceCount,
			SourceRefs:    promptTraceSourceRefs(block.SourceRefs),
		})
	}
	disabledReason := strings.TrimSpace(input.StatefulDecision.DisabledReason)
	if strings.TrimSpace(input.PreviousResponseID) != "" {
		disabledReason = ""
	}
	if !shouldExposePromptTraceDisabledReason(disabledReason) {
		disabledReason = ""
	}
	return &MessagePromptTrace{
		Mode:                   shape.Mode,
		PromptFingerprint:      strings.TrimSpace(input.PromptFingerprint),
		StatefulUsed:           strings.TrimSpace(input.PreviousResponseID) != "",
		StatefulDisabledReason: disabledReason,
		TotalTokenEstimate:     input.Plan.TotalTokenEstimate,
		SentTokenEstimate:      shape.TotalTokens,
		FullMessageCount:       shape.FullMessageCount,
		SentMessageCount:       shape.MessageCount,
		StatefulSavedMessages:  shape.StatefulSavedMsgs,
		StatefulSavedTokens:    shape.StatefulSavedToken,
		Blocks:                 blocks,
	}
}

func shouldExposePromptTraceDisabledReason(reason string) bool {
	switch strings.TrimSpace(reason) {
	case "", "route_or_branch_not_eligible":
		return false
	default:
		return true
	}
}

// cloneMessagePromptTrace 复制 PromptTrace，避免后续 payload 合并修改内存快照。
func cloneMessagePromptTrace(trace *MessagePromptTrace) *MessagePromptTrace {
	if trace == nil {
		return nil
	}
	cloned := *trace
	if len(trace.Blocks) > 0 {
		cloned.Blocks = append([]MessagePromptTraceBlock(nil), trace.Blocks...)
		for index := range cloned.Blocks {
			if len(trace.Blocks[index].SourceRefs) > 0 {
				cloned.Blocks[index].SourceRefs = append([]MessagePromptTraceSourceRef(nil), trace.Blocks[index].SourceRefs...)
			}
		}
	}
	return &cloned
}

// messagePromptTracePayload 将 PromptTrace 写入 trace payload，供持久化后复原。
func messagePromptTracePayload(trace *MessagePromptTrace) map[string]interface{} {
	if trace == nil {
		return nil
	}
	blocks := make([]map[string]interface{}, 0, len(trace.Blocks))
	for _, block := range trace.Blocks {
		sourceRefs := make([]map[string]interface{}, 0, len(block.SourceRefs))
		for _, ref := range block.SourceRefs {
			sourceRef := map[string]interface{}{
				"sourceType":       strings.TrimSpace(ref.SourceType),
				"sourceID":         strings.TrimSpace(ref.SourceID),
				valueTitleFB9F13D2: strings.TrimSpace(ref.Title),
			}
			if strings.TrimSpace(ref.ArtifactID) != "" {
				sourceRef["artifactID"] = ref.ArtifactID
			}
			sourceRefs = append(sourceRefs, sourceRef)
		}
		blocks = append(blocks, map[string]interface{}{
			valueKindFEBE6019:  strings.TrimSpace(block.Kind),
			valueTitleFB9F13D2: strings.TrimSpace(block.Title),
			"tokenEstimate":    block.TokenEstimate,
			"cacheable":        block.Cacheable,
			"sourceCount":      block.SourceCount,
			"sourceRefs":       sourceRefs,
		})
	}
	return map[string]interface{}{
		valueMode9251166E:        strings.TrimSpace(trace.Mode),
		"promptFingerprint":      strings.TrimSpace(trace.PromptFingerprint),
		"statefulUsed":           trace.StatefulUsed,
		"statefulDisabledReason": strings.TrimSpace(trace.StatefulDisabledReason),
		"totalTokenEstimate":     trace.TotalTokenEstimate,
		"sentTokenEstimate":      trace.SentTokenEstimate,
		"fullMessageCount":       trace.FullMessageCount,
		"sentMessageCount":       trace.SentMessageCount,
		"statefulSavedMessages":  trace.StatefulSavedMessages,
		"statefulSavedTokens":    trace.StatefulSavedTokens,
		"blocks":                 blocks,
	}
}

// messagePromptTraceFromPayload 从 process trace payload 中恢复结构化 PromptTrace。

// promptTraceSourceRefs 将规划器来源引用转换为领域 trace 来源引用。
func promptTraceSourceRefs(refs []PromptSourceRef) []MessagePromptTraceSourceRef {
	if len(refs) == 0 {
		return nil
	}
	result := make([]MessagePromptTraceSourceRef, 0, len(refs))
	for _, ref := range refs {
		result = append(result, MessagePromptTraceSourceRef{
			SourceType: strings.TrimSpace(ref.SourceType),
			SourceID:   strings.TrimSpace(ref.SourceID),
			Title:      strings.TrimSpace(ref.Title),
			ArtifactID: ref.ArtifactID,
		})
	}
	return result
}

// promptTraceSourceRefsFromPayload 从持久化 payload 中恢复来源引用。

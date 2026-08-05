// Package agentruntime owns Agent Runtime use cases and policy.
package agentruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	domainconversation "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueUser90BA419D = "user"
)

const (
	valueAssistantB87088D6 = "assistant"
	valueImageBE9EE30F     = "image"
	valueSystemE36EB5DA    = "system"
)

// Provider request wrappers and token estimation both add uncertainty around
// the configured input limit. Keep bounded transport headroom while accounting
// for tool definitions, which providers count as input context too.
const promptTransportBudgetPercent int64 = 80

// The gateway request body includes JSON message and tool-schema envelopes in
// addition to model tokens. Bound that serialized core independently so an
// upstream HTTP body limit cannot be exceeded by a long transcript.
const (
	promptTransportBytesNumerator   int64 = 5
	promptTransportBytesDenominator int64 = 2
)

// PromptBlockKind 标识 PromptPlan 中每一类上下文块。
type PromptBlockKind string

const (
	PromptBlockSystemPolicy       PromptBlockKind = "system_policy"
	PromptBlockStableContext      PromptBlockKind = "stable_context"
	PromptBlockHistoricalEvidence PromptBlockKind = "historical_evidence"
	PromptBlockDynamicContext     PromptBlockKind = "dynamic_context"
	PromptBlockSkillContext       PromptBlockKind = "skill_context"
	PromptBlockToolGuidance       PromptBlockKind = "tool_guidance"
	PromptBlockWorkspaceContext   PromptBlockKind = "workspace_context"
	PromptBlockTranscript         PromptBlockKind = "transcript"
)

// PromptBlockTrace 描述一次请求中单个上下文块的规划结果。
type PromptBlockTrace struct {
	Kind          PromptBlockKind
	Title         string
	TokenEstimate int64
	Cacheable     bool
	SourceCount   int
	SourceRefs    []PromptSourceRef
}

// PromptSourceRef 描述 PromptPlan 块的来源引用。
type PromptSourceRef struct {
	SourceType string
	SourceID   string
	Title      string
	ArtifactID string
}

// PromptTrace 汇总本轮发送给上游前的上下文形态。
type PromptTrace struct {
	Blocks                []PromptBlockTrace
	TotalTokenEstimate    int64
	HardInputTokens       int64              `json:"hardInputTokens,omitempty"`
	SoftInputTokens       int64              `json:"softInputTokens,omitempty"`
	RawTokenEstimate      int64              `json:"rawTokenEstimate,omitempty"`
	AdjustedTokenEstimate int64              `json:"adjustedTokenEstimate,omitempty"`
	TokenCountSource      string             `json:"tokenCountSource,omitempty"`
	TrimActions           []PromptTrimAction `json:"trimActions,omitempty"`
}

// PromptTrimAction records a safe structural reduction without exposing prompt content.
type PromptTrimAction struct {
	Action       string `json:"action"`
	MessageCount int    `json:"messageCount,omitempty"`
	ArtifactID   string `json:"artifactID,omitempty"`
	ContentHash  string `json:"contentHash,omitempty"`
}

// PromptPlan 是对话请求发送前的唯一上下文规划结果。
type PromptPlan struct {
	Messages []Message
	Trace    PromptTrace
}

type promptPlanInput struct {
	BaseMessages      []Message
	StableAttachments []AttachmentInput
	DynamicContext    userContextInput
	SkillPrompts      *skillPrompts
	ToolRuntime       selectedToolRuntime
	Config            Config
	Actor             domainconversation.ActorRef
	OpenFileContent   func(context.Context, domainconversation.ActorRef, string) (io.ReadCloser, error)
}

// buildPromptPlan 按稳定上下文、动态上下文、工具规则的固定顺序生成最终上游消息。
func buildPromptPlan(ctx context.Context, input promptPlanInput) PromptPlan {
	messages := cloneLLMMessages(input.BaseMessages)
	trace := PromptTrace{}

	before := len(messages)
	messages = prependStableFileContext(messages, input.StableAttachments)
	if len(messages) > before {
		sourceRefs := stableAttachmentSourceRefs(input.StableAttachments, input.DynamicContext.CurrentArtifacts)
		trace.addBlock(PromptBlockTrace{
			Kind:          PromptBlockStableContext,
			Title:         "稳定文件上下文",
			TokenEstimate: estimateMessageTokens(messages[0]),
			Cacheable:     true,
			SourceCount:   len(sourceRefs),
			SourceRefs:    sourceRefs,
		})
	}
	systemPolicyCount := countMessagesByRole(input.BaseMessages, valueSystemE36EB5DA)
	if systemPolicyCount > 0 {
		trace.addBlock(PromptBlockTrace{
			Kind:          PromptBlockSystemPolicy,
			Title:         "系统策略",
			TokenEstimate: estimateMessagesByRole(input.BaseMessages, valueSystemE36EB5DA),
			Cacheable:     true,
			SourceCount:   systemPolicyCount,
		})
	}
	trace.addBlock(PromptBlockTrace{
		Kind:          PromptBlockTranscript,
		Title:         "历史对话",
		TokenEstimate: estimateTranscriptTokens(input.BaseMessages),
		Cacheable:     false,
		SourceCount:   countMessagesByRole(input.BaseMessages, valueUser90BA419D) + countMessagesByRole(input.BaseMessages, valueAssistantB87088D6),
	})

	messages = injectDynamicPromptPlanContext(ctx, messages, input, &trace)

	before = len(messages)
	messages = injectSkillPrompts(messages, input.SkillPrompts)
	if len(messages) > before && input.SkillPrompts != nil {
		inserted := findSkillPromptMessage(messages)
		tokenEstimate := int64(0)
		if inserted >= 0 {
			tokenEstimate = estimateMessageTokens(messages[inserted])
		}
		trace.addBlock(PromptBlockTrace{
			Kind:          PromptBlockSkillContext,
			Title:         "Skill 上下文",
			TokenEstimate: tokenEstimate,
			Cacheable:     true,
			SourceCount:   len(input.SkillPrompts.Skills),
			SourceRefs:    skillPromptSourceRefs(input.SkillPrompts.Skills),
		})
	}

	before = len(messages)
	messages = injectMCPToolGuidance(messages, input.ToolRuntime, input.Config.Tools.Prompt)
	toolTokens := estimateToolDefinitionTokens(input.ToolRuntime.definitions)
	if len(messages) > before {
		inserted := findToolGuidanceMessage(messages)
		tokenEstimate := int64(0)
		if inserted >= 0 {
			tokenEstimate = estimateMessageTokens(messages[inserted])
		}
		trace.addBlock(PromptBlockTrace{
			Kind:          PromptBlockToolGuidance,
			Title:         "工具使用规则",
			TokenEstimate: tokenEstimate + toolTokens,
			Cacheable:     true,
			SourceCount:   len(input.ToolRuntime.definitions),
			SourceRefs:    toolDefinitionSourceRefs(input.ToolRuntime.definitions),
		})
	}
	messages = enforcePromptInputBudget(messages, toolTokens, input.Config.Context.MaxInputTokens)
	messages = enforcePromptTransportByteBudget(messages, input.ToolRuntime.definitions, input.Config.Context.MaxInputTokens)
	trace.updateTranscript(messages)
	messages = markLeadingSystemMessagesCacheable(messages)

	trace.TotalTokenEstimate = estimatePromptTokens(messages) + toolTokens
	return PromptPlan{Messages: messages, Trace: trace}
}

func enforcePromptTransportByteBudget(messages []Message, definitions []ToolDefinition, maxInputTokens int) []Message {
	if maxInputTokens <= 0 || len(messages) == 0 {
		return messages
	}
	// Keep the multiplication expressed without a fractional constant.
	budget := int64(maxInputTokens) * promptTransportBytesNumerator / promptTransportBytesDenominator
	if budget < 1 {
		budget = 1
	}
	result := cloneLLMMessages(messages)
	for estimatePromptTransportBytes(result, definitions) > budget {
		trimmed, ok := trimOldestPromptTurn(result)
		if !ok {
			trimmed, ok = trimSupersededFailedToolAttempt(result)
		}
		if !ok {
			break
		}
		result = trimmed
	}
	return result
}

func estimatePromptTransportBytes(messages []Message, definitions []ToolDefinition) int64 {
	raw, err := json.Marshal(struct {
		Messages []Message        `json:"messages"`
		Tools    []ToolDefinition `json:"tools,omitempty"`
	}{Messages: messages, Tools: definitions})
	if err != nil {
		return 0
	}
	return int64(len(raw))
}

func estimateToolDefinitionTokens(definitions []ToolDefinition) int64 {
	if len(definitions) == 0 {
		return 0
	}
	raw, err := json.Marshal(definitions)
	if err != nil {
		return 0
	}
	return estimateTokens(string(raw))
}

func enforcePromptInputBudget(messages []Message, toolTokens int64, maxInputTokens int) []Message {
	if maxInputTokens <= 0 || len(messages) == 0 {
		return messages
	}
	budget := int64(maxInputTokens) * promptTransportBudgetPercent / 100
	if budget < 1 {
		budget = 1
	}
	result := cloneLLMMessages(messages)
	for estimatePromptTokens(result)+toolTokens > budget {
		trimmed, ok := trimOldestPromptTurn(result)
		if !ok {
			trimmed, ok = trimSupersededFailedToolAttempt(result)
		}
		if !ok {
			break
		}
		result = trimmed
	}
	return result
}

func trimSupersededFailedToolAttempt(messages []Message) ([]Message, bool) {
	for index := 0; index+1 < len(messages); index++ {
		if len(messages[index].ToolCalls) == 0 || !messageHasFailedToolResult(messages[index+1]) {
			continue
		}
		if !laterSuccessfulToolResult(messages[index+2:], messages[index].ToolCalls) {
			continue
		}
		result := make([]Message, 0, len(messages)-2)
		result = append(result, messages[:index]...)
		result = append(result, messages[index+2:]...)
		return result, true
	}
	return messages, false
}

func laterSuccessfulToolResult(messages []Message, failedCalls []ToolCall) bool {
	failedNames := make(map[string]struct{}, len(failedCalls))
	for _, call := range failedCalls {
		if name := strings.TrimSpace(call.ToolName); name != "" {
			failedNames[name] = struct{}{}
		}
	}
	for _, message := range messages {
		for _, result := range message.ToolResults {
			if _, sameTool := failedNames[strings.TrimSpace(result.ToolName)]; !sameTool {
				continue
			}
			status := strings.ToLower(strings.TrimSpace(result.Status))
			if strings.TrimSpace(result.Error) == "" && (status == valueSuccess4D886D19 || status == "succeeded" || status == "completed") {
				return true
			}
		}
	}
	return false
}

func messageHasFailedToolResult(message Message) bool {
	for _, result := range message.ToolResults {
		if strings.TrimSpace(result.Error) != "" || strings.EqualFold(strings.TrimSpace(result.Status), "failed") {
			return true
		}
	}
	return false
}

func trimOldestPromptTurn(messages []Message) ([]Message, bool) {
	first := -1
	latestUser := -1
	for index, message := range messages {
		if message.Role != valueSystemE36EB5DA && first < 0 {
			first = index
		}
		if message.Role == valueUser90BA419D {
			latestUser = index
		}
	}
	if first < 0 || first >= latestUser {
		return messages, false
	}
	end := latestUser
	for index := first + 1; index < latestUser; index++ {
		if messages[index].Role == valueUser90BA419D {
			end = index
			break
		}
	}
	result := make([]Message, 0, len(messages)-(end-first))
	result = append(result, messages[:first]...)
	result = append(result, messages[end:]...)
	return result, true
}

func injectDynamicPromptPlanContext(ctx context.Context, messages []Message, input promptPlanInput, trace *PromptTrace) []Message {
	beforeMessages := cloneLLMMessages(messages)
	messages = injectUserContext(ctx, messages, input.DynamicContext, input.Config, input.Actor, input.OpenFileContent)
	if !promptMessagesChanged(beforeMessages, messages) {
		return messages
	}
	historicalTokenEstimate := addHistoricalEvidenceTrace(trace, input.DynamicContext)
	addDynamicContextTrace(trace, input.DynamicContext, messages, beforeMessages, historicalTokenEstimate)
	return messages
}

func addHistoricalEvidenceTrace(trace *PromptTrace, dynamicContext userContextInput) int64 {
	tokenEstimate := estimateContextArtifactsTokens(dynamicContext.HistoricalArtifacts)
	if len(dynamicContext.HistoricalArtifacts) == 0 {
		return tokenEstimate
	}
	sourceRefs := historicalArtifactSourceRefs(dynamicContext.HistoricalArtifacts)
	trace.addBlock(PromptBlockTrace{
		Kind:          PromptBlockHistoricalEvidence,
		Title:         "历史证据",
		TokenEstimate: tokenEstimate,
		Cacheable:     false,
		SourceCount:   len(sourceRefs),
		SourceRefs:    sourceRefs,
	})
	return tokenEstimate
}

func addDynamicContextTrace(
	trace *PromptTrace,
	dynamicContext userContextInput,
	messages []Message,
	beforeMessages []Message,
	historicalTokenEstimate int64,
) {
	sourceRefs := dynamicContextSourceRefs(dynamicContext)
	if len(sourceRefs) == 0 {
		return
	}
	trace.addBlock(PromptBlockTrace{
		Kind:          PromptBlockDynamicContext,
		Title:         "本轮动态上下文",
		TokenEstimate: estimatePromptTokens(messages) - estimatePromptTokens(beforeMessages) - historicalTokenEstimate,
		Cacheable:     false,
		SourceCount:   len(sourceRefs),
		SourceRefs:    sourceRefs,
	})
}

// addBlock 规范化并追加单个上下文块 trace。
func (t *PromptTrace) addBlock(block PromptBlockTrace) {
	if block.TokenEstimate < 0 {
		block.TokenEstimate = 0
	}
	if block.SourceCount < 0 {
		block.SourceCount = 0
	}
	t.Blocks = append(t.Blocks, block)
}

func (t *PromptTrace) updateTranscript(messages []Message) {
	for index := range t.Blocks {
		if t.Blocks[index].Kind != PromptBlockTranscript {
			continue
		}
		t.Blocks[index].TokenEstimate = estimateTranscriptTokens(messages)
		t.Blocks[index].SourceCount = countMessagesByRole(messages, valueUser90BA419D) +
			countMessagesByRole(messages, valueAssistantB87088D6)
		return
	}
}

// cloneLLMMessages 复制消息切片，避免规划过程修改调用方持有的切片头。
func cloneLLMMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return nil
	}
	result := make([]Message, len(messages))
	copy(result, messages)
	return result
}

// countMessagesByRole 统计指定角色消息数量，用于 PromptTrace 来源计数。
func countMessagesByRole(messages []Message, role string) int {
	count := 0
	for _, item := range messages {
		if item.Role == role {
			count++
		}
	}
	return count
}

// estimateMessagesByRole 估算指定角色消息的 token 数。
func estimateMessagesByRole(messages []Message, role string) int64 {
	var total int64
	for _, item := range messages {
		if item.Role == role {
			total += estimateMessageTokens(item)
		}
	}
	return total
}

// estimateTranscriptTokens 只估算真实对话轮次，不把 system 策略混进历史对话。
func estimateTranscriptTokens(messages []Message) int64 {
	var total int64
	for _, item := range messages {
		if item.Role == valueUser90BA419D || item.Role == valueAssistantB87088D6 {
			total += estimateMessageTokens(item)
		}
	}
	return total
}

// countStableTextAttachments 统计可进入稳定文本上下文的附件数量。
func countStableTextAttachments(attachments []AttachmentInput) int {
	count := 0
	for _, att := range attachments {
		if normalizeAttachmentKind(att.Kind, att.MimeType) == valueImageBE9EE30F {
			continue
		}
		if strings.TrimSpace(att.ExtractedText) == "" {
			continue
		}
		count++
	}
	return count
}

// stableAttachmentSourceRefs 提取稳定全文文件的来源引用。
func stableAttachmentSourceRefs(attachments []AttachmentInput, currentArtifacts []domainconversation.ContextArtifact) []PromptSourceRef {
	refs := make([]PromptSourceRef, 0, countStableTextAttachments(attachments))
	fallbackArtifacts := contextArtifactsByKindAndSourceID(currentArtifacts, domainconversation.ContextArtifactFileRAGFallback)
	for _, att := range attachments {
		if normalizeAttachmentKind(att.Kind, att.MimeType) == valueImageBE9EE30F {
			continue
		}
		if strings.TrimSpace(att.ExtractedText) == "" {
			continue
		}
		sourceID := stableAttachmentSourceID(att)
		if artifact, ok := fallbackArtifacts[fallbackFileSourceID(att)]; ok {
			refs = appendPromptSourceRefWithArtifactID(refs, string(domainconversation.ContextArtifactFileRAGFallback), sourceID, firstNonEmptyString(att.FileName, artifact.SourceTitle), artifact.ArtifactID)
			continue
		}
		refs = appendPromptSourceRef(refs, "file_full", sourceID, att.FileName)
	}
	return refs
}

// dynamicContextSourceRefs 提取本轮动态上下文的来源引用。
func dynamicContextSourceRefs(input userContextInput) []PromptSourceRef {
	refs := make([]PromptSourceRef, 0, len(input.RAGChunks)+len(input.RecallChunks)+len(input.Memory)+len(input.Attachments)+1)
	ragArtifacts := contextArtifactsByKindAndSourceID(input.CurrentArtifacts, domainconversation.ContextArtifactFileRAGChunk)
	recallArtifacts := contextArtifactsByKindAndSourceID(input.CurrentArtifacts, domainconversation.ContextArtifactSemanticRecall)
	memoryArtifacts := contextArtifactsByKindAndSourceID(input.CurrentArtifacts, domainconversation.ContextArtifactUserMemory)
	for _, chunk := range input.RAGChunks {
		artifact := ragArtifacts[fileRAGChunkSourceID(chunk)]
		refs = appendPromptSourceRefWithArtifactID(refs, string(domainconversation.ContextArtifactFileRAGChunk), chunk.FileID, ragChunkSourceTitle(chunk), artifact.ArtifactID)
	}
	for _, chunk := range input.RecallChunks {
		sourceID := messageChunkSourceID(chunk)
		artifact := recallArtifacts[sourceID]
		refs = appendPromptSourceRefWithArtifactID(refs, string(domainconversation.ContextArtifactSemanticRecall), sourceID, chunk.Role, artifact.ArtifactID)
	}
	for _, memory := range input.Memory {
		sourceID := strings.TrimSpace(memory.MemoryKey)
		artifact := memoryArtifacts[sourceID]
		refs = appendPromptSourceRefWithArtifactID(refs, string(domainconversation.ContextArtifactUserMemory), sourceID, memory.Scope, artifact.ArtifactID)
	}
	if input.Snapshot != nil && strings.TrimSpace(input.Snapshot.Summary) != "" {
		refs = appendPromptSourceRef(refs, "summary", input.Snapshot.Strategy, "上下文摘要")
	}
	for _, att := range input.Attachments {
		refs = appendPromptSourceRef(refs, valueImageBE9EE30F, stableAttachmentSourceID(att), att.FileName)
	}
	return refs
}

// contextArtifactsByKindAndSourceID 按类型和来源 ID 建立索引，用于把已落库证据回填到 PromptTrace 来源。
func contextArtifactsByKindAndSourceID(artifacts []domainconversation.ContextArtifact, kind domainconversation.ContextArtifactKind) map[string]domainconversation.ContextArtifact {
	result := make(map[string]domainconversation.ContextArtifact)
	for _, artifact := range artifacts {
		if artifact.Kind != kind {
			continue
		}
		sourceID := strings.TrimSpace(artifact.SourceID)
		if sourceID == "" || strings.TrimSpace(artifact.ArtifactID) == "" {
			continue
		}
		result[sourceID] = artifact
	}
	return result
}

func ragChunkSourceTitle(chunk domainconversation.RAGChunk) string {
	title := strings.TrimSpace(chunk.FileName)
	if title == "" {
		title = strings.TrimSpace(chunk.FileID)
	}
	if chunk.ChunkIndex >= 0 {
		return fmt.Sprintf("%s #%d", title, chunk.ChunkIndex+1)
	}
	return title
}

// historicalArtifactSourceRefs 提取历史证据 artifact 的来源引用。
func historicalArtifactSourceRefs(artifacts []domainconversation.ContextArtifact) []PromptSourceRef {
	refs := make([]PromptSourceRef, 0, len(artifacts))
	for _, artifact := range artifacts {
		refs = appendPromptSourceRefWithArtifactID(refs, string(artifact.Kind), artifact.SourceID, artifact.SourceTitle, artifact.ArtifactID)
	}
	return refs
}

// skillPromptSourceRefs 提取本轮可用 Skill 的来源引用。
func skillPromptSourceRefs(skills []Skill) []PromptSourceRef {
	refs := make([]PromptSourceRef, 0, len(skills))
	for _, skill := range skills {
		refs = appendPromptSourceRef(refs, skill.Ref.Kind, skill.Ref.ID, skill.Title)
	}
	return refs
}

// toolDefinitionSourceRefs 提取本轮可用工具定义的来源引用。
func toolDefinitionSourceRefs(tools []ToolDefinition) []PromptSourceRef {
	refs := make([]PromptSourceRef, 0, len(tools))
	for _, tool := range tools {
		refs = appendPromptSourceRef(refs, "tool", tool.Name, tool.Name)
	}
	return refs
}

// appendPromptSourceRef 追加非空来源引用，保持 trace payload 干净。
func appendPromptSourceRef(refs []PromptSourceRef, sourceType string, sourceID string, title string) []PromptSourceRef {
	return appendPromptSourceRefWithArtifactID(refs, sourceType, sourceID, title, "")
}

// appendPromptSourceRefWithArtifactID 追加非空来源引用，并携带可追溯的 artifact 主键。
func appendPromptSourceRefWithArtifactID(refs []PromptSourceRef, sourceType string, sourceID string, title string, artifactID string) []PromptSourceRef {
	sourceType = strings.TrimSpace(sourceType)
	sourceID = strings.TrimSpace(sourceID)
	title = strings.TrimSpace(title)
	artifactID = strings.TrimSpace(artifactID)
	if sourceType == "" && sourceID == "" && title == "" && artifactID == "" {
		return refs
	}
	return append(refs, PromptSourceRef{
		SourceType: sourceType,
		SourceID:   sourceID,
		Title:      title,
		ArtifactID: artifactID,
	})
}

// stableAttachmentSourceID 选择文件来源的稳定标识，优先使用业务 fileID。
func stableAttachmentSourceID(att AttachmentInput) string {
	if id := strings.TrimSpace(att.FileID); id != "" {
		return id
	}
	if sha := strings.TrimSpace(att.SHA256); sha != "" {
		return sha
	}
	return strings.TrimSpace(att.FileName)
}

// messageChunkSourceID 生成消息语义分片的来源标识。
func messageChunkSourceID(chunk domainconversation.RecallChunk) string {
	if projectionID := strings.TrimSpace(chunk.Projection.ID); projectionID != "" {
		return fmt.Sprintf("%s:%d", projectionID, chunk.ChunkIndex)
	}
	return fmt.Sprintf("%s:%d", strings.TrimSpace(chunk.Role), chunk.ChunkIndex)
}

// estimateContextArtifactsTokens 估算历史证据块的 token 消耗。
func estimateContextArtifactsTokens(artifacts []domainconversation.ContextArtifact) int64 {
	var total int64
	for _, artifact := range artifacts {
		if artifact.TokenEstimate > 0 {
			total += artifact.TokenEstimate
			continue
		}
		total += estimateTokens(artifact.Content)
	}
	return total
}

// promptMessagesChanged 判断规划步骤是否改变了最终上游消息形态。
func promptMessagesChanged(left []Message, right []Message) bool {
	if len(left) != len(right) {
		return true
	}
	for i := range left {
		if left[i].Role != right[i].Role || left[i].Content != right[i].Content || len(left[i].Parts) != len(right[i].Parts) {
			return true
		}
	}
	return false
}

// findToolGuidanceMessage 定位工具使用规则消息，供 trace 估算 token。
func findToolGuidanceMessage(messages []Message) int {
	for i, item := range messages {
		if item.Role == valueSystemE36EB5DA && strings.HasPrefix(strings.TrimSpace(item.Content), "# tool_use") {
			return i
		}
	}
	return -1
}

// markLeadingSystemMessagesCacheable 给稳定 system 前缀加块级缓存提示。
func markLeadingSystemMessagesCacheable(messages []Message) []Message {
	if len(messages) == 0 {
		return messages
	}
	result := cloneLLMMessages(messages)
	cacheableIndices := make([]int, 0, 4)
	for index := range result {
		if result[index].Role != valueSystemE36EB5DA {
			break
		}
		if strings.TrimSpace(result[index].Content) == "" && len(result[index].Parts) == 0 {
			continue
		}
		cacheableIndices = append(cacheableIndices, index)
	}
	if len(cacheableIndices) > 4 {
		cacheableIndices = cacheableIndices[len(cacheableIndices)-4:]
	}
	for _, index := range cacheableIndices {
		result[index].CacheControl = &CacheControl{Type: "ephemeral"}
	}
	return result
}

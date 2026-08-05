// Package agentruntime owns Agent Runtime use cases and policy.
package agentruntime

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	domainconversation "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueRetry7C66A8AA          = "retry"
	statusRequestEntityTooLarge = 413
)

const (
	valueAnthropic563F0125 = "anthropic"
	valueDefaultD025BA82   = "default"
	valueEdit907769A2      = "edit"
	valueEvidence58BA4CBE  = "evidence"
	valueFile958FAA41      = "file"
	valueImage4EAFEEBE     = "image"
	valueName09837852      = "name"
	valueOpenai29FA5130    = "openai"
	valueSystemB5E762B9    = "system"
	valueText0F66BE5D      = "text"
	valueUnknown83F7FCF3   = "unknown"
	valueUserFBB5C5BA      = "user"
)

func normalizePublicID(raw string) string {
	return strings.ReplaceAll(strings.TrimSpace(raw), "-", "")
}

// isCJKRune 判断字符是否属于 CJK 字符范围（中文、日文、韩文）。
func isCJKRune(r rune) bool {
	return (r >= 0x2E80 && r <= 0x9FFF) || // CJK 部首、假名、统一表意文字
		(r >= 0xAC00 && r <= 0xD7AF) || // 韩文音节
		(r >= 0xF900 && r <= 0xFAFF) || // CJK 兼容汉字
		(r >= 0x20000 && r <= 0x2A6DF) // CJK 扩展 B
}

// estimateTokens 估算文本 token 数，区分 CJK 与其他字符权重。
// CJK 字符：约 1.5 chars/token；ASCII 及其他：约 4 chars/token。
func estimateTokens(content string) int64 {
	if len(content) == 0 {
		return 0
	}
	var cjk, other int64
	for _, r := range content {
		if isCJKRune(r) {
			cjk++
		} else {
			other++
		}
	}
	// CJK: tokens = ceil(cjk * 2/3)；other: tokens = ceil(other / 4)
	tokens := (cjk*2+2)/3 + (other+3)/4
	if tokens == 0 {
		return 1
	}
	return tokens
}

func estimateContentPartTokens(part ContentPart) int64 {
	switch part.Kind {
	case ContentPartImage:
		return 255
	case ContentPartFile:
		return estimateTokens(part.FileName) + estimateTokens(part.Text) + 8
	default:
		return estimateTokens(part.Text)
	}
}

func estimateMessageTokens(message Message) int64 {
	var tokens int64 = 4
	if message.Role != "" {
		tokens += 1
	}
	if len(message.Parts) > 0 {
		for _, part := range message.Parts {
			tokens += estimateContentPartTokens(part)
		}
	} else {
		tokens += estimateTokens(message.Content)
	}
	tokens += estimateTokens(message.ReasoningContent)
	for _, call := range message.ToolCalls {
		tokens += 8 + estimateTokens(call.ToolCallID) + estimateTokens(call.ToolType) + estimateTokens(call.ToolName) + estimateTokens(call.ArgumentsJSON) + estimateTokens(call.ThoughtSignature) + estimateTokens(call.Status) + estimateTokens(call.OutputJSON) + estimateTokens(call.ErrorJSON)
	}
	for _, result := range message.ToolResults {
		tokens += 6 + estimateTokens(result.ToolCallID) + estimateTokens(result.ToolName) + estimateTokens(result.OutputJSON) + estimateTokens(result.Status) + estimateTokens(result.Error)
	}
	if message.CacheControl != nil {
		tokens += 2 + estimateTokens(message.CacheControl.Type) + estimateTokens(message.CacheControl.TTL)
	}
	return tokens
}

func estimatePromptTokens(messages []Message) int64 {
	var tokens int64 = 2
	for _, message := range messages {
		tokens += estimateMessageTokens(message)
	}
	if tokens < 0 {
		return 0
	}
	return tokens
}

func compactSnippet(content string, maxLen int) string {
	value := strings.Join(strings.Fields(strings.TrimSpace(content)), " ")
	if value == "" {
		return ""
	}
	if maxLen <= 0 {
		maxLen = 120
	}
	runes := []rune(value)
	if len(runes) <= maxLen {
		return value
	}
	return string(runes[:maxLen]) + "..."
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if normalized := strings.TrimSpace(value); normalized != "" {
			return normalized
		}
	}
	return ""
}

func truncateError(message string) string {
	value := strings.TrimSpace(message)
	const limit = 255
	if len([]rune(value)) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

func classifyRunErrorCode(err error) string {
	var upstreamErr *UpstreamError
	if errors.As(err, &upstreamErr) {
		if upstreamErr.StatusCode == statusRequestEntityTooLarge {
			return ErrorCodeUpstreamPayloadTooLarge
		}
	}
	var workspaceErr *WorkspaceError
	if errors.As(err, &workspaceErr) && workspaceErr.Code() != "" {
		return workspaceErr.Code()
	}
	for _, item := range runErrorCodeMappings {
		if errors.Is(err, item.err) {
			return item.code
		}
	}
	return "internal_error"
}

type runErrorCodeMapping struct {
	err  error
	code string
}

var runErrorCodeMappings = []runErrorCodeMapping{
	{err: ErrThreadNotFound, code: "thread_not_found"},
	{err: ErrInvalidAttachmentReference, code: "invalid_attachment_reference"},
	{err: ErrAttachmentNotFound, code: "attachment_not_found"},
	{err: ErrContextBudgetExceeded, code: "context_budget_exceeded"},
	{err: ErrModelRouteNotConfigured, code: "model_route_not_configured"},
	{err: ErrUpstreamEmptyResponse, code: "upstream_empty_response"},
	{err: ErrToolRunFinalAnswerMissing, code: "tool_run_final_answer_missing"},
	{err: ErrRunCanceled, code: "generation_canceled"},
	{err: ErrUpstreamRequestFailed, code: "upstream_request_failed"},
	{err: ErrWorkspaceArtifactMissing, code: errorCodeWorkspaceArtifactMissing},
}

func messageErrorSummary(err error) string {
	if err == nil {
		return ""
	}
	var upstreamErr *UpstreamError
	if errors.As(err, &upstreamErr) {
		return upstreamErrorSummary(upstreamErr)
	}
	value := strings.TrimSpace(err.Error())
	if value == "" {
		return ""
	}
	prefix := ErrUpstreamRequestFailed.Error() + ":"
	for strings.HasPrefix(value, prefix) {
		value = strings.TrimSpace(strings.TrimPrefix(value, prefix))
	}
	return value
}

func messageErrorDebug(err error) *UpstreamDebugSnapshot {
	if err == nil {
		return nil
	}
	var upstreamErr *UpstreamError
	if errors.As(err, &upstreamErr) {
		return sanitizeUpstreamDebugSnapshot(upstreamErr.Debug)
	}
	return nil
}

func sanitizeUpstreamDebugSnapshot(debug *UpstreamDebugSnapshot) *UpstreamDebugSnapshot {
	if debug == nil {
		return nil
	}
	return &UpstreamDebugSnapshot{
		Request: UpstreamDebugRequest{
			Method: debug.Request.Method,
			Path:   debug.Request.Path,
			Body:   sanitizeUpstreamNameJSON(debug.Request.Body),
		},
		Response: UpstreamDebugResponse{
			StatusCode: debug.Response.StatusCode,
			Body:       sanitizeUpstreamNameJSON(debug.Response.Body),
		},
	}
}

func sanitizeUpstreamNameJSON(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return raw
	}
	var payload interface{}
	if err := json.Unmarshal([]byte(value), &payload); err != nil {
		return raw
	}
	deleteUpstreamNameValues(payload, "")
	data, err := json.Marshal(payload)
	if err != nil {
		return raw
	}
	return string(data)
}

func deleteUpstreamNameValues(value interface{}, parentKey string) {
	switch current := value.(type) {
	case map[string]interface{}:
		for key, child := range current {
			if isUpstreamNameKey(key, parentKey) {
				delete(current, key)
				continue
			}
			deleteUpstreamNameValues(child, key)
		}
	case []interface{}:
		for _, child := range current {
			deleteUpstreamNameValues(child, parentKey)
		}
	}
}

func isUpstreamNameKey(key string, parentKey string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "_", ""))
	if normalized == "upstreamname" {
		return true
	}
	return strings.ToLower(strings.TrimSpace(parentKey)) == "upstream" && (normalized == valueName09837852 || normalized == "displayname")
}

func upstreamErrorSummary(err *UpstreamError) string {
	if err == nil {
		return ""
	}
	lines := make([]string, 0, 3)
	if isSuccessfulUpstreamStatus(err.StatusCode) {
		lines = append(lines, fmt.Sprintf("模型响应格式不兼容（HTTP %d）", err.StatusCode))
		lines = append(lines, "错误：上游返回成功状态码，但响应格式与当前协议不兼容")
		return strings.Join(lines, "\n")
	}
	if err.StatusCode > 0 {
		lines = append(lines, fmt.Sprintf("模型请求失败（HTTP %d）", err.StatusCode))
	} else {
		lines = append(lines, "模型请求失败")
	}
	if message := normalizeUpstreamErrorMessage(err.Message); message != "" {
		lines = append(lines, "错误："+message)
	}
	return strings.Join(lines, "\n")
}

func isSuccessfulUpstreamStatus(statusCode int) bool {
	return statusCode >= 200 && statusCode < 300
}

func normalizeUpstreamErrorMessage(message string) string {
	value := strings.TrimSpace(message)
	if value == "" || looksLikeRawSSEBody(value) {
		return ""
	}
	return value
}

func looksLikeRawSSEBody(value string) bool {
	normalized := strings.TrimSpace(value)
	return strings.HasPrefix(normalized, "data:") ||
		strings.Contains(normalized, "\ndata:") ||
		strings.Contains(normalized, " data:")
}

func normalizeAttachmentKind(kind string, mimeType string) string {
	value := strings.TrimSpace(kind)
	if value != "" {
		return value
	}
	return inferAttachmentKind(mimeType)
}

// NormalizeAttachmentKind 规范化附件类型，供边界层复用。
func NormalizeAttachmentKind(kind string, mimeType string) string {
	return normalizeAttachmentKind(kind, mimeType)
}

func inferAttachmentKind(mimeType string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(mimeType)), "image/") {
		return valueImage4EAFEEBE
	}
	return valueFile958FAA41
}

func fallbackContentType(contentType string) string {
	value := strings.TrimSpace(contentType)
	if value == "" {
		return valueText0F66BE5D
	}
	return value
}

type userContextInput struct {
	Attachments         []AttachmentInput
	RAGChunks           []domainconversation.RAGChunk
	HistoricalArtifacts []domainconversation.ContextArtifact
	CurrentArtifacts    []domainconversation.ContextArtifact
	Snapshot            *snapshotContext
	Memory              []MemoryItem
	RecallChunks        []domainconversation.RecallChunk
}

type snapshotContext struct {
	Summary  string
	FromTurn int
	ToTurn   int
	Strategy string
}

// prependStableFileContext 将可全文注入的文本文件固定放在消息前缀，避免多轮对话中
// 同一份文件内容漂移到最新 user 消息，破坏上游前缀缓存。
func prependStableFileContext(messages []Message, attachments []AttachmentInput) []Message {
	contextXML := buildStableFileContextXML(attachments)
	if contextXML.empty() {
		return messages
	}
	content := buildUserContextPrompt("", contextXML)
	if strings.TrimSpace(content) == "" {
		return messages
	}
	result := make([]Message, 0, len(messages)+1)
	result = append(result, Message{
		Role:    valueSystemB5E762B9,
		Content: content,
	})
	result = append(result, messages...)
	return result
}

func buildStableFileContextXML(attachments []AttachmentInput) userContextXML {
	if len(attachments) == 0 {
		return userContextXML{}
	}
	items := make([]AttachmentInput, 0, len(attachments))
	for _, att := range attachments {
		kind := normalizeAttachmentKind(att.Kind, att.MimeType)
		if kind == valueImage4EAFEEBE || strings.TrimSpace(att.ExtractedText) == "" {
			continue
		}
		items = append(items, att)
	}
	sort.SliceStable(items, func(i, j int) bool {
		left := stableAttachmentSortKey(items[i])
		right := stableAttachmentSortKey(items[j])
		return left < right
	})

	contextXML := userContextXML{files: make([]string, 0, len(items))}
	for _, att := range items {
		contextXML.files = append(contextXML.files, formatAttachmentFileContext(att.FileName, att.ExtractedText))
	}
	return contextXML
}

func stableAttachmentSortKey(att AttachmentInput) string {
	if value := strings.TrimSpace(att.FileID); value != "" {
		return "0:" + value
	}
	if value := strings.TrimSpace(att.SHA256); value != "" {
		return "1:" + value
	}
	if value := strings.TrimSpace(att.FileName); value != "" {
		return "2:" + value
	}
	return "3:"
}

func imageAttachmentsForCurrentUser(attachments []AttachmentInput) []AttachmentInput {
	if len(attachments) == 0 {
		return nil
	}
	result := make([]AttachmentInput, 0)
	for _, att := range attachments {
		if normalizeAttachmentKind(att.Kind, att.MimeType) == valueImage4EAFEEBE {
			result = append(result, att)
		}
	}
	return result
}

func injectUserContext(
	ctx context.Context,
	messages []Message,
	input userContextInput,
	cfg Config,
	actor domainconversation.ActorRef,
	openFile func(context.Context, domainconversation.ActorRef, string) (io.ReadCloser, error),
) []Message {
	if userContextInputEmpty(input) {
		return messages
	}

	// 找到最后一条用户消息，构建 ContentParts
	lastUserIdx := lastUserMessageIndex(messages)
	if lastUserIdx < 0 || lastUserIdx >= len(messages) {
		return messages
	}

	lastUserMsg := messages[lastUserIdx]
	contextXML := buildUserContextXML(input)
	imageParts := buildUserContextImageParts(ctx, actor, input.Attachments, openFile, effectiveImageMaxDimension(cfg))

	if len(imageParts) == 0 && contextXML.empty() {
		return messages
	}

	result := make([]Message, len(messages))
	copy(result, messages)
	result[lastUserIdx] = userContextMessage(lastUserMsg, contextXML, imageParts)
	return result
}

func userContextInputEmpty(input userContextInput) bool {
	return len(input.Attachments) == 0 &&
		len(input.RAGChunks) == 0 &&
		len(input.HistoricalArtifacts) == 0 &&
		input.Snapshot == nil &&
		len(input.Memory) == 0 &&
		len(input.RecallChunks) == 0
}

func effectiveImageMaxDimension(cfg Config) int {
	if cfg.Files.ImageMaxDimension > 0 {
		return cfg.Files.ImageMaxDimension
	}
	return 1024
}

func lastUserMessageIndex(messages []Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == valueUserFBB5C5BA {
			return i
		}
	}
	return -1
}

func userContextMessage(lastUserMsg Message, contextXML userContextXML, imageParts []ContentPart) Message {
	content := strings.TrimSpace(lastUserMsg.Content)
	if !contextXML.empty() {
		content = buildUserContextPrompt(content, contextXML)
	}
	if len(imageParts) == 0 {
		return Message{Role: lastUserMsg.Role, Content: content}
	}
	parts := make([]ContentPart, 0, 1+len(imageParts))
	if content != "" {
		parts = append(parts, ContentPart{
			Kind: ContentPartText,
			Text: content,
		})
	}
	parts = append(parts, imageParts...)
	return Message{Role: lastUserMsg.Role, Parts: parts}
}

func buildUserContextImageParts(
	ctx context.Context,
	actor domainconversation.ActorRef,
	attachments []AttachmentInput,
	openFile func(context.Context, domainconversation.ActorRef, string) (io.ReadCloser, error),
	maxDim int,
) []ContentPart {
	imageParts := make([]ContentPart, 0, len(attachments))
	for _, att := range attachments {
		part, ok := userContextImagePart(ctx, actor, att, openFile, maxDim)
		if ok {
			imageParts = append(imageParts, part)
		}
	}
	return imageParts
}

func userContextImagePart(
	ctx context.Context,
	actor domainconversation.ActorRef,
	att AttachmentInput,
	openFile func(context.Context, domainconversation.ActorRef, string) (io.ReadCloser, error),
	maxDim int,
) (ContentPart, bool) {
	if normalizeAttachmentKind(att.Kind, att.MimeType) != valueImage4EAFEEBE || strings.TrimSpace(att.FileID) == "" {
		return ContentPart{}, false
	}
	if openFile == nil {
		return ContentPart{}, false
	}
	reader, readErr := openFile(ctx, actor, strings.TrimSpace(att.FileID))
	if readErr != nil {
		return ContentPart{}, false
	}
	imgData, readErr := io.ReadAll(io.LimitReader(reader, 50*1024*1024))
	_ = reader.Close()
	if readErr != nil {
		return ContentPart{}, false
	}
	mime := resolveImageMimeType(att.MimeType)
	return ContentPart{
		Kind:     ContentPartImage,
		MimeType: mime,
		Data:     resizeImageIfNeeded(imgData, mime, maxDim),
	}, true
}

func formatAttachmentFileContext(fileName string, text string) string {
	name := strings.TrimSpace(fileName)
	if name == "" {
		name = "未命名文件"
	}
	return `<file name="` + xmlEscapeAttr(name) + `">` + xmlEscapeText(strings.TrimSpace(text)) + `</file>`
}

type userContextXML struct {
	summary  string
	memory   []string
	files    []string
	evidence []string
	rag      []string
	recall   []string
}

func (x userContextXML) empty() bool {
	return strings.TrimSpace(x.summary) == "" &&
		len(x.memory) == 0 &&
		len(x.files) == 0 &&
		len(x.evidence) == 0 &&
		len(x.rag) == 0 &&
		len(x.recall) == 0
}

func buildUserContextXML(input userContextInput) userContextXML {
	return userContextXML{
		summary:  formatSnapshotContext(input.Snapshot),
		memory:   formatMemoryContext(input.Memory),
		evidence: formatHistoricalEvidenceContext(input.HistoricalArtifacts),
		rag:      formatRAGFileContext(input.RAGChunks),
		recall:   formatRecallContext(input.RecallChunks),
	}
}

func formatSnapshotContext(snapshot *snapshotContext) string {
	if snapshot == nil || strings.TrimSpace(snapshot.Summary) == "" {
		return ""
	}
	attrs := ` from="` + xmlEscapeAttr(fmt.Sprintf("%d", snapshot.FromTurn)) + `" to="` + xmlEscapeAttr(fmt.Sprintf("%d", snapshot.ToTurn)) + `"`
	if strategy := strings.TrimSpace(snapshot.Strategy); strategy != "" {
		attrs += ` strategy="` + xmlEscapeAttr(strategy) + `"`
	}
	return "<sum" + attrs + ">" + xmlEscapeText(strings.TrimSpace(snapshot.Summary)) + "</sum>"
}

func formatMemoryContext(memories []MemoryItem) []string {
	if len(memories) == 0 {
		return nil
	}
	items := make([]string, 0, len(memories))
	for _, memory := range memories {
		key := strings.TrimSpace(memory.MemoryKey)
		value := strings.TrimSpace(memory.Value)
		if key == "" || value == "" {
			continue
		}
		items = append(items, `<mem k="`+xmlEscapeAttr(key)+`">`+xmlEscapeText(value)+`</mem>`)
	}
	return items
}

func formatRAGFileContext(chunks []domainconversation.RAGChunk) []string {
	if len(chunks) == 0 {
		return nil
	}
	items := make([]string, 0, len(chunks))
	for index, chunk := range chunks {
		text := strings.TrimSpace(chunk.Content)
		if text == "" {
			continue
		}
		name := strings.TrimSpace(chunk.FileName)
		if name == "" {
			name = strings.TrimSpace(chunk.FileID)
		}
		if name == "" {
			name = valueUnknown83F7FCF3
		}
		chunkIndex := chunk.ChunkIndex
		if chunkIndex <= 0 {
			chunkIndex = index + 1
		}
		items = append(items, `<doc name="`+xmlEscapeAttr(name)+`" i="`+xmlEscapeAttr(fmt.Sprintf("%d", chunkIndex))+`">`+xmlEscapeText(text)+`</doc>`)
	}
	return items
}

func formatHistoricalEvidenceContext(artifacts []domainconversation.ContextArtifact) []string {
	if len(artifacts) == 0 {
		return nil
	}
	items := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		content := strings.TrimSpace(artifact.Content)
		if content == "" {
			continue
		}
		kind := strings.TrimSpace(string(artifact.Kind))
		if kind == "" {
			kind = valueEvidence58BA4CBE
		}
		source := strings.TrimSpace(artifact.SourceTitle)
		if source == "" {
			source = strings.TrimSpace(artifact.SourceID)
		}
		if source == "" {
			source = valueUnknown83F7FCF3
		}
		items = append(items, `<ev k="`+xmlEscapeAttr(kind)+`" src="`+xmlEscapeAttr(source)+`">`+xmlEscapeText(compactSnippet(content, 500))+`</ev>`)
	}
	return items
}

func formatRecallContext(chunks []domainconversation.RecallChunk) []string {
	if len(chunks) == 0 {
		return nil
	}
	items := make([]string, 0, len(chunks))
	for index, chunk := range chunks {
		content := strings.TrimSpace(chunk.Content)
		if content == "" {
			continue
		}
		role := strings.TrimSpace(chunk.Role)
		if role == "" {
			role = valueUnknown83F7FCF3
		}
		chunkIndex := chunk.ChunkIndex
		if chunkIndex <= 0 {
			chunkIndex = index + 1
		}
		items = append(items, `<msg role="`+xmlEscapeAttr(role)+`" i="`+xmlEscapeAttr(fmt.Sprintf("%d", chunkIndex))+`">`+xmlEscapeText(compactSnippet(content, 300))+`</msg>`)
	}
	return items
}

func buildUserContextPrompt(userRequest string, contextXML userContextXML) string {
	var builder strings.Builder
	builder.WriteString("<ctx>")
	if strings.TrimSpace(contextXML.summary) != "" {
		builder.WriteString("\n")
		builder.WriteString(contextXML.summary)
	}
	if len(contextXML.memory) > 0 {
		builder.WriteString("\n<mems>\n")
		builder.WriteString(strings.Join(contextXML.memory, "\n"))
		builder.WriteString("\n</mems>")
	}
	if len(contextXML.files) > 0 {
		builder.WriteString("\n<files>\n")
		builder.WriteString(strings.Join(contextXML.files, "\n"))
		builder.WriteString("\n</files>")
	}
	if len(contextXML.evidence) > 0 {
		builder.WriteString("\n<evs>\n")
		builder.WriteString(strings.Join(contextXML.evidence, "\n"))
		builder.WriteString("\n</evs>")
	}
	if len(contextXML.rag) > 0 {
		builder.WriteString("\n<rag>\n")
		builder.WriteString(strings.Join(contextXML.rag, "\n"))
		builder.WriteString("\n</rag>")
	}
	if len(contextXML.recall) > 0 {
		builder.WriteString("\n<recall>\n")
		builder.WriteString(strings.Join(contextXML.recall, "\n"))
		builder.WriteString("\n</recall>")
	}
	builder.WriteString("\n</ctx>")

	request := strings.TrimSpace(userRequest)
	if request != "" {
		builder.WriteString("\n\n<q>")
		builder.WriteString(xmlEscapeText(request))
		builder.WriteString("</q>")
	}
	return builder.String()
}

var xmlTextReplacer = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
)

func xmlEscapeAttr(value string) string {
	var builder strings.Builder
	if err := xml.EscapeText(&builder, []byte(value)); err != nil {
		return ""
	}
	return builder.String()
}

func xmlEscapeText(value string) string {
	return xmlTextReplacer.Replace(value)
}

// filterMemoriesByScope 按 scope 过滤记忆列表。
func filterMemoriesByScope(memories []MemoryItem, scopes ...string) []MemoryItem {
	scopeSet := make(map[string]struct{}, len(scopes))
	for _, s := range scopes {
		scopeSet[s] = struct{}{}
	}
	result := make([]MemoryItem, 0, len(memories))
	for _, m := range memories {
		if _, ok := scopeSet[m.Scope]; ok {
			result = append(result, m)
		}
	}
	return result
}

// selectRelevantMemories 从记忆列表中按关键词相关性选出最多 topK 条。
// 无向量服务时使用关键词匹配作为后备策略：key 或 value 命中查询词即认为相关。
func selectRelevantMemories(memories []MemoryItem, query string, topK int) []MemoryItem {
	if len(memories) == 0 || topK <= 0 {
		return nil
	}
	if len(memories) <= topK {
		return memories
	}

	// 查询词命中的记忆优先注入上下文，降低无关长期记忆对回答的干扰。
	queryLower := strings.ToLower(strings.TrimSpace(query))
	words := strings.Fields(queryLower)

	items := scoreRelevantMemories(memories, queryLower, words)

	// 按分数降序，保持同分时原始顺序（stable）
	sort.SliceStable(items, func(i, j int) bool { return items[i].score > items[j].score })

	result := make([]MemoryItem, 0, topK)
	for i := 0; i < topK && i < len(items); i++ {
		result = append(result, items[i].m)
	}
	return result
}

type scoredMemory struct {
	m     MemoryItem
	score int
}

func scoreRelevantMemories(memories []MemoryItem, queryLower string, words []string) []scoredMemory {
	items := make([]scoredMemory, 0, len(memories))
	for _, memory := range memories {
		items = append(items, scoredMemory{m: memory, score: memoryKeywordScore(memory, queryLower, words)})
	}
	return items
}

func memoryKeywordScore(memory MemoryItem, queryLower string, words []string) int {
	if queryLower == "" || len(words) == 0 {
		return 0
	}
	combined := strings.ToLower(memory.MemoryKey + " " + memory.Value)
	score := 0
	for _, word := range words {
		if len(word) >= 2 && strings.Contains(combined, word) {
			score++
		}
	}
	return score
}

// selectRelevantUserMemories 优先使用记忆向量检索；不可用或超时后回退到关键词筛选。
func (s *Engine) selectRelevantUserMemories(ctx context.Context, actor domainconversation.ActorRef, query string, memories []MemoryItem, topK int) []MemoryItem {
	fallback := selectRelevantMemories(memories, query, topK)
	if !s.canSearchUserMemories(query) {
		return fallback
	}
	cfg := s.cfg.Snapshot()
	if !cfg.Retrieval.EmbeddingEnabled {
		return fallback
	}
	searchCtx, cancel := context.WithTimeout(ctx, semanticRecallDeadline)
	defer cancel()
	embeddings, err := s.embeddingSvc.EmbedTexts(searchCtx, []string{query})
	if err != nil || len(embeddings) == 0 {
		return fallback
	}
	matches, err := s.memoryRecorder.SearchUserMemoriesByEmbedding(searchCtx, actor, embeddings[0], topK, 0.7)
	if err != nil || len(matches) == 0 {
		return fallback
	}

	result := userMemorySearchResults(memories, matches, topK)
	if len(result) == 0 {
		return fallback
	}
	return result
}

func (s *Engine) canSearchUserMemories(query string) bool {
	return s != nil && s.embeddingSvc != nil && s.memoryRecorder != nil && strings.TrimSpace(query) != ""
}

func userMemorySearchResults(memories []MemoryItem, matches []MemoryItem, topK int) []MemoryItem {
	allowed := allowedUserMemories(memories)
	result := make([]MemoryItem, 0, topK)
	seen := make(map[string]struct{}, topK)
	for _, memory := range matches {
		item, key, ok := allowedUserMemoryMatch(allowed, seen, memory)
		if !ok {
			continue
		}
		result = append(result, item)
		seen[key] = struct{}{}
		if len(result) >= topK {
			break
		}
	}
	return result
}

func allowedUserMemories(memories []MemoryItem) map[string]MemoryItem {
	allowed := make(map[string]MemoryItem, len(memories))
	for _, memory := range memories {
		key := strings.TrimSpace(memory.MemoryKey)
		if key != "" {
			allowed[key] = memory
		}
	}
	return allowed
}

func allowedUserMemoryMatch(
	allowed map[string]MemoryItem,
	seen map[string]struct{},
	memory MemoryItem,
) (MemoryItem, string, bool) {
	key := strings.TrimSpace(memory.MemoryKey)
	item, ok := allowed[key]
	if !ok {
		return MemoryItem{}, "", false
	}
	if _, exists := seen[key]; exists {
		return MemoryItem{}, "", false
	}
	return item, key, true
}

// buildPreferencePrompt 将 scope=preference 的记忆格式化为行为指令型 system 提示。
func buildPreferencePrompt(memories []MemoryItem, maxTokens int) string {
	if len(memories) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("# prefs\n")
	tokenCount := estimateTokens(sb.String())
	for _, m := range memories {
		line := "- " + strings.TrimSpace(m.MemoryKey) + ": " + strings.TrimSpace(m.Value) + "\n"
		lineTokens := estimateTokens(line)
		if int(tokenCount)+int(lineTokens) > maxTokens {
			break
		}
		sb.WriteString(line)
		tokenCount += lineTokens
	}
	return strings.TrimRight(sb.String(), "\n")
}

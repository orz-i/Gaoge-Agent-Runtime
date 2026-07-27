package agentruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueAutoB2727A9E          = "auto"
	valueBlockEDF40B57         = "block"
	valueChunkCount2D53A94A    = "chunk_count"
	valueChunkIndexB1785D2D    = "chunk_index"
	valueCompletedEC0C83B1     = "completed"
	valueError60DF354B         = "error"
	valueFileCountC837E95C     = "file_count"
	valueFileId3BB37534        = "file_id"
	valueFileNames650D881C     = "file_names"
	valueFromTurn87CA3596      = "from_turn"
	valueImage6D31EE93         = "image"
	valueItemsFD393ECE         = "items"
	valueKindCBB3405C          = "kind"
	valueName35CE2796          = "name"
	valueOutputDetail4C8D1609  = "output_detail"
	valueQuery7EE5FF94         = "query"
	valueQueued3AF6D5E6        = "queued"
	valueSourceTokens0CC008D0  = "source_tokens"
	valueStatus2F783DF0        = "status"
	valueStrategy89403E82      = "strategy"
	valueSummaryTokens8DC5EF0F = "summary_tokens"
	valueThinkingFD36601C      = "thinking"
	valueToTurn8766AA60        = "to_turn"
	valueTool8DD31B97          = "tool"
	valueToolCallIdE22B7882    = "tool_call_id"
	valueToolCalls1EB6FB6D     = "tool_calls"
	valueTrace88AB275D         = "trace"
	valueTypeEA47FB2C          = "type"
	valueUnknown7E61D539       = "unknown"
)

const (
	messageTraceTypeProcess        = "process"
	messageTraceTypeTools          = "tools"
	messageTraceTypeUpstreamThink  = "upstream_think"
	messageTraceStageProcess       = "process"
	messageTraceStageThink         = "think"
	messageTraceStageTool          = valueTool8DD31B97
	messageTraceStatusStreaming    = "streaming"
	messageTraceStatusCompleted    = valueCompletedEC0C83B1
	messageTraceStatusError        = valueError60DF354B
	messageTraceThinkKindSummary   = "summary_text"
	messageTraceThinkKindContent   = "content_text"
	messageTraceThinkKindSignature = "signature"
)

const (
	processTracePayloadStage        = "trace_stage"
	processTracePayloadStages       = "trace_stages"
	processTraceKindFileContext     = "file_context"
	processTraceKindRetrieval       = "content_retrieval"
	processTraceStatusReady         = "ready"
	processTraceStatusCompleted     = valueCompletedEC0C83B1
	processTraceStatusIncomplete    = "incomplete"
	processTraceStatusEmpty         = "empty"
	processTraceStatusLowScore      = "low_score"
	processTraceStatusSkipped       = "skipped"
	processTraceFallbackFullText    = "full_text"
	processTraceFallbackUnavailable = "unavailable"
)

type messageTraceDraft struct {
	traceType       string
	eventID         string
	eventType       string
	eventSeq        int
	stage           string
	roundID         string
	parentEventID   string
	status          string
	title           string
	summary         string
	contentMarkdown string
	payload         map[string]interface{}
	seq             int
	startedAt       time.Time
	endedAt         *time.Time
}

type messageTraceRecorder struct {
	service       *Engine
	ctx           context.Context
	cfg           Config
	assistant     *ContextMessage
	onEvent       func(string, map[string]interface{}) error
	process       *messageTraceDraft
	tools         *messageTraceDraft
	upstreamThink *messageTraceDraft
	promptTrace   *MessagePromptTrace
	nextEventSeq  int
	nextRoundSeq  int
	eventCounters map[string]int
	events        []MessageTraceEvent
}

func formatTraceStep(label string, detail string) string {
	label = strings.TrimSpace(label)
	detail = strings.TrimSpace(detail)
	if label == "" && detail == "" {
		return ""
	}
	if label == "" {
		return detail
	}
	if detail == "" {
		return fmt.Sprintf("**%s**", label)
	}
	return fmt.Sprintf("**%s**：%s", label, detail)
}

func traceNameScope(names []string) string {
	cleaned := make([]string, 0, len(names))
	for _, name := range names {
		value := strings.TrimSpace(name)
		if value != "" {
			cleaned = append(cleaned, value)
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	if len(cleaned) <= 3 {
		return "（" + strings.Join(cleaned, "、") + "）"
	}
	return fmt.Sprintf("（%s 等 %d 个）", strings.Join(cleaned[:2], "、"), len(cleaned))
}

func (r *messageTraceRecorder) enabled() bool {
	return r != nil && r.cfg.Trace.Enabled && r.assistant != nil
}

func (r *messageTraceRecorder) visible() bool {
	return r.enabled() && r.cfg.Trace.VisibleToUser
}

func (r *messageTraceRecorder) ensureDraft(traceType string) *messageTraceDraft {
	if !r.enabled() {
		return nil
	}
	switch traceType {
	case messageTraceTypeProcess:
		return r.ensureProcessDraft(traceType)
	case messageTraceTypeUpstreamThink:
		return r.ensureUpstreamThinkDraft(traceType)
	case messageTraceTypeTools:
		return r.ensureToolsDraft(traceType)
	default:
		return nil
	}
}

func (r *messageTraceRecorder) ensureProcessDraft(traceType string) *messageTraceDraft {
	if r.process == nil {
		r.process = r.newTraceDraft(traceType, "process", "处理", 1, messageTraceStageProcess, "process", "")
	}
	return r.process
}

func (r *messageTraceRecorder) ensureUpstreamThinkDraft(traceType string) *messageTraceDraft {
	if !r.cfg.Trace.StoreUpstreamThink {
		return nil
	}
	if r.upstreamThink == nil || traceDraftTerminal(r.upstreamThink) {
		r.upstreamThink = r.newTraceDraft(traceType, "think", "模型思考", 3, messageTraceStageThink, r.nextTraceRoundID(), "")
	}
	return r.upstreamThink
}

func traceDraftTerminal(draft *messageTraceDraft) bool {
	return draft.status == messageTraceStatusCompleted || draft.status == messageTraceStatusError
}

func (r *messageTraceRecorder) ensureToolsDraft(traceType string) *messageTraceDraft {
	if r.tools == nil {
		r.tools = &messageTraceDraft{
			traceType: traceType,
			eventType: valueTool8DD31B97,
			stage:     messageTraceStageTool,
			status:    messageTraceStatusStreaming,
			title:     "工具",
			seq:       2,
			startedAt: time.Now(),
			payload:   make(map[string]interface{}),
		}
	}
	return r.tools
}

func (r *messageTraceRecorder) newTraceDraft(traceType string, eventType string, title string, blockSeq int, stage string, roundID string, parentEventID string) *messageTraceDraft {
	eventID, eventSeq := r.nextTraceEventIdentity(traceType)
	return &messageTraceDraft{
		traceType:     traceType,
		eventID:       eventID,
		eventType:     eventType,
		eventSeq:      eventSeq,
		stage:         stage,
		roundID:       strings.TrimSpace(roundID),
		parentEventID: strings.TrimSpace(parentEventID),
		status:        messageTraceStatusStreaming,
		title:         title,
		seq:           blockSeq,
		startedAt:     time.Now(),
		payload:       make(map[string]interface{}),
	}
}

func (r *messageTraceRecorder) nextTraceRoundID() string {
	r.nextRoundSeq++
	return fmt.Sprintf("round_%d", r.nextRoundSeq)
}

func (r *messageTraceRecorder) nextTraceEventIdentity(traceType string) (string, int) {
	if r.eventCounters == nil {
		r.eventCounters = make(map[string]int)
	}
	r.eventCounters[traceType]++
	if r.nextEventSeq <= 0 {
		r.nextEventSeq = 1
	} else {
		r.nextEventSeq++
	}
	return fmt.Sprintf("%s_%d", traceType, r.eventCounters[traceType]), r.nextEventSeq
}

func (r *messageTraceRecorder) appendProcessSection(summary string, markdown string, payload map[string]interface{}) {
	if !r.enabled() {
		return
	}
	value := strings.TrimSpace(markdown)
	if value == "" {
		return
	}
	draft := r.ensureDraft(messageTraceTypeProcess)
	if draft == nil {
		return
	}
	if draft.contentMarkdown != "" {
		draft.contentMarkdown += "\n\n"
	}
	draft.contentMarkdown += value
	if strings.TrimSpace(summary) != "" {
		draft.summary = strings.TrimSpace(summary)
	}
	draft.status = messageTraceStatusStreaming
	draft.endedAt = nil
	mergeTracePayload(draft.payload, payload)
	r.persistDraft(draft, false)
	r.emitProcessUpdate()
}

// recordPromptTrace 把 PromptPlan 摘要合并进处理轨迹，供前端结构化展示。
func (r *messageTraceRecorder) recordPromptTrace(trace *MessagePromptTrace) {
	if !r.enabled() || trace == nil {
		return
	}
	draft := r.ensureDraft(messageTraceTypeProcess)
	if draft == nil {
		return
	}
	r.promptTrace = cloneMessagePromptTrace(trace)
	if draft.payload == nil {
		draft.payload = make(map[string]interface{})
	}
	draft.payload["prompt_trace"] = messagePromptTracePayload(trace)
	if strings.TrimSpace(draft.summary) == "" {
		draft.summary = buildPromptTraceSummary(trace)
	}
	draft.status = messageTraceStatusStreaming
	draft.endedAt = nil
	r.persistDraft(draft, false)
	r.emitProcessUpdate()
}

func (r *messageTraceRecorder) completeDraft(draft *messageTraceDraft) bool {
	if !r.enabled() || draft == nil || draft.status == messageTraceStatusCompleted || draft.status == messageTraceStatusError {
		return false
	}
	now := time.Now()
	draft.status = messageTraceStatusCompleted
	draft.endedAt = &now
	if draft.traceType != messageTraceTypeTools {
		r.upsertSnapshotEvent(draft, tracePayloadJSON(draft.payload))
	}
	if r.service != nil && r.service.repo != nil {
		go r.persistDraftBackground(cloneTraceDraft(draft))
	}
	return true
}

func (r *messageTraceRecorder) completeProcess() {
	if r.completeDraft(r.process) {
		r.emitProcessUpdate()
	}
}

func (r *messageTraceRecorder) completeTools() {
	if r.completeDraft(r.tools) {
		r.emitToolUpdate()
	}
}

func (r *messageTraceRecorder) completeUpstreamThink() {
	if r.completeDraft(r.upstreamThink) {
		r.emitUpstreamThinkDelta(nil)
	}
}

func (r *messageTraceRecorder) complete() {
	r.completeProcess()
	r.completeTools()
	r.completeUpstreamThink()
}

func (r *messageTraceRecorder) snapshot() *MessageProcessTrace {
	if !r.visible() {
		return nil
	}
	process := traceDraftToBlock(r.process)
	tools := traceDraftToBlock(r.tools)
	upstreamThink := traceDraftToBlock(r.upstreamThink)
	if process == nil && tools == nil && upstreamThink == nil && len(r.events) == 0 {
		return nil
	}
	return &MessageProcessTrace{
		Enabled:       true,
		Status:        aggregateTraceStatus(r.process, r.tools, r.upstreamThink),
		Process:       process,
		Tools:         tools,
		UpstreamThink: upstreamThink,
		PromptTrace:   cloneMessagePromptTrace(r.promptTrace),
		Events:        append([]MessageTraceEvent(nil), r.events...),
	}
}

func (r *messageTraceRecorder) persistDraft(draft *messageTraceDraft, force bool) {
	r.persistDraftCtx(r.ctx, draft, force)
}

func cloneTraceDraft(draft *messageTraceDraft) *messageTraceDraft {
	if draft == nil {
		return nil
	}
	cloned := *draft
	if draft.payload != nil {
		cloned.payload = make(map[string]interface{}, len(draft.payload))
		for key, value := range draft.payload {
			cloned.payload[key] = value
		}
	}
	return &cloned
}

// persistDraftBackground 使用独立的 background context 持久化 trace，
// 专供 complete() 的异步 goroutine 调用，避免请求 context 取消后写入失败。
func (r *messageTraceRecorder) persistDraftBackground(draft *messageTraceDraft) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !r.enabled() || draft == nil {
		return
	}
	payloadJSON := tracePayloadJSON(draft.payload)
	r.persistMessageTraceRow(ctx, draft, payloadJSON)
	if draft.traceType != messageTraceTypeTools {
		r.persistTraceEventRow(ctx, draft, payloadJSON)
	}
}

func (r *messageTraceRecorder) persistDraftCtx(ctx context.Context, draft *messageTraceDraft, force bool) {
	if !r.enabled() || draft == nil {
		return
	}
	payloadJSON := tracePayloadJSON(draft.payload)
	if draft.traceType != messageTraceTypeTools {
		r.upsertSnapshotEvent(draft, payloadJSON)
	}
	if !force && !r.cfg.Trace.PersistInflight {
		return
	}
	r.persistMessageTraceRow(ctx, draft, payloadJSON)
	if draft.traceType != messageTraceTypeTools {
		r.persistTraceEventRow(ctx, draft, payloadJSON)
	}
}

func (r *messageTraceRecorder) persistMessageTraceRow(ctx context.Context, draft *messageTraceDraft, payloadJSON string) {
	_ = ctx
	_ = draft
	_ = payloadJSON
}

func tracePayloadJSON(payload map[string]interface{}) string {
	if len(payload) == 0 {
		return "{}"
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func (r *messageTraceRecorder) persistTraceEventRow(ctx context.Context, draft *messageTraceDraft, payloadJSON string) {
	_ = ctx
	_ = draft
	_ = payloadJSON
}

func (r *messageTraceRecorder) upsertSnapshotEvent(draft *messageTraceDraft, payloadJSON string) {
	event := MessageTraceEvent{
		EventID:         draft.eventID,
		EventType:       draft.eventType,
		Phase:           draft.traceType,
		Stage:           draft.stage,
		RoundID:         draft.roundID,
		ParentEventID:   draft.parentEventID,
		Title:           draft.title,
		Summary:         truncateError(strings.TrimSpace(draft.summary)),
		ContentMarkdown: draft.contentMarkdown,
		Status:          draft.status,
		Seq:             draft.eventSeq,
		StartedAt:       draft.startedAt,
		EndedAt:         draft.endedAt,
		UpdatedAt:       time.Now(),
		PayloadJSON:     payloadJSON,
	}
	for idx, item := range r.events {
		if item.EventID == event.EventID {
			r.events[idx] = event
			return
		}
	}
	r.events = append(r.events, event)
}

func (r *messageTraceRecorder) emitProcessUpdate() {
	if !r.visible() || r.process == nil {
		return
	}
	emitEvent(r.onEvent, "process_update", map[string]interface{}{
		valueStatus2F783DF0: r.process.status,
		valueBlockEDF40B57:  traceDraftToBlock(r.process),
		valueTrace88AB275D:  r.snapshot(),
	})
}

func (r *messageTraceRecorder) emitToolUpdate() {
	if !r.visible() || r.tools == nil {
		return
	}
	emitEvent(r.onEvent, "process_update", map[string]interface{}{
		valueStatus2F783DF0: r.tools.status,
		valueBlockEDF40B57:  traceDraftToBlock(r.tools),
		valueTrace88AB275D:  r.snapshot(),
	})
}

func (r *messageTraceRecorder) emitUpstreamThinkDelta(reasoning map[string]interface{}) {
	if !r.visible() || r.upstreamThink == nil {
		return
	}
	payload := map[string]interface{}{
		valueStatus2F783DF0: r.upstreamThink.status,
		valueBlockEDF40B57:  traceDraftToBlock(r.upstreamThink),
		valueTrace88AB275D:  r.snapshot(),
	}
	if len(reasoning) > 0 {
		payload["reasoning"] = reasoning
	}
	emitEvent(r.onEvent, "upstream_think_delta", payload)
}

func traceDraftToBlock(draft *messageTraceDraft) *MessageTraceBlock {
	if draft == nil {
		return nil
	}
	if strings.TrimSpace(draft.contentMarkdown) == "" && strings.TrimSpace(draft.summary) == "" {
		return nil
	}
	updatedAt := draft.startedAt
	if draft.endedAt != nil {
		updatedAt = *draft.endedAt
	}
	var payloadJSON string
	if len(draft.payload) > 0 {
		if raw, err := json.Marshal(draft.payload); err == nil {
			payloadJSON = string(raw)
		}
	}
	return &MessageTraceBlock{
		Title:           draft.title,
		Summary:         draft.summary,
		ContentMarkdown: draft.contentMarkdown,
		Status:          draft.status,
		Stage:           draft.stage,
		RoundID:         draft.roundID,
		ParentEventID:   draft.parentEventID,
		UpdatedAt:       updatedAt,
		PayloadJSON:     payloadJSON,
	}
}

func aggregateTraceStatus(drafts ...*messageTraceDraft) string {
	hasStreaming := false
	hasCompleted := false
	for _, draft := range drafts {
		if draft == nil {
			continue
		}
		switch draft.status {
		case messageTraceStatusError:
			return messageTraceStatusError
		case messageTraceStatusStreaming:
			hasStreaming = true
		case messageTraceStatusCompleted:
			hasCompleted = true
		}
	}
	if hasStreaming {
		return messageTraceStatusStreaming
	}
	if hasCompleted {
		return messageTraceStatusCompleted
	}
	return ""
}

func mergeTracePayload(dst map[string]interface{}, src map[string]interface{}) {
	if dst == nil || len(src) == 0 {
		return
	}
	for key, value := range src {
		mergeTracePayloadField(dst, key, value)
	}
}

func mergeTracePayloadField(dst map[string]interface{}, key string, value interface{}) {
	switch key {
	case processTracePayloadStage:
		appendProcessTraceStagePayload(dst, value)
	case processTracePayloadStages:
		appendProcessTraceStagePayloads(dst, value)
	case valueToolCalls1EB6FB6D:
		existing, existingOK := dst[key].([]map[string]interface{})
		incoming, incomingOK := value.([]map[string]interface{})
		if existingOK && incomingOK {
			dst[key] = append(existing, incoming...)
			return
		}
		dst[key] = value
	default:
		dst[key] = value
	}
}

func appendProcessTraceStagePayload(dst map[string]interface{}, value interface{}) {
	stage, ok := value.(map[string]interface{})
	if !ok || len(stage) == 0 {
		return
	}
	existing := normalizeProcessTraceStagePayloads(dst[processTracePayloadStages])
	dst[processTracePayloadStages] = append(existing, stage)
}

func appendProcessTraceStagePayloads(dst map[string]interface{}, value interface{}) {
	stages := normalizeProcessTraceStagePayloads(value)
	if len(stages) == 0 {
		return
	}
	existing := normalizeProcessTraceStagePayloads(dst[processTracePayloadStages])
	dst[processTracePayloadStages] = append(existing, stages...)
}

func normalizeProcessTraceStagePayloads(value interface{}) []map[string]interface{} {
	switch items := value.(type) {
	case []map[string]interface{}:
		return append([]map[string]interface{}{}, items...)
	case []interface{}:
		result := make([]map[string]interface{}, 0, len(items))
		for _, item := range items {
			if stage, ok := item.(map[string]interface{}); ok && len(stage) > 0 {
				result = append(result, stage)
			}
		}
		return result
	default:
		return nil
	}
}

type attachmentTraceFileRef struct {
	FileID      string `json:"file_id"`
	FileName    string `json:"file_name"`
	Kind        string `json:"kind"`
	MimeType    string `json:"mime_type"`
	ContextMode string `json:"context_mode"`
}

type attachmentTraceFileGroups struct {
	DirectImages []string `json:"direct_images"`
	Adaptive     []string `json:"adaptive"`
	Retrieval    []string `json:"retrieval"`
	FullContext  []string `json:"full_context"`
	Skipped      []string `json:"skipped"`
}

type attachmentTraceRefGroups struct {
	DirectImages []attachmentTraceFileRef `json:"direct_images"`
	Adaptive     []attachmentTraceFileRef `json:"adaptive"`
	Retrieval    []attachmentTraceFileRef `json:"retrieval"`
	FullContext  []attachmentTraceFileRef `json:"full_context"`
	Skipped      []attachmentTraceFileRef `json:"skipped"`
}

type attachmentTracePayload struct {
	FileMode      string                    `json:"file_mode"`
	FileNames     []string                  `json:"file_names"`
	FileRefs      []attachmentTraceFileRef  `json:"file_refs"`
	FileGroups    attachmentTraceFileGroups `json:"file_groups"`
	FileGroupRefs attachmentTraceRefGroups  `json:"file_group_refs"`
}

func buildAttachmentProcessTrace(
	fileMode string,
	attachments []AttachmentInput,
) (string, string, map[string]interface{}) {
	if len(attachments) == 0 {
		return "", "", nil
	}

	payload := attachmentTracePayload{
		FileMode:  strings.TrimSpace(fileMode),
		FileNames: make([]string, 0, len(attachments)),
		FileRefs:  make([]attachmentTraceFileRef, 0, len(attachments)),
	}
	for _, item := range attachments {
		name := strings.TrimSpace(item.FileName)
		if name == "" {
			name = strings.TrimSpace(item.FileID)
		}
		payload.FileNames = append(payload.FileNames, name)
		ref := newAttachmentTraceFileRef(item, name)
		payload.FileRefs = append(payload.FileRefs, ref)
		addAttachmentTracePayloadItem(&payload, item, name, ref)
	}
	includedCount := len(attachments) - len(payload.FileGroups.Skipped)
	skippedCount := len(payload.FileGroups.Skipped)
	summary := formatAttachmentProcessCounts(includedCount, skippedCount, "已纳入")
	detail := fmt.Sprintf("文件已就绪，%s。", formatAttachmentProcessCounts(includedCount, skippedCount, "纳入"))
	return summary, formatTraceStep("文件上下文", detail), attachmentTracePayloadMap(payload)
}

func addAttachmentTracePayloadItem(payload *attachmentTracePayload, item AttachmentInput, name string, ref attachmentTraceFileRef) {
	if normalizeAttachmentKind(item.Kind, item.MimeType) == valueImage6D31EE93 {
		payload.FileGroups.DirectImages = append(payload.FileGroups.DirectImages, name)
		payload.FileGroupRefs.DirectImages = append(payload.FileGroupRefs.DirectImages, ref)
		return
	}
	switch item.ContextMode {
	case fileContextModeRAG:
		payload.FileGroups.Retrieval = append(payload.FileGroups.Retrieval, name)
		payload.FileGroupRefs.Retrieval = append(payload.FileGroupRefs.Retrieval, ref)
	case fileContextModeRAGFallback:
		payload.FileGroups.FullContext = append(payload.FileGroups.FullContext, name)
		payload.FileGroupRefs.FullContext = append(payload.FileGroupRefs.FullContext, ref)
	case fileContextModeSkipped:
		payload.FileGroups.Skipped = append(payload.FileGroups.Skipped, name)
		payload.FileGroupRefs.Skipped = append(payload.FileGroupRefs.Skipped, ref)
	case fileContextModeFull:
		addFullContextAttachmentTracePayloadItem(payload, name, ref)
	default:
		payload.FileGroups.FullContext = append(payload.FileGroups.FullContext, name)
		payload.FileGroupRefs.FullContext = append(payload.FileGroupRefs.FullContext, ref)
	}
}

func addFullContextAttachmentTracePayloadItem(payload *attachmentTracePayload, name string, ref attachmentTraceFileRef) {
	if payload.FileMode == valueAutoB2727A9E {
		payload.FileGroups.Adaptive = append(payload.FileGroups.Adaptive, name)
		payload.FileGroupRefs.Adaptive = append(payload.FileGroupRefs.Adaptive, ref)
		return
	}
	payload.FileGroups.FullContext = append(payload.FileGroups.FullContext, name)
	payload.FileGroupRefs.FullContext = append(payload.FileGroupRefs.FullContext, ref)
}

func formatAttachmentProcessCounts(includedCount int, skippedCount int, includedVerb string) string {
	parts := make([]string, 0, 2)
	if includedCount > 0 || skippedCount == 0 {
		parts = append(parts, fmt.Sprintf("%s %d 个文件", includedVerb, includedCount))
	}
	if skippedCount > 0 {
		parts = append(parts, fmt.Sprintf("未纳入 %d 个文件", skippedCount))
	}
	return strings.Join(parts, "，")
}

func newAttachmentTraceFileRef(item AttachmentInput, fallbackName string) attachmentTraceFileRef {
	name := strings.TrimSpace(item.FileName)
	if name == "" {
		name = strings.TrimSpace(fallbackName)
	}
	if name == "" {
		name = strings.TrimSpace(item.FileID)
	}
	return attachmentTraceFileRef{
		FileID:      strings.TrimSpace(item.FileID),
		FileName:    name,
		Kind:        strings.TrimSpace(item.Kind),
		MimeType:    strings.TrimSpace(item.MimeType),
		ContextMode: strings.TrimSpace(item.ContextMode),
	}
}

func attachmentTracePayloadMap(payload attachmentTracePayload) map[string]interface{} {
	includedCount := len(payload.FileRefs) - len(payload.FileGroupRefs.Skipped)
	if includedCount < 0 {
		includedCount = 0
	}
	return map[string]interface{}{
		"file_mode":            payload.FileMode,
		valueFileNames650D881C: payload.FileNames,
		"file_refs":            payload.FileRefs,
		"file_groups":          payload.FileGroups,
		"file_group_refs":      payload.FileGroupRefs,
		processTracePayloadStage: map[string]interface{}{
			valueKindCBB3405C:   processTraceKindFileContext,
			valueStatus2F783DF0: processTraceStatusReady,
			"included_count":    includedCount,
			"skipped_count":     len(payload.FileGroupRefs.Skipped),
		},
	}
}

func buildRAGProcessTrace(
	query string,
	fileObjs []FileAsset,
	chunks []model.RAGChunk,
) (string, string, map[string]interface{}) {
	if len(fileObjs) == 0 {
		return "", "", nil
	}
	names := make([]string, 0, len(fileObjs))
	for _, item := range fileObjs {
		name := strings.TrimSpace(item.FileName)
		if name == "" {
			name = strings.TrimSpace(item.FileID)
		}
		names = append(names, name)
	}
	citations := make([]map[string]interface{}, 0, len(chunks))
	for _, chunk := range chunks {
		citations = append(citations, map[string]interface{}{
			"file_name":             chunk.FileName,
			valueFileId3BB37534:     chunk.FileID,
			valueChunkIndexB1785D2D: chunk.ChunkIndex,
			"score":                 chunk.Score,
			"preview":               compactSnippet(chunk.Content, 100),
		})
	}
	detail := fmt.Sprintf("检索已完成，共检索 %d 个文件，命中 %d 个段落。", len(names), len(chunks))
	return fmt.Sprintf("检索到 %d 段相关内容", len(chunks)), formatTraceStep("内容检索", detail), map[string]interface{}{
		valueQuery7EE5FF94:     compactSnippet(query, 240),
		valueFileNames650D881C: names,
		"hit_chunk_count":      len(chunks),
		"citations":            citations,
		processTracePayloadStage: map[string]interface{}{
			valueKindCBB3405C:       processTraceKindRetrieval,
			valueStatus2F783DF0:     processTraceStatusCompleted,
			valueFileCountC837E95C:  len(names),
			valueChunkCount2D53A94A: len(chunks),
		},
	}
}

func buildPromptTraceSummary(trace *MessagePromptTrace) string {
	if trace == nil {
		return ""
	}
	if trace.StatefulUsed {
		return fmt.Sprintf("续接发送 %d 条消息", trace.SentMessageCount)
	}
	return fmt.Sprintf("准备 %d tokens 上下文", trace.SentTokenEstimate)
}

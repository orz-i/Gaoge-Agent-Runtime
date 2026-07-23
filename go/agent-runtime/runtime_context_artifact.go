// Package agentruntime owns Agent Runtime use cases and policy.
package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	domainconversation "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueMcp75675BED = "mcp"
)

const (
	valueChunkCount3A7B8453    = "chunk_count"
	valueChunkIndex57F6D050    = "chunk_index"
	valueError9B7383E5         = "error"
	valueFailedF9AB515B        = "failed"
	valueFile65279AC8          = "file"
	valueFileId2C739CF1        = "file_id"
	valueFromTurn04D064E1      = "from_turn"
	valueFunctionA9DD1819      = "function"
	valueInput155D06F3         = "input"
	valueQueryE4581591         = "query"
	valueReasonB02F17C7        = "reason"
	valueSourceTokens70418271  = "source_tokens"
	valueStatusA4C72E03        = "status"
	valueStrategy66C6BD9B      = "strategy"
	valueSummaryTokens137CDE82 = "summary_tokens"
	valueToTurn80048439        = "to_turn"
	valueToolCallId898CDCFE    = "tool_call_id"
)

const (
	contextArtifactExcerptChars       = 2000
	historicalArtifactScanLimit       = 30
	historicalArtifactDefaultMaxItems = 5
	historicalArtifactDefaultMaxToken = 1200
)

type promptContextArtifactInput struct {
	Actor        domainconversation.ActorRef
	Thread       domainconversation.ThreadRef
	Projection   domainconversation.ProjectionRef
	RunID        string
	Query        string
	RAGChunks    []domainconversation.RAGChunk
	RAGFallbacks []ragFallbackEvidence
	RecallChunks []domainconversation.RecallChunk
	Memories     []MemoryItem
}

type toolContextArtifactInput struct {
	Actor      domainconversation.ActorRef
	Thread     domainconversation.ThreadRef
	Projection domainconversation.ProjectionRef
	RunID      string
	Rows       []domainconversation.ToolRecord
}

type snapshotContextArtifactInput struct {
	Actor      domainconversation.ActorRef
	Thread     domainconversation.ThreadRef
	Projection domainconversation.ProjectionRef
	RunID      string
	Snapshot   *ThreadCompaction
}

type historicalContextArtifactInput struct {
	CurrentProjection  string
	HasCurrentSnapshot bool
	CoveredThrough     string
	AllowedProjections map[string]struct{}
	Query              string
	Candidates         []domainconversation.ContextArtifact
	CurrentRAGChunks   []domainconversation.RAGChunk
	CurrentFallbacks   []AttachmentInput
	CurrentRecall      []domainconversation.RecallChunk
	MaxItems           int
	MaxTokens          int64
}

type historicalScoredArtifact struct {
	item  domainconversation.ContextArtifact
	score int
	index int
}

type ragFallbackEvidence struct {
	Attachment AttachmentInput
	Reason     string
	Error      string
}

// GetContextArtifact 查询当前用户可访问的上下文证据详情。
func (s *Engine) GetContextArtifact(ctx context.Context, actor domainconversation.ActorRef, artifactID string) (*domainconversation.ContextArtifact, error) {
	if strings.TrimSpace(artifactID) == "" {
		return nil, ErrContextArtifactNotFound
	}
	item, err := s.repo.GetContextArtifact(ctx, actor, artifactID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrContextArtifactNotFound
		}
		return nil, err
	}
	return item, nil
}

// persistSnapshotContextArtifact 保存压缩摘要证据，供未来 PromptPlan 解释和召回。
func (s *Engine) persistSnapshotContextArtifact(ctx context.Context, input snapshotContextArtifactInput) {
	item := buildSnapshotContextArtifact(input)
	if item == nil {
		return
	}
	items := []domainconversation.ContextArtifact{*item}
	s.applyContextArtifactRetention(items)
	if err := s.repo.CreateContextArtifacts(ctx, items); err != nil && s.logger != nil {
		s.logger.Warn("snapshot_context_artifact_persist_failed", String("trace_id", s.traceID(ctx)), String("thread_id", input.Thread.ID), String("projection_id", input.Projection.ID), Error(err))
	}
}

// applyContextArtifactRetention 给新证据写入过期时间；过期策略只影响证据表，不影响原始消息。
func (s *Engine) applyContextArtifactRetention(items []domainconversation.ContextArtifact) {
	if len(items) == 0 || s == nil || s.cfg == nil {
		return
	}
	days := s.cfg.Snapshot().Retention.ContextArtifactDays
	if days <= 0 {
		return
	}
	expiresAt := time.Now().Add(time.Duration(days) * 24 * time.Hour)
	for index := range items {
		items[index].ExpiresAt = &expiresAt
	}
}

// recallHistoricalContextArtifacts 读取近期上下文证据并按当前问题筛选。
func (s *Engine) recallHistoricalContextArtifacts(
	ctx context.Context,
	actor domainconversation.ActorRef,
	thread domainconversation.ThreadRef,
	currentProjection string,
	hasCurrentSnapshot bool,
	coveredThrough string,
	allowedProjections map[string]struct{},
	query string,
	currentRAGChunks []domainconversation.RAGChunk,
	currentFallbacks []AttachmentInput,
	currentRecall []domainconversation.RecallChunk,
) []domainconversation.ContextArtifact {
	if strings.TrimSpace(query) == "" {
		return nil
	}
	kinds := []domainconversation.ContextArtifactKind{
		domainconversation.ContextArtifactFileRAGChunk,
		domainconversation.ContextArtifactFileRAGFallback,
		domainconversation.ContextArtifactToolResult,
		domainconversation.ContextArtifactNativeTool,
	}
	if !hasCurrentSnapshot {
		kinds = append(kinds, domainconversation.ContextArtifactSummary)
	}
	candidates, err := s.repo.ListRecentContextArtifacts(ctx, actor, thread, historicalArtifactScanLimit)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("historical_context_artifact_recall_failed", String("trace_id", s.traceID(ctx)), String("thread_id", thread.ID), Error(err))
		}
		return nil
	}
	return selectHistoricalContextArtifacts(historicalContextArtifactInput{
		CurrentProjection:  currentProjection,
		HasCurrentSnapshot: hasCurrentSnapshot,
		CoveredThrough:     coveredThrough,
		AllowedProjections: allowedProjections,
		Query:              query,
		Candidates:         candidates,
		CurrentRAGChunks:   currentRAGChunks,
		CurrentFallbacks:   currentFallbacks,
		CurrentRecall:      currentRecall,
	})
}

// buildPromptContextArtifacts 将 RAG、全文回退和语义召回统一转换为上下文证据。
func buildPromptContextArtifacts(input promptContextArtifactInput) []domainconversation.ContextArtifact {
	items := make([]domainconversation.ContextArtifact, 0, len(input.RAGChunks)+len(input.RAGFallbacks)+len(input.RecallChunks)+len(input.Memories))
	for _, chunk := range input.RAGChunks {
		content := strings.TrimSpace(chunk.Content)
		if content == "" {
			continue
		}
		sourceID := fileRAGChunkSourceID(chunk)
		items = append(items, domainconversation.ContextArtifact{
			Projection:    input.Projection,
			RunID:         input.RunID,
			Kind:          domainconversation.ContextArtifactFileRAGChunk,
			SourceType:    "file_chunk",
			SourceID:      sourceID,
			SourceTitle:   strings.TrimSpace(chunk.FileName),
			Content:       contextArtifactExcerpt(content),
			ContentHash:   contextArtifactHash(domainconversation.ContextArtifactFileRAGChunk, sourceID, content),
			TokenEstimate: estimateTokens(content),
			Score:         float64(chunk.Score),
			MetadataJSON: contextArtifactMetadata(map[string]interface{}{
				valueQueryE4581591:      strings.TrimSpace(input.Query),
				valueFileId2C739CF1:     strings.TrimSpace(chunk.FileID),
				valueChunkIndex57F6D050: chunk.ChunkIndex,
				"score":                 chunk.Score,
			}),
		})
	}

	for _, fallback := range input.RAGFallbacks {
		file := fallback.Attachment
		content := strings.TrimSpace(file.ExtractedText)
		if content == "" {
			continue
		}
		sourceID := fallbackFileSourceID(file)
		items = append(items, domainconversation.ContextArtifact{
			Projection:    input.Projection,
			RunID:         input.RunID,
			Kind:          domainconversation.ContextArtifactFileRAGFallback,
			SourceType:    valueFile65279AC8,
			SourceID:      sourceID,
			SourceTitle:   strings.TrimSpace(file.FileName),
			Content:       contextArtifactExcerpt(content),
			ContentHash:   contextArtifactHash(domainconversation.ContextArtifactFileRAGFallback, sourceID, content),
			TokenEstimate: estimateTokens(content),
			MetadataJSON: contextArtifactMetadata(map[string]interface{}{
				valueQueryE4581591:      strings.TrimSpace(input.Query),
				valueReasonB02F17C7:     strings.TrimSpace(fallback.Reason),
				valueError9B7383E5:      strings.TrimSpace(fallback.Error),
				valueFileId2C739CF1:     strings.TrimSpace(file.FileID),
				"sha256":                strings.TrimSpace(file.SHA256),
				valueChunkCount3A7B8453: file.ChunkCount,
				"embed_status":          strings.TrimSpace(file.EmbedStatus),
				"extract_status":        strings.TrimSpace(file.ExtractStatus),
			}),
		})
	}

	for _, memory := range input.Memories {
		key := strings.TrimSpace(memory.MemoryKey)
		content := strings.TrimSpace(memory.Value)
		if key == "" || content == "" {
			continue
		}
		scope := strings.TrimSpace(memory.Scope)
		items = append(items, domainconversation.ContextArtifact{
			Projection:    input.Projection,
			RunID:         input.RunID,
			Kind:          domainconversation.ContextArtifactUserMemory,
			SourceType:    "user_memory",
			SourceID:      key,
			SourceTitle:   firstNonEmptyString(scope, key),
			Content:       contextArtifactExcerpt(content),
			ContentHash:   contextArtifactHash(domainconversation.ContextArtifactUserMemory, key, content),
			TokenEstimate: estimateTokens(content),
			Score:         1,
			MetadataJSON: contextArtifactMetadata(map[string]interface{}{
				"memory_key": strings.TrimSpace(memory.MemoryKey),
				"scope":      scope,
				"updated_by": strings.TrimSpace(memory.UpdatedBy),
			}),
		})
	}

	for _, chunk := range input.RecallChunks {
		content := strings.TrimSpace(chunk.Content)
		if content == "" {
			continue
		}
		sourceID := fmt.Sprintf("%s:%d", chunk.Projection.ID, chunk.ChunkIndex)
		items = append(items, domainconversation.ContextArtifact{
			Projection:    input.Projection,
			RunID:         input.RunID,
			Kind:          domainconversation.ContextArtifactSemanticRecall,
			SourceType:    "message_chunk",
			SourceID:      sourceID,
			SourceTitle:   chunk.Role,
			Content:       contextArtifactExcerpt(content),
			ContentHash:   contextArtifactHash(domainconversation.ContextArtifactSemanticRecall, sourceID, content),
			TokenEstimate: estimateTokens(content),
			Score:         chunk.Similarity,
			MetadataJSON: contextArtifactMetadata(map[string]interface{}{
				"source_message_id":     chunk.Projection.ID,
				valueChunkIndex57F6D050: chunk.ChunkIndex,
				"role":                  strings.TrimSpace(chunk.Role),
				"similarity":            chunk.Similarity,
			}),
		})
	}
	return items
}

// buildToolContextArtifacts 将工具调用结果转换为可召回的上下文证据。
func buildToolContextArtifacts(input toolContextArtifactInput) []domainconversation.ContextArtifact {
	items := make([]domainconversation.ContextArtifact, 0, len(input.Rows))
	for _, row := range input.Rows {
		rawContent := toolArtifactContent(row)
		content, truncated := toolArtifactEvidenceContent(rawContent, contextArtifactExcerptChars)
		if strings.TrimSpace(content) == "" {
			continue
		}
		sourceID := strings.TrimSpace(row.ToolCallID)
		if sourceID == "" {
			sourceID = strings.TrimSpace(row.ToolName)
		}
		kind := toolContextArtifactKind(row)
		items = append(items, domainconversation.ContextArtifact{
			Projection:    input.Projection,
			RunID:         input.RunID,
			Kind:          kind,
			SourceType:    "tool_call",
			SourceID:      sourceID,
			SourceTitle:   strings.TrimSpace(row.ToolName),
			Content:       content,
			ContentHash:   contextArtifactHash(kind, sourceID, rawContent),
			TokenEstimate: estimateTokens(content),
			Score:         1,
			MetadataJSON: contextArtifactMetadata(map[string]interface{}{
				valueToolCallId898CDCFE: strings.TrimSpace(row.ToolCallID),
				"tool_type":             strings.TrimSpace(row.ToolType),
				"tool_name":             strings.TrimSpace(row.ToolName),
				valueStatusA4C72E03:     strings.TrimSpace(row.Status),
				"latency_ms":            row.LatencyMS,
				valueInput155D06F3:      strings.TrimSpace(row.InputJSON),
				"output_chars":          len([]rune(rawContent)),
				"truncated":             truncated,
			}),
		})
	}
	return items
}

// buildSnapshotContextArtifact 将压缩快照转换为历史 evidence。
func buildSnapshotContextArtifact(input snapshotContextArtifactInput) *domainconversation.ContextArtifact {
	if input.Snapshot == nil || strings.TrimSpace(input.Snapshot.Summary) == "" {
		return nil
	}
	sourceID := input.Snapshot.CoveredThrough.ID
	if input.Snapshot.CoveredThrough.ID == "" {
		sourceID = strings.TrimSpace(input.RunID)
	}
	if sourceID == "" {
		sourceID = strings.TrimSpace(input.RunID)
	}
	title := fmt.Sprintf("上下文摘要 %d-%d", input.Snapshot.FromTurn, input.Snapshot.ToTurn)
	content := strings.TrimSpace(input.Snapshot.Summary)
	tokenEstimate := input.Snapshot.SummaryTokens
	if tokenEstimate <= 0 {
		tokenEstimate = estimateTokens(content)
	}
	return &domainconversation.ContextArtifact{
		Projection:    input.Projection,
		RunID:         firstNonEmptyString(input.RunID, input.RunID),
		Kind:          domainconversation.ContextArtifactSummary,
		SourceType:    "context_snapshot",
		SourceID:      sourceID,
		SourceTitle:   title,
		Content:       contextArtifactExcerpt(content),
		ContentHash:   contextArtifactHash(domainconversation.ContextArtifactSummary, sourceID, content),
		TokenEstimate: tokenEstimate,
		Score:         1,
		MetadataJSON: contextArtifactMetadata(map[string]interface{}{
			valueFromTurn04D064E1:      input.Snapshot.FromTurn,
			valueToTurn80048439:        input.Snapshot.ToTurn,
			valueSourceTokens70418271:  input.Snapshot.SourceTokens,
			valueSummaryTokens137CDE82: input.Snapshot.SummaryTokens,
			valueStrategy66C6BD9B:      strings.TrimSpace(input.Snapshot.Strategy),
		}),
	}
}

// selectHistoricalContextArtifacts 从近期证据中选择与当前问题相关的少量历史证据。
func selectHistoricalContextArtifacts(input historicalContextArtifactInput) []domainconversation.ContextArtifact {
	if len(input.Candidates) == 0 {
		return nil
	}
	maxItems, maxTokens := historicalArtifactLimits(input)
	terms := artifactQueryTerms(input.Query)
	followUp := isFollowUpArtifactQuery(input.Query)
	seen := currentArtifactContentFingerprints(input)
	scored := buildHistoricalScoredArtifacts(input, terms, followUp, seen)
	sortHistoricalArtifacts(scored)

	return selectHistoricalArtifactResults(scored, maxItems, maxTokens)
}

func historicalArtifactLimits(input historicalContextArtifactInput) (int, int64) {
	maxItems := input.MaxItems
	if maxItems <= 0 {
		maxItems = historicalArtifactDefaultMaxItems
	}
	maxTokens := input.MaxTokens
	if maxTokens <= 0 {
		maxTokens = historicalArtifactDefaultMaxToken
	}
	return maxItems, maxTokens
}

func buildHistoricalScoredArtifacts(
	input historicalContextArtifactInput,
	terms []string,
	followUp bool,
	seen map[string]struct{},
) []historicalScoredArtifact {
	scored := make([]historicalScoredArtifact, 0, len(input.Candidates))
	for index, item := range input.Candidates {
		candidate, ok := historicalScoredArtifactCandidate(input, item, index, terms, followUp, seen)
		if !ok {
			continue
		}
		scored = append(scored, candidate)
	}
	return scored
}

func historicalScoredArtifactCandidate(
	input historicalContextArtifactInput,
	item domainconversation.ContextArtifact,
	index int,
	terms []string,
	followUp bool,
	seen map[string]struct{},
) (historicalScoredArtifact, bool) {
	content := strings.TrimSpace(item.Content)
	if historicalArtifactDisallowed(input, item, content) {
		return historicalScoredArtifact{}, false
	}
	fingerprint := normalizedContentFingerprint(content)
	if fingerprint == "" {
		return historicalScoredArtifact{}, false
	}
	if _, exists := seen[fingerprint]; exists {
		return historicalScoredArtifact{}, false
	}
	seen[fingerprint] = struct{}{}
	score := scoreHistoricalArtifact(item, terms)
	if score == 0 && followUp {
		score = 1
	}
	if score <= 0 {
		return historicalScoredArtifact{}, false
	}
	return historicalScoredArtifact{item: item, score: score, index: index}, true
}

func historicalArtifactDisallowed(input historicalContextArtifactInput, item domainconversation.ContextArtifact, content string) bool {
	if input.HasCurrentSnapshot && item.Kind == domainconversation.ContextArtifactSummary {
		return true
	}
	if input.CoveredThrough != "" && item.Projection.ID == input.CoveredThrough {
		return true
	}
	if content == "" || item.Projection.ID == input.CurrentProjection {
		return true
	}
	return !historicalArtifactMessageAllowed(input, item)
}

func historicalArtifactMessageAllowed(input historicalContextArtifactInput, item domainconversation.ContextArtifact) bool {
	if len(input.AllowedProjections) == 0 {
		return true
	}
	if item.Projection.ID == "" {
		return false
	}
	_, ok := input.AllowedProjections[item.Projection.ID]
	return ok
}

func selectHistoricalArtifactResults(scored []historicalScoredArtifact, maxItems int, maxTokens int64) []domainconversation.ContextArtifact {
	results := make([]domainconversation.ContextArtifact, 0, maxItems)
	var usedTokens int64
	for _, candidate := range scored {
		item, tokenEstimate := compactHistoricalArtifact(candidate.item)
		if usedTokens+tokenEstimate > maxTokens {
			continue
		}
		results = append(results, item)
		usedTokens += tokenEstimate
		if len(results) >= maxItems {
			break
		}
	}
	return results
}

func compactHistoricalArtifact(item domainconversation.ContextArtifact) (domainconversation.ContextArtifact, int64) {
	content := compactSnippet(strings.TrimSpace(item.Content), 500)
	tokenEstimate := estimateTokens(content)
	if item.TokenEstimate > 0 && item.TokenEstimate < tokenEstimate {
		tokenEstimate = item.TokenEstimate
	}
	if tokenEstimate <= 0 {
		tokenEstimate = 1
	}
	item.Content = content
	item.TokenEstimate = tokenEstimate
	return item, tokenEstimate
}

func contextArtifactHash(kind domainconversation.ContextArtifactKind, sourceID string, content string) string {
	sum := sha256.Sum256([]byte(string(kind) + "\x00" + strings.TrimSpace(sourceID) + "\x00" + content))
	return hex.EncodeToString(sum[:])
}

func contextArtifactMetadata(value map[string]interface{}) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(payload)
}

func contextArtifactExcerpt(content string) string {
	runes := []rune(content)
	if len(runes) <= contextArtifactExcerptChars {
		return content
	}
	return string(runes[:contextArtifactExcerptChars])
}

func fileRAGChunkSourceID(chunk domainconversation.RAGChunk) string {
	if id := strings.TrimSpace(chunk.FileID); id != "" {
		return fmt.Sprintf("%s:%d", id, chunk.ChunkIndex)
	}
	return fmt.Sprintf("%s:%d", strings.TrimSpace(chunk.FileName), chunk.ChunkIndex)
}

func fallbackFileSourceID(file AttachmentInput) string {
	if id := strings.TrimSpace(file.FileID); id != "" {
		return id
	}
	if sha := strings.TrimSpace(file.SHA256); sha != "" {
		return sha
	}
	return strings.TrimSpace(file.FileName)
}

func ragFallbackEvidencesFromAttachments(items []AttachmentInput, reason string, errMessage string) []ragFallbackEvidence {
	if len(items) == 0 {
		return nil
	}
	result := make([]ragFallbackEvidence, 0, len(items))
	for _, item := range items {
		result = append(result, ragFallbackEvidence{
			Attachment: item,
			Reason:     strings.TrimSpace(reason),
			Error:      strings.TrimSpace(errMessage),
		})
	}
	return result
}

func ragFallbackEvidenceAttachments(items []ragFallbackEvidence) []AttachmentInput {
	if len(items) == 0 {
		return nil
	}
	result := make([]AttachmentInput, 0, len(items))
	for _, item := range items {
		result = append(result, item.Attachment)
	}
	return result
}

func toolArtifactContent(row domainconversation.ToolRecord) string {
	switch strings.TrimSpace(row.Status) {
	case valueError9B7383E5, valueFailedF9AB515B:
		return firstNonEmptyString(row.ErrorJSON, row.OutputJSON)
	default:
		return firstNonEmptyString(row.OutputJSON, row.ErrorJSON)
	}
}

func toolArtifactEvidenceContent(raw string, maxChars int) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", false
	}
	if maxChars <= 0 || len([]rune(value)) <= maxChars {
		return value, false
	}
	return headTailContextArtifact(value, maxChars), true
}

func headTailContextArtifact(value string, maxChars int) string {
	runes := []rune(strings.TrimSpace(value))
	if maxChars <= 0 || len(runes) <= maxChars {
		return string(runes)
	}
	separator := fmt.Sprintf("\n\n[... %d chars omitted ...]\n\n", len(runes)-maxChars)
	separatorChars := len([]rune(separator))
	if separatorChars >= maxChars {
		return strings.TrimSpace(string(runes[:maxChars]))
	}
	available := maxChars - separatorChars
	headChars := available / 2
	tailChars := available - headChars
	head := strings.TrimSpace(string(runes[:headChars]))
	tail := strings.TrimSpace(string(runes[len(runes)-tailChars:]))
	return head + separator + tail
}

func toolContextArtifactKind(row domainconversation.ToolRecord) domainconversation.ContextArtifactKind {
	toolType := strings.ToLower(strings.TrimSpace(row.ToolType))
	switch toolType {
	case "", valueFunctionA9DD1819, valueMcp75675BED:
		return domainconversation.ContextArtifactToolResult
	default:
		return domainconversation.ContextArtifactNativeTool
	}
}

func currentArtifactContentFingerprints(input historicalContextArtifactInput) map[string]struct{} {
	seen := make(map[string]struct{})
	for _, chunk := range input.CurrentRAGChunks {
		if fingerprint := normalizedContentFingerprint(chunk.Content); fingerprint != "" {
			seen[fingerprint] = struct{}{}
		}
	}
	for _, file := range input.CurrentFallbacks {
		if fingerprint := normalizedContentFingerprint(file.ExtractedText); fingerprint != "" {
			seen[fingerprint] = struct{}{}
		}
	}
	for _, chunk := range input.CurrentRecall {
		if fingerprint := normalizedContentFingerprint(chunk.Content); fingerprint != "" {
			seen[fingerprint] = struct{}{}
		}
	}
	return seen
}

func normalizedContentFingerprint(content string) string {
	value := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(content)), " "))
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func artifactQueryTerms(query string) []string {
	value := strings.ToLower(strings.TrimSpace(query))
	if value == "" {
		return nil
	}
	seen := make(map[string]struct{})
	add := func(term string) {
		term = strings.TrimSpace(term)
		if len([]rune(term)) < 2 {
			return
		}
		seen[term] = struct{}{}
	}
	for _, field := range strings.Fields(value) {
		add(field)
	}
	runes := []rune(value)
	for i := 0; i+1 < len(runes); i++ {
		if isCJKRune(runes[i]) && isCJKRune(runes[i+1]) {
			add(string(runes[i : i+2]))
		}
	}
	terms := make([]string, 0, len(seen))
	for term := range seen {
		terms = append(terms, term)
	}
	return terms
}

func isFollowUpArtifactQuery(query string) bool {
	value := strings.ToLower(strings.TrimSpace(query))
	if value == "" {
		return false
	}
	markers := []string{"刚才", "上面", "上一", "上个", "之前", "前面", "那个", "这些", "这段", "这份", "这个文件", "继续", "修改", "改短", "展开", "引用", "总结"}
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func scoreHistoricalArtifact(item domainconversation.ContextArtifact, terms []string) int {
	if len(terms) == 0 {
		return 0
	}
	content := strings.ToLower(strings.TrimSpace(item.Content))
	title := strings.ToLower(strings.TrimSpace(item.SourceTitle + " " + item.SourceID))
	score := 0
	for _, term := range terms {
		if strings.Contains(title, term) {
			score += 4
		}
		if strings.Contains(content, term) {
			score++
		}
	}
	if item.Score > 0 {
		score++
	}
	return score
}

func sortHistoricalArtifacts(items []historicalScoredArtifact) {
	for i := 1; i < len(items); i++ {
		key := items[i]
		j := i - 1
		for j >= 0 && historicalArtifactLess(items[j], key) {
			items[j+1] = items[j]
			j--
		}
		items[j+1] = key
	}
}

func historicalArtifactLess(left historicalScoredArtifact, right historicalScoredArtifact) bool {
	if left.score != right.score {
		return left.score < right.score
	}
	return left.index > right.index
}

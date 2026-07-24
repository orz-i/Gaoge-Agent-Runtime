// Package agentruntime owns Agent Runtime use cases and policy.
package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
	"golang.org/x/sync/errgroup"
	"strings"
	"sync"
	"time"
)

const (
	valueStreamingFB700CEB = "streaming"
)

const (
	valueAutoA736B2DB   = "auto"
	valueFileId27747453 = "file_id"
	valueKindEC105AB1   = "kind"
	valueStatus78866AB7 = "status"
)

const (
	contextArtifactCleanupInterval = 24 * time.Hour
	contextArtifactCleanupBatch    = 1000
)

// Start starts Engine-owned workers without taking ownership of injected
// adapters. It may be called exactly once.
func (s *Engine) Start(ctx context.Context) error {
	if s == nil {
		return ErrEngineClosed
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closed {
		return ErrEngineClosed
	}
	if s.started {
		return ErrEngineAlreadyStarted
	}
	workerCtx, cancel := context.WithCancel(ctx)
	s.workerCancel = cancel
	s.started = true
	s.startWorker(workerCtx, s.startInMemoryCacheCleanupWorker)
	s.startWorker(workerCtx, s.startContextArtifactCleanupWorker)
	s.startContinuationWorkers(workerCtx)
	s.startWorker(workerCtx, s.startTextRunReconciliation)
	s.startWorker(workerCtx, s.startRunQueueDispatcher)
	return nil
}

func (s *Engine) startWorker(ctx context.Context, worker func(context.Context)) {
	s.workerWG.Add(1)
	go func() {
		defer s.workerWG.Done()
		worker(ctx)
	}()
}

// Close stops new scheduling and waits for Engine-owned workers. Persisted
// runs and injected adapters remain owned by the host.
func (s *Engine) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	if s.closed {
		s.lifecycleMu.Unlock()
		return nil
	}
	s.closed = true
	cancel := s.workerCancel
	s.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		s.workerWG.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (s *Engine) startContextArtifactCleanupWorker(ctx context.Context) {
	if s == nil || s.repo == nil {
		return
	}
	s.deleteExpiredContextArtifacts(ctx)
	ticker := time.NewTicker(contextArtifactCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.deleteExpiredContextArtifacts(ctx)
		}
	}
}

func (s *Engine) deleteExpiredContextArtifacts(ctx context.Context) {
	if s == nil || s.cfg == nil || s.cfg.Snapshot().Retention.ContextArtifactDays <= 0 {
		return
	}
	for {
		cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		deleted, err := s.repo.DeleteExpiredContextArtifacts(cleanupCtx, time.Now(), contextArtifactCleanupBatch)
		cancel()
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("context_artifact_cleanup_failed", Error(err))
			}
			return
		}
		if deleted == 0 || deleted < contextArtifactCleanupBatch {
			return
		}
	}
}

func (s *Engine) resolveAttachments(
	ctx context.Context,
	actor domain.ActorRef,
	fileIDs []string,
) ([]AttachmentInput, error) {
	normalizedIDs := normalizedUniqueFileIDs(fileIDs)
	if len(normalizedIDs) == 0 {
		return nil, nil
	}
	if s.attachments == nil {
		return nil, ErrInvalidAttachmentReference
	}
	refs := make([]domain.ResourceRef, 0, len(normalizedIDs))
	for _, id := range normalizedIDs {
		refs = append(refs, domain.ResourceRef{Kind: valueFileBE372696, ID: id})
	}
	result, err := s.attachments.ResolveAttachments(ctx, ResolveAttachmentsRequest{Actor: actor, References: refs})
	if err != nil || len(result.Attachments) != len(refs) {
		return nil, ErrInvalidAttachmentReference
	}
	resolved := make([]AttachmentInput, 0, len(result.Attachments))
	for _, item := range result.Attachments {
		resolved = append(resolved, attachmentFromResolved(item))
	}
	return resolved, nil
}

func attachmentFromResolved(item ResolvedAttachment) AttachmentInput {
	return AttachmentInput{FileID: item.Ref.ID, Kind: item.Kind, FileName: item.Name, MimeType: item.MediaType, DetectedMIME: item.DetectedMediaType, FileCategory: item.Category, FileSize: item.SizeBytes, SHA256: item.SHA256, PageCount: item.PageCount, ProcessingStatus: item.ProcessingStatus, ProcessingReady: item.ProcessingReady, ProcessingErrorCode: item.ProcessingErrorCode, ProcessingErrorMessage: item.ProcessingErrorMessage, ExtractStatus: item.ExtractStatus, EmbedStatus: item.EmbedStatus, RagOptOut: item.RAGDisabled, ChunkCount: item.ChunkCount}
}

func attachmentFromFileAsset(fileItem FileAsset) AttachmentInput {
	return AttachmentInput{FileID: fileItem.FileID, Kind: inferAttachmentKind(fileItem.DetectedMIME), FileName: fileItem.FileName, MimeType: fileItem.MimeType, DetectedMIME: fileItem.DetectedMIME, FileCategory: fileItem.FileCategory, FileSize: fileItem.SizeBytes, SHA256: fileItem.SHA256, PageCount: fileItem.PageCount, ProcessingStatus: fileItem.ProcessingStatus, ProcessingReady: fileItem.ProcessingReady, ProcessingErrorCode: fileItem.ProcessingErrorCode, ProcessingErrorMessage: fileItem.ProcessingErrorMessage, ExtractStatus: fileItem.ExtractStatus, EmbedStatus: fileItem.EmbedStatus, RagOptOut: fileItem.RagOptOut, ChunkCount: fileItem.ChunkCount}
}

func (s *Engine) resolveConversationFileContext(
	ctx context.Context,
	actor domain.ActorRef,
	fileIDs []string,
	currentFileIDs []string,
) ([]AttachmentInput, error) {
	normalizedIDs := normalizedUniqueFileIDs(fileIDs)
	if len(normalizedIDs) == 0 {
		return nil, nil
	}

	current := fileIDSet(currentFileIDs)
	resolved, err := s.resolveAttachments(ctx, actor, normalizedIDs)
	if err != nil {
		return nil, err
	}
	for index := range resolved {
		_, resolved[index].Current = current[resolved[index].FileID]
	}
	return resolved, nil
}

func normalizedUniqueFileIDs(fileIDs []string) []string {
	normalizedIDs := make([]string, 0, len(fileIDs))
	seen := make(map[string]struct{}, len(fileIDs))
	for _, rawID := range fileIDs {
		fileID := strings.TrimSpace(rawID)
		if fileID == "" {
			continue
		}
		if _, exists := seen[fileID]; exists {
			continue
		}
		seen[fileID] = struct{}{}
		normalizedIDs = append(normalizedIDs, fileID)
	}
	return normalizedIDs
}

func fileIDSet(fileIDs []string) map[string]struct{} {
	items := make(map[string]struct{}, len(fileIDs))
	for _, rawID := range fileIDs {
		fileID := strings.TrimSpace(rawID)
		if fileID != "" {
			items[fileID] = struct{}{}
		}
	}
	return items
}

func fileObjectMap(fileObjects []FileAsset) map[string]FileAsset {
	items := make(map[string]FileAsset, len(fileObjects))
	for _, item := range fileObjects {
		items[item.FileID] = item
	}
	return items
}

func resolveConversationFileAttachment(
	fileID string,
	fileMap map[string]FileAsset,
	current map[string]struct{},
) (AttachmentInput, bool, error) {
	fileItem, ok := fileMap[fileID]
	if !ok {
		if _, required := current[fileID]; required {
			return AttachmentInput{}, false, ErrInvalidAttachmentReference
		}
		return AttachmentInput{}, false, nil
	}
	_, isCurrent := current[fileID]
	return attachmentInputFromFileObject(fileItem, isCurrent), true, nil
}

func attachmentInputFromFileObject(fileItem FileAsset, isCurrent bool) AttachmentInput {
	return AttachmentInput{
		FileID:                 fileItem.FileID,
		Kind:                   inferAttachmentKind(fileItem.DetectedMIME),
		FileName:               fileItem.FileName,
		MimeType:               fileItem.MimeType,
		DetectedMIME:           fileItem.DetectedMIME,
		FileCategory:           fileItem.FileCategory,
		FileSize:               fileItem.SizeBytes,
		SHA256:                 fileItem.SHA256,
		MetaJSON:               "",
		PageCount:              fileItem.PageCount,
		ProcessingStatus:       fileItem.ProcessingStatus,
		ProcessingReady:        fileItem.ProcessingReady,
		ProcessingErrorCode:    fileItem.ProcessingErrorCode,
		ProcessingErrorMessage: fileItem.ProcessingErrorMessage,
		ExtractStatus:          fileItem.ExtractStatus,
		EmbedStatus:            fileItem.EmbedStatus,
		RagOptOut:              fileItem.RagOptOut,
		ChunkCount:             fileItem.ChunkCount,
		Current:                isCurrent,
	}
}

func (s *Engine) hydrateAttachmentsForRun(
	ctx context.Context,
	actor domain.ActorRef,
	attachments []AttachmentInput,
	onEvent func(string, map[string]interface{}) error,
) ([]AttachmentInput, error) {
	if len(attachments) == 0 {
		return attachments, nil
	}

	// 多文件并行等待：每个文件独立 WaitUntilReady，总耗时 = max(单个文件) 而非 sum。
	// 图片和空 FileID 直接跳过等待，不启动 goroutine。
	items := make([]AttachmentInput, len(attachments))
	copy(items, attachments) // 预置，图片/空 FileID 直接保留

	// mu 保护 onEvent 的并发调用（onEvent 非 goroutine-safe）。
	var mu sync.Mutex
	g, gCtx := errgroup.WithContext(ctx)

	for i, att := range attachments {
		if skipAttachmentProcessingWait(att) {
			continue
		}
		g.Go(func() error {
			return s.hydrateAttachmentForRun(gCtx, actor, &items[i], att, onEvent, &mu)
		})
	}

	if err := g.Wait(); err != nil {
		switch {
		case errors.Is(err, ErrAttachmentProcessingFailed):
			return nil, fmt.Errorf("%w: %s", ErrAttachmentProcessingNotReady, err.Error())
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return nil, err
		default:
			return nil, ErrInvalidAttachmentReference
		}
	}
	return items, nil
}

func skipAttachmentProcessingWait(att AttachmentInput) bool {
	return strings.TrimSpace(att.FileID) == "" || att.FileCategory == fileCategoryImage
}

func (s *Engine) hydrateAttachmentForRun(
	ctx context.Context,
	actor domain.ActorRef,
	target *AttachmentInput,
	att AttachmentInput,
	onEvent func(string, map[string]interface{}) error,
	mu *sync.Mutex,
) error {
	content, err := s.attachments.OpenAttachment(ctx, OpenAttachmentRequest{Actor: actor, Ref: domain.ResourceRef{Kind: valueFileBE372696, ID: att.FileID}})
	if err != nil {
		if att.Current {
			return err
		}
		return nil
	}
	target.ExtractedText = string(content.Data)
	if target.DetectedMIME == "" {
		target.DetectedMIME = content.MediaType
	}
	if target.SHA256 == "" {
		target.SHA256 = content.SHA256
	}
	emitAttachmentProcessingUpdate(onEvent, mu)
	return nil
}

func emitAttachmentProcessingUpdate(onEvent func(string, map[string]interface{}) error, mu *sync.Mutex) {
	mu.Lock()
	defer mu.Unlock()
	emitEvent(onEvent, "process_update", map[string]interface{}{
		valueStatus78866AB7: valueStreamingFB700CEB,
	})
}

func applyAttachmentProcessingError(target *AttachmentInput, att AttachmentInput, latestFile *FileAsset, err error) error {
	if att.Current {
		return err
	}
	if latestFile != nil {
		target.ProcessingStatus = latestFile.ProcessingStatus
		target.ProcessingReady = latestFile.ProcessingReady
		target.ProcessingErrorCode = latestFile.ProcessingErrorCode
		target.ProcessingErrorMessage = latestFile.ProcessingErrorMessage
		target.ExtractStatus = latestFile.ExtractStatus
		target.EmbedStatus = latestFile.EmbedStatus
	}
	return nil
}

func canUseAttachmentFullContext(att AttachmentInput, cfg Config) bool {
	if att.FileCategory == fileCategoryImage {
		return false
	}
	text := strings.TrimSpace(att.ExtractedText)
	if text == "" {
		return false
	}
	if cfg.Files.FullContextMaxBytes > 0 && int64(len([]byte(text))) > cfg.Files.FullContextMaxBytes {
		return false
	}
	if cfg.Files.FullContextMaxTokens > 0 && estimateTokens(text) > int64(cfg.Files.FullContextMaxTokens) {
		return false
	}
	if att.FileCategory == fileCategoryPDF && cfg.Files.FullContextPDFPages > 0 && att.PageCount > cfg.Files.FullContextPDFPages {
		return false
	}
	return true
}

func buildFileAttachmentSnapshot(att AttachmentInput) map[string]interface{} {
	return map[string]interface{}{
		valueFileId27747453:        att.FileID,
		valueKindEC105AB1:          att.Kind,
		"file_name":                att.FileName,
		"mime_type":                att.MimeType,
		"detected_mime":            att.DetectedMIME,
		"file_category":            att.FileCategory,
		"file_size":                att.FileSize,
		"processing_status":        att.ProcessingStatus,
		"processing_ready":         att.ProcessingReady,
		"processing_error_code":    att.ProcessingErrorCode,
		"processing_error_message": att.ProcessingErrorMessage,
	}
}

func marshalAttachmentSnapshots(items []AttachmentInput) string {
	payload := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		payload = append(payload, buildFileAttachmentSnapshot(item))
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

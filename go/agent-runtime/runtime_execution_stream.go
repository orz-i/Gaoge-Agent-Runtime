package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func (s *Engine) synthesizeRun(ctx context.Context, run model.Run, orchestrationStepID string, effective effectiveTextRunConfig, contextMessages []Message, summaries []string) (Usage, *LLMRoute, string, error) {
	messages := cloneLLMMessages(contextMessages)
	messages = append(messages, Message{Role: valueUser19341906, Content: "基于以下已完成步骤结果合成最终回答：\n" + strings.Join(summaries, "\n")})
	return s.streamRunAnswer(ctx, run, orchestrationStepID, effective, "synthesis", "synthesis", messages, strings.TrimSpace(effective.Instructions)+"\n基于已完成步骤合成最终回答。", false)
}

func (s *Engine) streamRunAnswerAttempt(ctx context.Context, run model.Run, orchestrationStepID string, effective effectiveTextRunConfig, requestKind, phase string, promptMessages []Message, instructions string, enableHostedTools bool) (Usage, *LLMRoute, string, error) {
	route, _, hostedTools, err := s.prepareStreamRun(ctx, run, effective, requestKind, enableHostedTools)
	if err != nil {
		return Usage{}, route, "", err
	}
	options := effective.Options
	if len(effective.StructuredOutputSchema) > 0 {
		spec, specErr := resolveStructuredRunOutput(route, effective.StructuredOutputSchema)
		if specErr != nil {
			return Usage{}, route, "", specErr
		}
		options = applyStructuredRunOutput(options, spec)
		instructions = structuredRunInstructions(instructions, spec)
	}
	holdForEvaluation := len(effective.StructuredOutputSchema) > 0 || s.evaluations != nil && s.evaluations.Enforces(EvaluationStageModelOutput)
	collector := runDeltaCollector{service: s, ctx: ctx, run: run, stepID: orchestrationStepID, projection: run.OutputProjection, lastFlush: time.Now(), holdForEvaluation: holdForEvaluation}
	request := GenerateInput{RequestID: run.RunID + ":" + requestKind, Messages: promptMessages, Instructions: instructions, HostedTools: hostedTools, DisableTools: len(hostedTools) == 0, Options: options}
	if err = s.recordRunLLMRouteSelected(context.WithoutCancel(ctx), run, orchestrationStepID, phase, route, request.RequestID); err != nil {
		return Usage{}, route, "", err
	}
	fields := runTelemetryFields(run,
		String("gen_ai.operation.name", "chat"),
		String("gen_ai.request.model", effective.PlatformModelName),
		String("run.id", run.RunID),
		String("step.id", orchestrationStepID),
		String("generation.phase", phase),
		String("model.name", effective.PlatformModelName),
		String("provider.protocol", route.Protocol),
		Bool("generation.stream", true),
		Bool("generation.held_for_evaluation", holdForEvaluation),
	)
	generateCtx, generationSpan := s.startSpan(ctx, "agentruntime.generation.generate", fields...)
	output, err := s.llmGateway.GenerateTextStream(generateCtx, route, request, collector.accept)
	if err != nil {
		generationSpan.RecordError(err)
	}
	generationSpan.End()
	if err != nil {
		return Usage{}, route, "", err
	}
	usage, finalText, err := s.finishStreamRun(ctx, run, orchestrationStepID, phase, route, output, &collector, effective.StructuredOutputSchema)
	return usage, route, finalText, err
}

func (s *Engine) repairStructuredRunAnswer(ctx context.Context, run model.Run, orchestrationStepID string, effective effectiveTextRunConfig, promptMessages []Message, invalidText string, validationErr error) (Usage, *LLMRoute, string, error) {
	spec, err := decodeStructuredRunOutput(effective.StructuredOutputSchema)
	if err != nil {
		return Usage{}, nil, invalidText, err
	}
	messages := cloneLLMMessages(promptMessages)
	totalUsage := Usage{}
	var lastRoute *LLMRoute
	corrections := structuredRunCorrectionAttempts(effective)
	for attempt := 1; attempt <= corrections; attempt++ {
		if err = s.recordStructuredRunCorrection(ctx, run, orchestrationStepID, attempt, validationErr); err != nil {
			return totalUsage, lastRoute, invalidText, err
		}
		messages = structuredRunCorrectionMessages(messages, invalidText, validationErr, spec)
		usage, route, finalText, attemptErr := s.streamRunAnswerAttempt(
			ctx,
			run,
			orchestrationStepID,
			effective,
			fmt.Sprintf("direct:result-correction:%d", attempt),
			"direct_result_correction",
			messages,
			strings.TrimSpace(effective.Instructions)+"\nCorrect the structured final result.",
			false,
		)
		totalUsage = mergeModelUsage(totalUsage, usage)
		if route != nil {
			lastRoute = route
		}
		if attemptErr == nil {
			return totalUsage, lastRoute, finalText, nil
		}
		if !errors.Is(attemptErr, ErrWorkflowResultInvalid) {
			return totalUsage, lastRoute, finalText, attemptErr
		}
		invalidText, validationErr = finalText, attemptErr
	}
	return totalUsage, lastRoute, invalidText, validationErr
}

func (s *Engine) prepareStreamRun(ctx context.Context, run model.Run, effective effectiveTextRunConfig, requestKind string, enableHostedTools bool) (*LLMRoute, model.ProjectionRef, []HostedTool, error) {
	if s.llmGateway == nil {
		return nil, model.ProjectionRef{}, nil, ErrModelRouteNotConfigured
	}
	route, err := s.llmGateway.PrepareTextRoute(ctx, LLMRouteInput{PlatformModelName: effective.PlatformModelName, TaskType: LLMTaskTypeText, Scope: LLMRouteScopeUser, Actor: run.Actor, Thread: run.Thread, RequestID: run.RequestID + ":" + requestKind})
	if err != nil {
		return nil, model.ProjectionRef{}, nil, err
	}
	if err = s.ensureRunCallBudgetWithReserve(ctx, run, effective, true, 0); err != nil {
		return route, model.ProjectionRef{}, nil, err
	}
	hostedTools, err := runHostedTools(effective, route.Protocol, enableHostedTools)
	return route, run.OutputProjection, hostedTools, err
}

func (s *Engine) finishStreamRun(ctx context.Context, run model.Run, stepID, phase string, route *LLMRoute, output *GenerateOutput, collector *runDeltaCollector, structuredSchema json.RawMessage) (Usage, string, error) {
	// Final flush must not leave incomplete-prefix holds unpublished when the
	// stream ended as public text; it must still refuse protocol buffers.
	if err := flushStreamBeforeEvaluation(collector); err != nil {
		return Usage{}, "", err
	}
	if collector == nil {
		return Usage{}, "", ErrInvalidInput
	}
	if err := s.recordStreamRunUsage(ctx, run, stepID, phase, route, output); err != nil {
		return usageFromGenerateOutput(output), "", err
	}
	finalText, err := finalizeStreamCollectorText(collector, output, phase)
	if err != nil {
		return usageFromGenerateOutput(output), "", err
	}
	invalidText, err := s.finishStreamEvaluation(ctx, run, stepID, phase, output, collector, finalText, structuredSchema)
	if err != nil {
		return usageFromGenerateOutput(output), invalidText, err
	}
	return usageFromGenerateOutput(output), finalText, nil
}

func flushStreamBeforeEvaluation(collector *runDeltaCollector) error {
	if collector == nil || collector.holdForEvaluation {
		return nil
	}
	return collector.flushFinal()
}

func (s *Engine) finishStreamEvaluation(ctx context.Context, run model.Run, stepID, phase string, output *GenerateOutput, collector *runDeltaCollector, finalText string, structuredSchema json.RawMessage) (string, error) {
	evaluationOutput := output
	if evaluationOutput == nil {
		evaluationOutput = &GenerateOutput{}
	}
	evaluationCopy := *evaluationOutput
	evaluationCopy.Text = finalText
	if err := s.evaluateAndPersistRuntimeBoundary(ctx, run, modelOutputEvaluationRequest(run, stepID, phase, &evaluationCopy)); err != nil {
		collector.buffer.Reset()
		return "", err
	}
	if len(structuredSchema) > 0 {
		if err := validateStructuredRunText(finalText, structuredSchema); err != nil {
			collector.buffer.Reset()
			return finalText, err
		}
	}
	if collector.holdForEvaluation {
		return "", collector.flushFinal()
	}
	return "", nil
}

// finalizeStreamCollectorText applies the public-text gate after streaming ends.
// Protocol markup (including streams suppressed mid-flight) never becomes final text.
func finalizeStreamCollectorText(collector *runDeltaCollector, output *GenerateOutput, phase string) (string, error) {
	finalText := ""
	if collector != nil {
		finalText = collector.content.String()
	}
	if strings.TrimSpace(finalText) == "" && output != nil {
		finalText = output.Text
	}
	if output == nil || strings.TrimSpace(finalText) == "" {
		return "", withErrorMessage(errCategoryDD926A6DAE, fmt.Sprintf("text run %s returned no result", phase))
	}
	if (collector != nil && collector.suppressed) || classifyModelText(finalText) == ModelTextToolProtocol {
		return "", errRequiredToolCallNotProduced
	}
	return finalText, nil
}

func runHostedTools(effective effectiveTextRunConfig, protocol string, enabled bool) ([]HostedTool, error) {
	if !enabled {
		return nil, nil
	}
	return hostedToolsForProtocol(effective, protocol)
}

func (collector *runDeltaCollector) accept(event GenerateStreamEvent) error {
	if event.Delta == "" {
		return nil
	}
	if collector.suppressed {
		// Keep full text for finishStreamRun classification without publishing.
		collector.content.WriteString(event.Delta)
		return nil
	}
	collector.content.WriteString(event.Delta)
	if looksLikeToolProtocolText(collector.content.String()) {
		collector.suppressProtocolBuffer()
		return nil
	}
	collector.buffer.WriteString(event.Delta)
	if collector.holdForEvaluation {
		return nil
	}
	if collector.buffer.Len() >= 2048 || time.Since(collector.lastFlush) >= 250*time.Millisecond {
		return collector.flush()
	}
	return nil
}

func (collector *runDeltaCollector) suppressProtocolBuffer() {
	collector.suppressed = true
	// Drop unflushed bytes so partial protocol markers never become deltas.
	collector.buffer.Reset()
}

func (collector *runDeltaCollector) flush() error {
	return collector.flushInternal(false)
}

func (collector *runDeltaCollector) flushFinal() error {
	return collector.flushInternal(true)
}

func (collector *runDeltaCollector) flushInternal(final bool) error {
	if collector.buffer.Len() == 0 {
		return nil
	}
	if collector.suppressed || looksLikeToolProtocolText(collector.content.String()) {
		collector.suppressProtocolBuffer()
		return nil
	}
	// Hold incomplete marker suffixes mid-stream; at end-of-stream release as public.
	if !final && hasIncompleteProtocolMarkerSuffix(collector.content.String()) {
		return nil
	}
	delta := collector.buffer.String()
	if err := collector.emitDelta(delta); err != nil {
		return err
	}
	collector.buffer.Reset()
	collector.lastFlush = time.Now()
	return nil
}

func (collector *runDeltaCollector) emitDelta(delta string) error {
	if collector.publishDelta != nil {
		return collector.publishDelta(delta)
	}
	if collector.service == nil {
		return nil
	}
	return collector.service.appendRunEvent(context.WithoutCancel(collector.ctx), &collector.run, "message.delta", collector.stepID, "", map[string]interface{}{valueDelta1F5E22EC: delta}, &collector.projection)
}

func (s *Engine) recordStreamRunUsage(ctx context.Context, run model.Run, stepID, phase string, route *LLMRoute, output *GenerateOutput) error {
	if output == nil {
		return nil
	}
	return s.recordRunLLMUsageForStep(context.WithoutCancel(ctx), run, stepID, phase, route, output)
}

func usageFromGenerateOutput(output *GenerateOutput) Usage {
	if output == nil {
		return Usage{}
	}
	return output.Usage
}

func (s *Engine) prepareOutput(ctx context.Context, run model.Run, stepID, toolCallID, outputID, title, summary, fileID, sourceToolCallID string, _ uint) (*model.OutputRef, model.Event, error) {
	current, err := s.repo.ListOutputs(ctx, run.Actor, run.RunID)
	if err != nil {
		return nil, model.Event{}, err
	}
	var effective effectiveTextRunConfig
	_ = json.Unmarshal([]byte(run.RunConfigSnapshotJSON), &effective)
	outputID = strings.TrimSpace(outputID)
	if outputID == "" {
		sum := sha256.Sum256([]byte(run.RunID + "\x00" + toolCallID))
		outputID = "output_" + fmt.Sprintf("%x", sum[:16])
	}
	maxOutputs := boundedTextRunConfig(effective.OutputMaxPerRun, 50, 500)
	parentRefID, eventType := outputParentAndEvent(current, effective.OutputRefs, outputID)
	if err := enforceOutputLimit(current, outputID, maxOutputs); err != nil {
		return nil, model.Event{}, err
	}
	output := &model.OutputRef{OutputID: outputID, RunID: run.RunID, StepID: stepID, ToolCallID: toolCallID, Kind: "artifact", Title: truncateRunTitle(title), Summary: truncateRunResult(summary), FileID: strings.TrimSpace(fileID), Projection: run.OutputProjection, ParentOutputID: parentRefID, Status: model.OutputDraft}
	if output.FileID != "" {
		if err := s.validateOutputFileLineage(ctx, run, output, strings.TrimSpace(sourceToolCallID)); err != nil {
			return nil, model.Event{}, err
		}
	}
	event := newRunEvent(run, eventType, stepID, output.Title, map[string]interface{}{valueOutputID7E64D749: output.OutputID, valueKindE5B2EFB3: output.Kind, valueTitle90A9E177: output.Title, valueSummaryCE2A127F: output.Summary, "fileID": output.FileID, valueStatus327C4193: output.Status}, nil)
	if output.SourceEventID == "" {
		output.SourceEventID = event.EventID
	}
	return output, event, nil
}

func outputParentAndEvent(current []model.OutputRef, refs []effectiveRunOutputRef, outputID string) (string, string) {
	eventType := "output.created"
	for _, existing := range current {
		if existing.OutputID == outputID {
			eventType = "output.updated"
			break
		}
	}
	for _, ref := range refs {
		if ref.OutputID == outputID {
			return ref.OutputID, "output.updated"
		}
	}
	return "", eventType
}

func enforceOutputLimit(current []model.OutputRef, outputID string, maxOutputs int) error {
	for _, existing := range current {
		if existing.OutputID == outputID {
			return nil
		}
	}
	if len(current) >= maxOutputs {
		return errCategoryB59D448B11
	}
	return nil
}

func (s *Engine) validateOutputFileLineage(ctx context.Context, run model.Run, output *model.OutputRef, sourceToolCallID string) error {
	if s.attachments == nil || output == nil {
		return ErrOutputLineageInvalid
	}
	resolved, err := s.attachments.ResolveAttachments(ctx, ResolveAttachmentsRequest{
		Actor:      run.Actor,
		References: []model.ResourceRef{{Kind: valueFileBE372696, ID: output.FileID}},
	})
	if err != nil || len(resolved.Attachments) != 1 || !validOutputLineageAttachment(resolved.Attachments[0], output.FileID) {
		return ErrOutputLineageInvalid
	}
	asset := resolved.Attachments[0]
	snapshot, err := s.repo.GetRunContextSnapshot(ctx, run.Actor, run.RunID)
	if err != nil {
		return ErrRunSnapshotIncompatible
	}
	var payload textRunContextSnapshotPayload
	if json.Unmarshal([]byte(snapshot.ContentJSON), &payload) != nil || payload.SemanticVersion != RuntimeSnapshotVersion {
		return ErrRunSnapshotIncompatible
	}
	fromContext := outputComesFromRunContext(output, payload.Files, asset.SHA256, snapshot.SnapshotID)
	if err = s.applyOutputToolLineage(ctx, run, output, sourceToolCallID, fromContext); err != nil {
		return err
	}
	output.FileSHA256 = strings.ToLower(strings.TrimSpace(asset.SHA256))
	output.FileMIMEType = firstNonEmptyString(asset.DetectedMediaType, asset.MediaType)
	output.Kind = firstNonEmptyString(asset.Category, outputKindFromMIME(output.FileMIMEType), valueFileA5BAA909)
	return nil
}

func validOutputLineageAttachment(asset ResolvedAttachment, fileID string) bool {
	return asset.Ref.ID == fileID && strings.TrimSpace(asset.SHA256) != ""
}

func outputComesFromRunContext(output *model.OutputRef, files []textRunContextFileRef, sha256Value, snapshotID string) bool {
	for _, file := range files {
		if file.FileID == output.FileID && strings.EqualFold(file.SHA256, sha256Value) {
			output.SourceSnapshotID = snapshotID
			return true
		}
	}
	return false
}

func (s *Engine) applyOutputToolLineage(ctx context.Context, run model.Run, output *model.OutputRef, sourceToolCallID string, fromContext bool) error {
	if sourceToolCallID == "" {
		if !fromContext {
			return ErrOutputLineageInvalid
		}
		return nil
	}
	result, err := s.repo.GetRunToolResult(ctx, run.Actor, run.RunID, sourceToolCallID)
	if err != nil || result == nil || !outputJSONContainsString(result.OutputJSON, output.FileID, 0) {
		return ErrOutputLineageInvalid
	}
	output.SourceToolCallID = sourceToolCallID
	output.SourceEventID = result.EventID
	return nil
}

func outputJSONContainsString(raw, target string, depth int) bool {
	if depth > 16 || strings.TrimSpace(target) == "" {
		return false
	}
	var value interface{}
	if json.Unmarshal([]byte(raw), &value) != nil {
		return false
	}
	return outputValueContainsString(value, target, depth)
}

func outputValueContainsString(value interface{}, target string, depth int) bool {
	if depth > 16 {
		return false
	}
	switch typed := value.(type) {
	case string:
		return typed == target
	case []interface{}:
		for _, child := range typed {
			if outputValueContainsString(child, target, depth+1) {
				return true
			}
		}
	case map[string]interface{}:
		for _, child := range typed {
			if outputValueContainsString(child, target, depth+1) {
				return true
			}
		}
	}
	return false
}

func outputKindFromMIME(mimeType string) string {
	switch {
	case strings.HasPrefix(strings.ToLower(mimeType), "image/"):
		return valueImageB8C50585
	case strings.HasPrefix(strings.ToLower(mimeType), "audio/"):
		return "audio"
	case strings.HasPrefix(strings.ToLower(mimeType), "video/"):
		return "video"
	default:
		return valueFileA5BAA909
	}
}

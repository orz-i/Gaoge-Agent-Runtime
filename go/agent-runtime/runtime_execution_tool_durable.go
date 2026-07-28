package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func (s *Engine) requestRunToolApproval(ctx context.Context, run model.Run, step model.Step, effective effectiveTextRunConfig, tool ResolvedTool, call ToolCall) (ToolResult, bool, error) {
	fields := runTelemetryFields(run,
		String("gen_ai.operation.name", "request_approval"),
		String("run.id", run.RunID),
		String("step.id", step.StepID),
		String("tool.call_id", call.ToolCallID),
		String("tool.key", tool.ToolKey),
		String("tool.name", tool.ModelName),
		String("approval.kind", model.InteractionApproveTool),
	)
	ctx, span := s.startSpan(ctx, "agentruntime.approval.request", fields...)
	defer span.End()
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(tool.ToolKey+"\x00"+tool.ModelName+"\x00"+canonicalRunJSON(json.RawMessage(call.ArgumentsJSON)))))
	request := map[string]interface{}{valueToolKey560014C9: tool.ToolKey, valueToolName4234B607: tool.ModelName, "originalName": tool.OriginalName, valueToolCallID64CA70DB: call.ToolCallID, "arguments": json.RawMessage(call.ArgumentsJSON), "fingerprint": fingerprint, "sideEffectLevel": tool.SideEffectLevel}
	interaction := newRunInteraction(run, step.StepID, model.InteractionApproveTool, request, effective.InteractionTTLHours)
	interaction.ToolCallID = call.ToolCallID
	checkpoint, err := newRunInteractionCheckpoint(run, interaction, "approve_tool")
	if err != nil {
		return ToolResult{}, false, err
	}
	events := []model.Event{
		newRunEvent(run, "checkpoint.created", step.StepID, "Tool approval checkpoint", map[string]interface{}{valueCheckpointID9CD08C70: checkpoint.CheckpointID}, nil),
		newRunEvent(run, "interaction.created", step.StepID, "Tool approval required", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID, valueType5EE8C955: interaction.Type, valueToolName4234B607: tool.ModelName}, nil),
		newRunEvent(run, "step.waiting_input", step.StepID, "Waiting for tool approval", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID}, nil),
		newRunEvent(run, "run.waiting_input", step.StepID, "Waiting for tool approval", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID, valueReasonB5B063AA: "approve_tool"}, nil),
	}
	saved, err := s.repo.CreateRunInteractionBundle(context.WithoutCancel(ctx), run.RunID, model.RunStatusRunning, interaction, checkpoint, events)
	if err == nil {
		s.publishRunEvents(run.RunID, saved)
	}
	return ToolResult{}, true, err
}

func (s *Engine) appendFrozenToolStarted(ctx context.Context, run model.Run, stepID string, tool ResolvedTool, call ToolCall) error {
	payload := map[string]interface{}{
		valueSegmentKeyB3442EFB:   runSegmentKey(ctx, run),
		valueToolCallID64CA70DB:   call.ToolCallID,
		valueToolKey560014C9:      tool.ToolKey,
		valueToolName4234B607:     tool.ModelName,
		valueProviderKind7144A4D9: tool.ProviderKind,
	}
	if signature := strings.TrimSpace(call.ThoughtSignature); signature != "" {
		payload["thoughtSignature"] = signature
	}
	started := newRunEvent(run, valueToolStartedB113F313, stepID, tool.ModelName, payload, nil)
	started.ToolCallID, started.ToolName, started.InputJSON = call.ToolCallID, tool.ModelName, call.ArgumentsJSON
	return s.appendRunEvents(context.WithoutCancel(ctx), run.RunID, []model.Event{started})
}

func (s *Engine) loadStartedToolCalls(ctx context.Context, run model.Run, stepID string) (map[string]ToolCall, error) {
	started := make(map[string]ToolCall)
	var cursor int64
	for {
		events, err := s.repo.ListRunEventsAfter(ctx, run.Actor, run.RunID, cursor, 1000)
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			cursor = event.Seq
			if event.StepID != stepID || event.EventType != valueToolStartedB113F313 || strings.TrimSpace(event.ToolCallID) == "" {
				continue
			}
			call := ToolCall{
				ToolCallID:    event.ToolCallID,
				ToolName:      event.ToolName,
				ArgumentsJSON: event.InputJSON,
			}
			var payload struct {
				ThoughtSignature string `json:"thoughtSignature"`
			}
			if json.Unmarshal([]byte(event.PayloadJSON), &payload) == nil {
				call.ThoughtSignature = strings.TrimSpace(payload.ThoughtSignature)
			}
			started[event.ToolCallID] = call
		}
		if len(events) < 1000 {
			break
		}
	}
	return started, nil
}

func (s *Engine) executeFrozenToolProvider(ctx context.Context, run model.Run, stepID string, effective effectiveTextRunConfig, tool ResolvedTool, call ToolCall, limits *TextRunExecutionLimits) (ToolExecutionResult, error) {
	requestID := run.RunID + ":tool:" + call.ToolCallID
	switch tool.ProviderKind {
	case workspaceProviderKind(effective):
		if s.workspaces == nil || effective.Workspace == nil {
			return ToolExecutionResult{}, ErrRunSnapshotIncompatible
		}
		provider, ok := s.workspaces.ResolveWorkspace(effective.Workspace.Request.Type)
		if !ok {
			return ToolExecutionResult{}, ErrRunSnapshotIncompatible
		}
		input := WorkspaceToolExecution{Actor: run.Actor, Thread: run.Thread, RunID: run.RunID, RequestID: requestID, ToolName: tool.OriginalName, ArgumentsJSON: call.ArgumentsJSON, Snapshot: *effective.Workspace}
		if tool.IdempotencyMode == ToolIdempotencyProviderReceipt {
			receiptProvider, receiptOK := provider.(WorkspaceReceiptProvider)
			if !receiptOK {
				return ToolExecutionResult{}, ErrRunToolProviderReceiptRequired
			}
			result, err := receiptProvider.ExecuteWorkspaceToolWithReceipt(ctx, input)
			result.Attempts = 1
			return validateToolExecutionResult(result, requestID, classifyWorkspaceProviderError(provider, err))
		}
		output, err := provider.ExecuteWorkspaceTool(ctx, input)
		return ToolExecutionResult{OutputJSON: output, Attempts: 1}, classifyWorkspaceProviderError(provider, err)
	case valueMcpCE1A7808:
		input := ExecuteToolInput{Actor: run.Actor, Thread: run.Thread, RequestID: requestID, ToolKey: tool.ToolKey, ProviderKind: tool.ProviderKind, ProviderKey: tool.ProviderKey, ToolName: tool.OriginalName, ArgumentsJSON: call.ArgumentsJSON, ExecutionLimits: limits, OnAttemptFailed: func(attempt, maxAttempts int, attemptErr error) error {
			return s.appendFrozenToolAttemptFailure(ctx, run, stepID, tool, call, attempt, maxAttempts, attemptErr)
		}}
		if tool.IdempotencyMode == ToolIdempotencyProviderReceipt {
			result, err := s.executeToolCallWithReceipt(ctx, input)
			return validateToolExecutionResult(result, requestID, err)
		}
		return s.executeToolCall(ctx, input)
	default:
		return ToolExecutionResult{}, ErrRunSnapshotIncompatible
	}
}

func validateToolExecutionResult(result ToolExecutionResult, requestID string, executionErr error) (ToolExecutionResult, error) {
	if executionErr != nil {
		return result, executionErr
	}
	receipt := result.Receipt
	validDisposition := receipt.Disposition == ToolReceiptCommitted || receipt.Disposition == ToolReceiptReplayed
	if strings.TrimSpace(receipt.RequestID) != strings.TrimSpace(requestID) || strings.TrimSpace(receipt.ProviderExecutionID) == "" || !validDisposition {
		return result, ErrRunToolProviderReceiptRequired
	}
	return result, nil
}

func workspaceProviderKind(effective effectiveTextRunConfig) string {
	if effective.Workspace == nil {
		return ""
	}
	return strings.TrimSpace(effective.Workspace.Request.Type)
}

func isWorkspaceProviderTool(effective effectiveTextRunConfig, tool ResolvedTool) bool {
	kind := workspaceProviderKind(effective)
	return kind != "" && strings.TrimSpace(tool.ProviderKind) == kind
}

func (s *Engine) appendFrozenToolAttemptFailure(ctx context.Context, run model.Run, stepID string, tool ResolvedTool, call ToolCall, attempt, maxAttempts int, attemptErr error) error {
	event := newRunEvent(run, "tool.attempt_failed", stepID, tool.ModelName, map[string]interface{}{valueSegmentKeyB3442EFB: runSegmentKey(ctx, run), valueToolCallID64CA70DB: call.ToolCallID, valueToolKey560014C9: tool.ToolKey, valueToolName4234B607: tool.ModelName, valueProviderKind7144A4D9: tool.ProviderKind, "attempt": attempt, "maxAttempts": maxAttempts}, nil)
	event.ToolCallID, event.ToolName = call.ToolCallID, tool.ModelName
	event.ErrorJSON = mustRunJSON(map[string]interface{}{valueErrorA8DE48C2: attemptErr.Error()})
	return s.appendRunEvents(context.WithoutCancel(ctx), run.RunID, []model.Event{event})
}

func (s *Engine) enforceFrozenWorkspaceBudget(ctx context.Context, run model.Run, effective effectiveTextRunConfig, tool ResolvedTool, output string, executionErr error) (int64, string, error) {
	if executionErr != nil || !isWorkspaceProviderTool(effective, tool) {
		return 0, output, executionErr
	}
	tokens := estimateTokens(output)
	if err := s.ensureWorkspaceToolResultBudget(ctx, run, effective, tokens); err != nil {
		return tokens, "", err
	}
	return tokens, output, nil
}

func (s *Engine) commitFrozenToolResult(ctx context.Context, run model.Run, stepID string, effective effectiveTextRunConfig, tool ResolvedTool, call ToolCall, output string, workspaceResultTokens int64, receipt ToolExecutionReceipt, executionErr error) (ToolResult, bool, error) {
	eventType, status := valueToolCompleted8D0A12FD, valueSuccess4D886D19
	if executionErr != nil {
		eventType, status = valueToolFailedFB145984, valueErrorA8DE48C2
	}
	completedPayload := map[string]interface{}{valueSegmentKeyB3442EFB: runSegmentKey(ctx, run), valueToolCallID64CA70DB: call.ToolCallID, valueToolKey560014C9: tool.ToolKey, valueToolName4234B607: tool.ModelName, valueProviderKind7144A4D9: tool.ProviderKind, valueStatus327C4193: status}
	repeatCount, countErr := s.addDeterministicFailureMetadata(ctx, run, effective, tool, call, executionErr, completedPayload)
	if countErr != nil {
		return ToolResult{}, false, countErr
	}
	if isWorkspaceProviderTool(effective, tool) {
		completedPayload["workspaceToolResultTokenEstimate"] = workspaceResultTokens
	}
	if strings.TrimSpace(receipt.ProviderExecutionID) != "" {
		completedPayload["executionReceipt"] = receipt
	}
	completed := newRunEvent(run, eventType, stepID, tool.ModelName, completedPayload, nil)
	completed.ToolCallID, completed.ToolName, completed.InputJSON = call.ToolCallID, tool.ModelName, call.ArgumentsJSON
	if executionErr != nil {
		completed.ErrorJSON = mustRunJSON(map[string]interface{}{valueErrorA8DE48C2: executionErr.Error()})
	} else {
		completed.OutputJSON = output
	}
	checkpoint := newRunContinuationCheckpoint(run, stepID, "tool_result", runContinuation{SemanticVersion: RuntimeSnapshotVersion, SegmentKey: runSegmentKey(ctx, run), Type: runContinuationContinuePlan, TargetStatus: model.RunStatusRunning, StepID: stepID, DurableToolResult: &runDurableToolResult{ToolCallID: call.ToolCallID, EventType: eventType}})
	checkpoint.ToolCallID = call.ToolCallID
	event := newRunEvent(run, "checkpoint.created", stepID, "Tool result checkpoint", map[string]interface{}{valueCheckpointID9CD08C70: checkpoint.CheckpointID, valueToolCallID64CA70DB: call.ToolCallID}, nil)
	outputRef, events, outputErr := s.prepareFrozenToolProjection(ctx, run, stepID, call, output, executionErr != nil, completed, event)
	if outputErr != nil {
		return ToolResult{}, false, outputErr
	}
	_, saved, _, commitErr := s.repo.CommitRunToolResultBundle(context.WithoutCancel(ctx), checkpoint, outputRef, events)
	if commitErr != nil {
		return ToolResult{}, false, commitErr
	}
	s.publishRunEvents(run.RunID, saved)
	if executionErr != nil {
		result := failedFrozenToolResult(call, tool, status, completed, executionErr)
		if repeatCount >= maxIdenticalDeterministicToolFailures {
			// Keep the typed cause so provider error classification survives the
			// generic repeat guard.
			return result, false, fmt.Errorf("%w: %w", errRepeatedDeterministicWorkspaceToolFailure, executionErr)
		}
		return result, false, nil
	}
	return ToolResult{ToolCallID: call.ToolCallID, ToolName: tool.ModelName, Status: status, OutputJSON: output}, false, nil
}

func (s *Engine) addDeterministicFailureMetadata(ctx context.Context, run model.Run, effective effectiveTextRunConfig, tool ResolvedTool, call ToolCall, executionErr error, payload map[string]interface{}) (int, error) {
	if !deterministicWorkspaceToolFailure(executionErr) {
		return 0, nil
	}
	fingerprint := deterministicToolFailureFingerprint(tool, call, executionErr, effective)
	prior, err := s.countDurableToolFailures(ctx, run, fingerprint)
	if err != nil {
		return 0, err
	}
	repeatCount := prior + 1
	payload["failureFingerprint"] = fingerprint
	payload["repeatCount"] = repeatCount
	payload["retryable"] = repeatCount < maxIdenticalDeterministicToolFailures
	return repeatCount, nil
}

func (s *Engine) prepareFrozenToolProjection(ctx context.Context, run model.Run, stepID string, call ToolCall, output string, executionFailed bool, completed, checkpointEvent model.Event) (*model.OutputRef, []model.Event, error) {
	events := []model.Event{completed, checkpointEvent}
	if executionFailed {
		return nil, events, nil
	}
	projection, projected := toolOutputProjection(output)
	if !projected {
		return nil, events, nil
	}
	created, outputEvent, err := s.prepareOutput(ctx, run, stepID, call.ToolCallID, "", projection.Title, projection.Summary, "", "", 0)
	if err != nil {
		return nil, nil, err
	}
	created.Kind, created.PreviewJSON = projection.Kind, string(projection.Preview)
	var artifact map[string]interface{}
	_ = json.Unmarshal(projection.Preview, &artifact)
	outputEvent.PayloadJSON = mustRunJSON(map[string]interface{}{valueOutputID7E64D749: created.OutputID, valueKindE5B2EFB3: created.Kind, valueTitle90A9E177: created.Title, valueSummaryCE2A127F: created.Summary, valueStatus327C4193: created.Status, "artifact": artifact})
	return created, []model.Event{outputEvent, completed, checkpointEvent}, nil
}

func deterministicWorkspaceToolFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var marker deterministicToolFailureMarker
	return errors.As(err, &marker) && marker.DeterministicToolFailure()
}

func deterministicToolFailureFingerprint(tool ResolvedTool, call ToolCall, err error, effective effectiveTextRunConfig) string {
	artifactContract := ""
	if effective.Workspace != nil {
		artifactContract = strings.TrimSpace(effective.Workspace.ExpectedArtifact)
		if artifactContract == "" {
			artifactContract = strings.TrimSpace(effective.Workspace.Request.ArtifactContract)
		}
	}
	payload := struct {
		ProviderKind, ProviderKey, ToolKey, ModelName, OriginalName string
		Arguments, Error, ArtifactContract                          string
	}{
		ProviderKind: tool.ProviderKind, ProviderKey: tool.ProviderKey, ToolKey: tool.ToolKey,
		ModelName: tool.ModelName, OriginalName: tool.OriginalName,
		Arguments: canonicalRunJSON(json.RawMessage(call.ArgumentsJSON)),
		Error:     normalizeDeterministicToolError(err), ArtifactContract: artifactContract,
	}
	raw := []byte(mustRunJSON(payload))
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("%x", digest[:])
}

func normalizeDeterministicToolError(err error) string {
	if err == nil {
		return ""
	}
	return strings.Join(strings.Fields(err.Error()), " ")
}

func (s *Engine) countDurableToolFailures(ctx context.Context, run model.Run, fingerprint string) (int, error) {
	count := 0
	var cursor int64
	for {
		events, err := s.repo.ListRunEventsAfter(ctx, run.Actor, run.RunID, cursor, 1000)
		if err != nil {
			return 0, err
		}
		count = advanceConsecutiveFailureCount(events, fingerprint, count)
		if len(events) == 0 || len(events) < 1000 {
			return count, nil
		}
		cursor = events[len(events)-1].Seq
	}
}

func advanceConsecutiveFailureCount(events []model.Event, fingerprint string, count int) int {
	for _, event := range events {
		if event.EventType == valueToolCompleted8D0A12FD {
			count = 0
			continue
		}
		if event.EventType != valueToolFailedFB145984 {
			continue
		}
		var payload struct {
			FailureFingerprint string `json:"failureFingerprint"`
		}
		if json.Unmarshal([]byte(event.PayloadJSON), &payload) != nil || payload.FailureFingerprint != fingerprint {
			count = 0
			continue
		}
		count++
	}
	return count
}

func failedFrozenToolResult(call ToolCall, tool ResolvedTool, status string, completed model.Event, executionErr error) ToolResult {
	return ToolResult{ToolCallID: call.ToolCallID, ToolName: tool.ModelName, Status: status, Error: executionErr.Error(), OutputJSON: completed.ErrorJSON}
}

func (s *Engine) ensureWorkspaceToolResultBudget(ctx context.Context, run model.Run, effective effectiveTextRunConfig, next int64) error {
	if effective.Workspace == nil || next <= 0 {
		return nil
	}
	limit := int64(effective.Workspace.ContextBudget * 55 / 100)
	if limit <= 0 {
		return nil
	}
	committed, err := s.committedWorkspaceToolResultTokens(ctx, run)
	if err != nil {
		return err
	}
	total := effective.Workspace.TokenEstimate + committed
	if total+next <= limit {
		return nil
	}
	message := fmt.Sprintf("workspace tool result budget exceeded: estimated=%d limit=%d", total+next, limit)
	if guidance := strings.TrimSpace(effective.Workspace.Policy.Failure.ToolResultBudgetGuidance); guidance != "" {
		message += "; " + guidance
	}
	return withErrorMessage(errCategoryD8EDA1A858, message)
}

func (s *Engine) committedWorkspaceToolResultTokens(ctx context.Context, run model.Run) (int64, error) {
	events, err := s.repo.ListRunEventsAfter(ctx, run.Actor, run.RunID, 0, 1000)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, event := range events {
		if event.EventType != valueToolCompleted8D0A12FD || strings.TrimSpace(event.PayloadJSON) == "" {
			continue
		}
		var payload struct {
			TokenEstimate int64 `json:"workspaceToolResultTokenEstimate"`
		}
		if json.Unmarshal([]byte(event.PayloadJSON), &payload) == nil {
			total += payload.TokenEstimate
		}
	}
	return total, nil
}

func frozenRunToolPolicy(effective effectiveTextRunConfig, toolKey string) (effectiveRunToolPolicy, bool) {
	for _, policy := range effective.ToolPolicies {
		if policy.ToolKey == toolKey && policy.Fingerprint != "" && fingerprintRunToolSnapshot(policy) == policy.Fingerprint {
			return policy, true
		}
	}
	return effectiveRunToolPolicy{}, false
}

func toolOutputProjection(output string) (toolProjectionInstruction, bool) {
	var envelope struct {
		Projection *toolProjectionInstruction `json:"projection"`
	}
	if json.Unmarshal([]byte(output), &envelope) != nil || envelope.Projection == nil {
		return toolProjectionInstruction{}, false
	}
	projection := *envelope.Projection
	projection.Kind = strings.TrimSpace(projection.Kind)
	projection.Title = strings.TrimSpace(projection.Title)
	projection.Summary = strings.TrimSpace(projection.Summary)
	if projection.Kind == "" || projection.Title == "" || projection.Summary == "" || len(projection.Preview) == 0 || !json.Valid(projection.Preview) {
		return toolProjectionInstruction{}, false
	}
	return projection, true
}

func terminalWorkspaceArtifactResult(effective effectiveTextRunConfig, results []ToolResult) (string, bool) {
	if effective.Workspace == nil {
		return "", false
	}
	policy := effective.Workspace.Policy
	if len(policy.TerminalArtifactTypes) == 0 {
		return "", false
	}
	resourceID := strings.TrimSpace(effective.Workspace.Request.ResourceID)
	for _, result := range results {
		projection, ok := toolOutputProjection(result.OutputJSON)
		if !ok {
			continue
		}
		var preview map[string]interface{}
		if json.Unmarshal(projection.Preview, &preview) != nil || !containsRuntimeString(policy.TerminalArtifactTypes, strings.TrimSpace(fmt.Sprint(preview["artifactType"]))) {
			continue
		}
		if field := strings.TrimSpace(policy.ArtifactResourceField); field != "" && strings.TrimSpace(fmt.Sprint(preview[field])) != resourceID {
			continue
		}
		return truncateRunResult(projection.Title + "\n\n" + projection.Summary), true
	}
	return "", false
}

func requiresWorkspaceArtifact(effective effectiveTextRunConfig) bool {
	return effective.Workspace != nil && effective.Workspace.Policy.RequiredArtifact
}

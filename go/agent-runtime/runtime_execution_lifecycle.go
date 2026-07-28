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

func (s *Engine) failTextRun(ctx context.Context, run model.Run, stepID string, err error) {
	if err == nil {
		return
	}
	if s.currentRunWaitingHandoff(context.WithoutCancel(ctx), run) {
		return
	}
	if s.isRunCanceled(ctx, run.RunID) || errors.Is(err, context.Canceled) {
		_ = s.cancelTextRun(ctx, run, stepID, ErrRunCanceled.Error())
		return
	}
	errorCode := runFailureCode(err)
	assistantContent := s.failedAssistantContent(run, err)
	diagnosticJSON := upstreamFailureDiagnosticJSON(err, run)
	events, _, finalizeErr := s.finalizeRunWithProjection(ctx, run, model.TerminalIntent{Actor: run.Actor, Thread: run.Thread, RunID: run.RunID, Outcome: model.TerminalFailed, CurrentStepID: stepID, Summary: err.Error(), ErrorCode: errorCode, ErrorMessage: err.Error(), DiagnosticJSON: diagnosticJSON}, assistantContent)
	if finalizeErr != nil {
		s.logger.Error("finalize_text_runtime_failure_failed", String("run_id", run.RunID), Error(finalizeErr))
		return
	}
	s.publishRunEvents(run.RunID, events)
}

// upstreamFailureDiagnosticJSON captures sanitized upstream rejection detail for
// admin/workbench inspection without changing end-user assistant copy.
func upstreamFailureDiagnosticJSON(err error, run model.Run) string {
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream == nil {
		return ""
	}
	payload := map[string]interface{}{"upstreamStatusCode": upstream.StatusCode}
	setTrimmed(payload, "upstreamErrorType", upstream.ErrorType)
	setTrimmed(payload, "providerRequestID", upstream.RequestID)
	setTrimmed(payload, "upstreamBody", upstream.Body)
	setTrimmed(payload, "protocol", run.ProviderProtocol)
	setTrimmed(payload, "platformModelName", run.PlatformModelName)
	attachRunConfigDiagnostic(payload, run.RunConfigSnapshotJSON)
	raw, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return ""
	}
	return string(raw)
}

func setTrimmed(payload map[string]interface{}, key, value string) {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		payload[key] = trimmed
	}
}

func attachRunConfigDiagnostic(payload map[string]interface{}, snapshotJSON string) {
	var effective effectiveTextRunConfig
	if json.Unmarshal([]byte(snapshotJSON), &effective) != nil {
		return
	}
	if effective.Workspace != nil && effective.Workspace.Request.Directive != nil {
		setTrimmed(payload, "workspaceActionID", effective.Workspace.Request.Directive.ActionID)
	}
	if names := providerToolNamesFromEffective(effective); len(names) > 0 {
		payload["providerToolNames"] = names
	}
}

func providerToolNamesFromEffective(effective effectiveTextRunConfig) []string {
	names := make([]string, 0, len(effective.ToolPolicies)+2)
	for _, policy := range effective.ToolPolicies {
		name := strings.TrimSpace(policy.ModelName)
		if name == "" {
			name = strings.TrimSpace(policy.OriginalName)
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return append(names, runControlAskUser, runControlPublishOutput)
}

func runFailureCode(err error) string {
	var workspaceErr *WorkspaceError
	if errors.As(err, &workspaceErr) && workspaceErr.Code() != "" {
		return workspaceErr.Code()
	}
	switch {
	case errors.Is(err, errPlanBudgetExceeded):
		return "plan_budget_exceeded"
	case errors.Is(err, errPlanInvalid):
		return "plan_invalid"
	case errors.Is(err, errPlannerStructuredOutputUnsupported):
		return "planner_structured_output_unsupported"
	case errors.Is(err, ErrWorkspaceArtifactMissing):
		return errorCodeWorkspaceArtifactMissing
	case errors.Is(err, errRepeatedDeterministicWorkspaceToolFailure):
		return "repeated_deterministic_tool_failure"
	case errors.Is(err, errRequiredToolCallNotProduced):
		return "required_tool_call_not_parsed"
	default:
		return "run_execution_failed"
	}
}

func (s *Engine) failedAssistantContent(run model.Run, err error) string {
	var effective effectiveTextRunConfig
	if json.Unmarshal([]byte(run.RunConfigSnapshotJSON), &effective) != nil || effective.Workspace == nil {
		if errors.Is(err, errRequiredToolCallNotProduced) {
			return "未能完成本次操作：模型返回的工具调用格式无法被系统识别。"
		}
		return "本次操作未完成，请稍后重试。"
	}
	if errors.Is(err, errRequiredToolCallNotProduced) {
		if content := strings.TrimSpace(effective.Workspace.Policy.Failure.RequiredToolCallAssistantContent); content != "" {
			return content
		}
		return "未能完成本次工作区操作：模型返回的工具调用格式无法被系统识别，且没有发布所需产物。"
	}
	var workspaceErr *WorkspaceError
	if errors.As(err, &workspaceErr) {
		if content := workspaceErr.AssistantContent(errors.Is(err, errRepeatedDeterministicWorkspaceToolFailure)); content != "" {
			return content
		}
	}
	if content := strings.TrimSpace(effective.Workspace.Policy.Failure.DefaultAssistantContent); content != "" {
		return content
	}
	return "本次工作区操作未完成，未发布所需产物。"
}

func (s *Engine) cancelTextRun(ctx context.Context, run model.Run, stepID, reason string) error {
	events, _, err := s.finalizeRunWithProjection(ctx, run, model.TerminalIntent{Actor: run.Actor, Thread: run.Thread, RunID: run.RunID, Outcome: model.TerminalCancelled, CurrentStepID: stepID, Summary: reason, ErrorCode: "run_cancelled", ErrorMessage: reason}, "")
	if err != nil {
		s.logger.Error("finalize_text_runtime_cancel_failed", String("run_id", run.RunID), Error(err))
		return err
	}
	s.publishRunEvents(run.RunID, events)
	s.cancelDelegatedChildren(context.WithoutCancel(ctx), run, reason)
	return nil
}

// RetireTextRun deliberately abandons recovery for a suspended run. It does
// not inspect the checkpoint manifest, so a corrupt checkpoint cannot trap a
// conversation queue forever.
func (s *Engine) RetireTextRun(ctx context.Context, actor model.ActorRef, runID string) (*model.Run, bool, error) {
	run, err := s.repo.GetRun(ctx, actor, normalizeRunID(runID))
	if err != nil {
		return nil, false, err
	}
	if run.RuntimeKind == model.RuntimeKindWorkflow {
		// A suspended Workflow represents an incomplete compensation. Retiring it
		// through the Text Run escape hatch would bypass the compensation ledger
		// and make the execution/result pair inconsistent.
		return nil, false, ErrRunRetireConflict
	}
	if run.Status == model.RunStatusCancelled {
		return run, true, nil
	}
	if run.Status != model.RunStatusSuspended {
		return nil, false, ErrRunRetireConflict
	}
	events, applied, err := s.finalizeRunWithProjection(ctx, *run, model.TerminalIntent{Actor: run.Actor, Thread: run.Thread, RunID: run.RunID, Outcome: model.TerminalCancelled, CurrentStepID: run.CurrentStepID, Summary: "Suspended text run retired", ErrorCode: "text_run_retired", ErrorMessage: "Text run recovery was abandoned by the user", Retire: true}, "")
	if err != nil {
		return nil, false, err
	}
	s.publishRunEvents(run.RunID, events)
	s.cancelDelegatedChildren(context.WithoutCancel(ctx), *run, "Parent run retired")
	s.FinishRunNotifications(run.RunID)
	updated, err := s.repo.GetRun(ctx, actor, run.RunID)
	if err != nil {
		return nil, false, err
	}
	return updated, !applied, nil
}

func (s *Engine) completeTextRun(ctx context.Context, run model.Run, rootStepID string, effective effectiveTextRunConfig, finalText string) error {
	if s.currentRunWaitingHandoff(context.WithoutCancel(ctx), run) {
		return nil
	}
	if err := s.validateRequiredWorkspaceArtifact(ctx, run, effective); err != nil {
		return err
	}
	result, err := textRunResult(run.RunID, finalText, effective.StructuredOutputSchema)
	if err != nil {
		return err
	}
	events, _, err := s.finalizeRunWithProjection(ctx, run, model.TerminalIntent{Actor: run.Actor, Thread: run.Thread, RunID: run.RunID, Outcome: model.TerminalCompleted, CurrentStepID: rootStepID, Summary: "Text run completed", Result: result}, finalText)
	if err != nil {
		return err
	}
	s.publishRunEvents(run.RunID, events)
	return nil
}

func textRunResult(runID, finalText string, schemaJSON json.RawMessage) (*model.RunResult, error) {
	var value interface{} = finalText
	schemaHash, err := hashWorkflowValue(json.RawMessage(`{"type":"string"}`))
	if err != nil {
		return nil, err
	}
	if len(schemaJSON) > 0 {
		decoded, err := decodeWorkflowJSON([]byte(finalText))
		if err != nil {
			return nil, errors.Join(ErrWorkflowResultInvalid, err)
		}
		if err = validateWorkflowJSON(schemaJSON, decoded); err != nil {
			return nil, errors.Join(ErrWorkflowResultInvalid, err)
		}
		value = decoded
		schemaHash, err = hashWorkflowValue(schemaJSON)
		if err != nil {
			return nil, err
		}
	}
	canonical, err := canonicalWorkflowJSON(value)
	if err != nil {
		return nil, errors.Join(ErrWorkflowResultInvalid, err)
	}
	contentHash, err := hashWorkflowValue(struct {
		Value        json.RawMessage
		Presentation string
	}{canonical, finalText})
	if err != nil {
		return nil, err
	}
	return &model.RunResult{
		RunID: runID, RuntimeKind: model.RuntimeKindText, CanonicalJSON: string(canonical),
		Presentation: finalText, SchemaHash: schemaHash, ContentHash: contentHash,
	}, nil
}

func runUsageFromUsage(usage Usage) runUsage {
	return runUsage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CacheReadTokens: usage.CacheReadTokens, CacheWriteTokens: usage.CacheWriteTokens, CacheWrite5mTokens: usage.CacheWrite5mTokens, CacheWrite1hTokens: usage.CacheWrite1hTokens, ReasoningTokens: usage.ReasoningTokens, RawUsageJSON: usage.RawUsageJSON, UsageSpeed: usage.Speed, UsageServiceTier: usage.ServiceTier, BillingRateClass: usage.BillingRateClass}
}

func addRunUsage(left, right runUsage) runUsage {
	left.InputTokens += right.InputTokens
	left.OutputTokens += right.OutputTokens
	left.CacheReadTokens += right.CacheReadTokens
	left.CacheWriteTokens += right.CacheWriteTokens
	left.CacheWrite5mTokens += right.CacheWrite5mTokens
	left.CacheWrite1hTokens += right.CacheWrite1hTokens
	left.ReasoningTokens += right.ReasoningTokens
	left.ServerSideToolUsage = mergeRunToolUsage(left.ServerSideToolUsage, right.ServerSideToolUsage)
	left.ServiceItems = append(left.ServiceItems, right.ServiceItems...)
	left.RawUsageJSON = MergeRawUsageJSON(left.RawUsageJSON, right.RawUsageJSON)
	if strings.TrimSpace(right.UsageSpeed) != "" {
		left.UsageSpeed = right.UsageSpeed
	}
	if strings.TrimSpace(right.UsageServiceTier) != "" {
		left.UsageServiceTier = right.UsageServiceTier
	}
	if strings.TrimSpace(right.BillingRateClass) != "" {
		left.BillingRateClass = right.BillingRateClass
	}
	return left
}

func mergeRunToolUsage(left, right map[string]int64) map[string]int64 {
	if len(left) == 0 && len(right) == 0 {
		return nil
	}
	result := make(map[string]int64, len(left)+len(right))
	for key, count := range left {
		if strings.TrimSpace(key) != "" && count > 0 {
			result[key] += count
		}
	}
	for key, count := range right {
		if strings.TrimSpace(key) != "" && count > 0 {
			result[key] += count
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func runUsageHasData(value runUsage) bool {
	return value.InputTokens != 0 || value.OutputTokens != 0 || value.CacheReadTokens != 0 || value.CacheWriteTokens != 0 || value.CacheWrite5mTokens != 0 || value.CacheWrite1hTokens != 0 || value.ReasoningTokens != 0 || len(value.ServerSideToolUsage) != 0 || len(value.ServiceItems) != 0 || strings.TrimSpace(value.RawUsageJSON) != ""
}

func mergeRunUsage(left, right Usage) Usage {
	left.InputTokens += right.InputTokens
	left.OutputTokens += right.OutputTokens
	left.CacheReadTokens += right.CacheReadTokens
	left.CacheWriteTokens += right.CacheWriteTokens
	left.CacheWrite5mTokens += right.CacheWrite5mTokens
	left.CacheWrite1hTokens += right.CacheWrite1hTokens
	left.ReasoningTokens += right.ReasoningTokens
	left.RawUsageJSON = MergeRawUsageJSON(left.RawUsageJSON, right.RawUsageJSON)
	if strings.TrimSpace(right.Speed) != "" {
		left.Speed = right.Speed
	}
	if strings.TrimSpace(right.ServiceTier) != "" {
		left.ServiceTier = right.ServiceTier
	}
	if strings.TrimSpace(right.BillingRateClass) != "" {
		left.BillingRateClass = right.BillingRateClass
	}
	return left
}

func (s *Engine) settleRunSegment(ctx context.Context, run model.Run, effective effectiveTextRunConfig, reservation *UsageBalanceReservation, route *LLMRoute, usage runUsage) error {
	usage, route, err := s.resolveRunSegmentUsage(ctx, run, usage, route)
	if err != nil {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "文本运行分段用量读取失败退回预扣")
		return err
	}
	result := s.runSegmentBillingResult(effective, route, usage)
	if s.billingSvc == nil {
		return s.ReleaseRunUsageReservation(ctx, reservation, "文本运行分段未配置计费服务退回预扣")
	}
	segmentKey := runSegmentKey(ctx, run)
	ledger, billable, err := s.buildRunUsageLedger(ctx, RunBillingInput{Actor: run.Actor, Thread: run.Thread, PlatformModelName: effective.PlatformModelName, ClientRunID: segmentKey, Usage: runTurnUsage(usage), Result: result})
	if err != nil {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "文本运行分段计价失败退回预扣")
		return err
	}
	if !billable {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "文本运行分段无计费用量退回预扣")
		return nil
	}
	if err = s.billingSvc.RecordUsageWithReservation(ctx, ledger, reservation); err != nil {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "文本运行分段计费失败退回预扣")
		return err
	}
	return s.appendRunBillingWithRetry(ctx, run, segmentKey, ledger)
}

func (s *Engine) resolveRunSegmentUsage(ctx context.Context, run model.Run, usage runUsage, route *LLMRoute) (runUsage, *LLMRoute, error) {
	durableUsage, durableRoute, err := s.runSegmentUsage(ctx, run)
	if err != nil || !runUsageHasData(durableUsage) {
		return usage, route, err
	}
	if route == nil {
		route = durableRoute
	}
	return durableUsage, route, nil
}

func (s *Engine) runSegmentBillingResult(effective effectiveTextRunConfig, route *LLMRoute, usage runUsage) *RunMessageResult {
	result := &RunMessageResult{Billable: true, PlatformModelName: effective.PlatformModelName, EffectiveOptions: effective.Options, CacheWrite5mTokens: usage.CacheWrite5mTokens, CacheWrite1hTokens: usage.CacheWrite1hTokens, UsageSpeed: usage.UsageSpeed, UsageServiceTier: usage.UsageServiceTier, BillingRateClass: usage.BillingRateClass, RawUsageJSON: usage.RawUsageJSON, ServerSideToolUsage: usage.ServerSideToolUsage, ServiceItems: usage.ServiceItems}
	if route != nil {
		result.UpstreamRef, result.UpstreamName, result.RoutedBindingCode, result.UpstreamModelName, result.UpstreamProtocol = route.UpstreamRef, route.UpstreamName, route.BindingCode, route.UpstreamModel, route.Protocol
	}
	return result
}

func runTurnUsage(usage runUsage) TurnUsage {
	return TurnUsage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CacheReadTokens: usage.CacheReadTokens, CacheWriteTokens: usage.CacheWriteTokens, ReasoningTokens: usage.ReasoningTokens}
}

func (s *Engine) appendRunBillingWithRetry(ctx context.Context, run model.Run, segmentKey string, ledger *UsageLedger) error {
	eventIDSum := sha256.Sum256([]byte(segmentKey))
	event := newRunEvent(run, "billing.updated", run.CurrentStepID, "Text Run segment billed", nil, nil)
	event.EventID = "evt_billing_" + fmt.Sprintf("%x", eventIDSum[:16])
	var saved *model.Event
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		saved, _, err = s.repo.AppendRunBilling(ctx, run.RunID, segmentKey, ledger.BilledCurrency, ledger.BilledNanousd, ledger.PricingSnapshotJSON, event)
		if err == nil {
			break
		}
		if attempt < 4 {
			timer := time.NewTimer(time.Duration(1<<attempt) * 5 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	if err != nil {
		return err
	}
	s.publishRunEvents(run.RunID, []model.Event{*saved})
	return nil
}

func (s *Engine) runSegmentUsage(ctx context.Context, run model.Run) (runUsage, *LLMRoute, error) {
	accumulator := runSegmentUsageAccumulator{segmentKey: runSegmentKey(ctx, run), seenToolCalls: make(map[string]struct{})}
	var cursor int64
	for {
		events, err := s.repo.ListRunEventsAfter(ctx, run.Actor, run.RunID, cursor, 1000)
		if err != nil {
			return runUsage{}, nil, err
		}
		for _, event := range events {
			cursor = event.Seq
			accumulator.apply(event)
		}
		if len(events) < 1000 {
			return accumulator.total, accumulator.route, nil
		}
	}
}

func (accumulator *runSegmentUsageAccumulator) apply(event model.Event) {
	if event.EventType == valueToolCompleted8D0A12FD {
		accumulator.applyTool(event)
		return
	}
	if event.EventType == valueUsageUpdatedABC8B0B2 {
		accumulator.applyUsage(event)
	}
}

func (accumulator *runSegmentUsageAccumulator) applyTool(event model.Event) {
	var payload runSegmentToolPayload
	if json.Unmarshal([]byte(event.PayloadJSON), &payload) != nil || payload.SegmentKey != accumulator.segmentKey || strings.TrimSpace(payload.ToolKey) == "" || payload.Replayed {
		return
	}
	callID := strings.TrimSpace(event.ToolCallID)
	if _, duplicate := accumulator.seenToolCalls[callID]; callID == "" || duplicate {
		return
	}
	accumulator.seenToolCalls[callID] = struct{}{}
	accumulator.total.ServiceItems = append(accumulator.total.ServiceItems, ServiceUsageInput{ServiceCode: "tool." + payload.ToolKey, ServiceName: payload.ToolKey, ProviderProtocol: payload.ProviderKind, CallCount: 1})
}

func (accumulator *runSegmentUsageAccumulator) applyUsage(event model.Event) {
	var payload runSegmentUsagePayload
	if json.Unmarshal([]byte(event.PayloadJSON), &payload) != nil || payload.SegmentKey != accumulator.segmentKey {
		return
	}
	accumulator.total = addRunUsage(accumulator.total, runUsage{InputTokens: payload.InputTokens, OutputTokens: payload.OutputTokens, CacheReadTokens: payload.CacheReadTokens, CacheWriteTokens: payload.CacheWriteTokens, CacheWrite5mTokens: payload.CacheWrite5mTokens, CacheWrite1hTokens: payload.CacheWrite1hTokens, ReasoningTokens: payload.ReasoningTokens, ServerSideToolUsage: payload.ServerSideToolUsage, RawUsageJSON: payload.RawUsageJSON, UsageSpeed: payload.UsageSpeed, UsageServiceTier: payload.UsageServiceTier, BillingRateClass: payload.BillingRateClass})
	if payload.UpstreamRef.ID != "" || payload.UpstreamModel != "" {
		accumulator.route = &LLMRoute{UpstreamRef: payload.UpstreamRef, UpstreamName: payload.UpstreamName, BindingCode: payload.BindingCode, UpstreamModel: payload.UpstreamModel, Protocol: payload.Protocol}
	}
}

func truncateRunResult(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > 2000 {
		return string(runes[:2000])
	}
	return string(runes)
}

func canonicalRunJSON(raw json.RawMessage) string {
	var value interface{}
	if json.Unmarshal(raw, &value) != nil {
		return strings.TrimSpace(string(raw))
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return strings.TrimSpace(string(raw))
	}
	return string(encoded)
}

package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func (s *Engine) resolveRunTools(_ context.Context, _ model.ActorRef, effective effectiveTextRunConfig) (map[string]ResolvedTool, error) {
	result := map[string]ResolvedTool{}
	if effective.SemanticVersion != RuntimeSnapshotVersion {
		return nil, ErrRunSnapshotIncompatible
	}
	if len(effective.ToolKeys) == 0 {
		return result, nil
	}
	wanted := make(map[string]struct{}, len(effective.ToolKeys))
	for _, key := range effective.ToolKeys {
		wanted[key] = struct{}{}
	}
	for _, policy := range effective.ToolPolicies {
		if _, ok := wanted[policy.ToolKey]; !ok {
			continue
		}
		if err := addResolvedRunTool(result, policy); err != nil {
			return nil, err
		}
		delete(wanted, policy.ToolKey)
	}
	if len(wanted) != 0 {
		return nil, ErrRunSnapshotIncompatible
	}
	return result, nil
}

func addResolvedRunTool(result map[string]ResolvedTool, policy effectiveRunToolPolicy) error {
	if !validRunToolPolicySnapshot(policy) {
		return ErrRunSnapshotIncompatible
	}
	if policy.ExecutionMode == valueLocalDispatchC00F9A8D && len(policy.InputSchema) == 0 || policy.ExecutionMode == valueProviderHostedF3C237B6 && len(policy.HostedVariants) == 0 {
		return ErrRunSnapshotIncompatible
	}
	approvalMode := policy.ApprovalMode
	if policy.ApprovalCapability == valuePerCall065DDC2C && approvalMode != valueNeverF5C79F24 {
		approvalMode = valueAlwaysE613B9F9
	}
	if policy.ExecutionMode == valueLocalDispatchC00F9A8D {
		result[policy.ModelName] = ResolvedTool{ToolKey: policy.ToolKey, ProviderKind: policy.ProviderKind, ProviderKey: policy.ProviderKey, ModelName: policy.ModelName, OriginalName: policy.OriginalName, Description: policy.Description, DefinitionVersion: policy.DefinitionVersion, InputSchema: append(json.RawMessage(nil), policy.InputSchema...), OutputSchema: append(json.RawMessage(nil), policy.OutputSchema...), ExecutionMode: policy.ExecutionMode, ApprovalCapability: policy.ApprovalCapability, ApprovalMode: approvalMode, RiskLevel: policy.RiskLevel, SideEffectLevel: policy.SideEffectLevel, IdempotencyMode: policy.IdempotencyMode}
	}
	return nil
}

func validRunToolPolicySnapshot(policy effectiveRunToolPolicy) bool {
	required := []string{policy.ToolKey, policy.ProviderKind, policy.ProviderKey, policy.ModelName, policy.OriginalName, policy.DefinitionVersion, policy.Fingerprint}
	for _, value := range required {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return policy.RetryCount >= 0 && policy.Concurrency > 0 && fingerprintRunToolSnapshot(policy) == policy.Fingerprint
}

func hasLocalRunTools(effective effectiveTextRunConfig) bool {
	for _, policy := range effective.ToolPolicies {
		if policy.ExecutionMode == valueLocalDispatchC00F9A8D {
			return true
		}
	}
	return false
}

func hostedToolsForProtocol(effective effectiveTextRunConfig, protocol string) ([]HostedTool, error) {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	result := make([]HostedTool, 0)
	for _, policy := range effective.ToolPolicies {
		if policy.ExecutionMode != valueProviderHostedF3C237B6 {
			continue
		}
		matched := false
		for _, variant := range policy.HostedVariants {
			if strings.ToLower(strings.TrimSpace(variant.Protocol)) != protocol {
				continue
			}
			result = append(result, HostedTool{ToolKey: policy.ToolKey, Protocol: protocol, Payload: variant.Payload})
			matched = true
			break
		}
		if !matched {
			return nil, ErrRunToolIncompatible
		}
	}
	return result, nil
}

func (s *Engine) executeRunStep(ctx context.Context, run model.Run, step model.Step, effective effectiveTextRunConfig, tools map[string]ResolvedTool, contextMessages []Message, summaries []string) (string, runUsage, bool, error) {
	prepared, err := s.prepareRunStepExecution(ctx, run, step, effective, tools, contextMessages, summaries)
	if err != nil {
		return "", runUsage{}, false, err
	}
	usage := runUsage{}
	for calls := 0; calls < effective.MaxLLMCalls; calls++ {
		output, err := s.generateRunStepTurn(ctx, run, step, effective, prepared, calls+1)
		if err != nil {
			return "", usage, false, err
		}
		usage = addRunUsage(usage, runUsageFromUsage(output.Usage))
		if len(output.ToolCalls) == 0 {
			text, retry, noToolErr := s.finishRunStepWithoutTools(ctx, run, step, effective, prepared, calls+1, output.Text)
			if retry {
				continue
			}
			return text, usage, false, noToolErr
		}
		finalText, terminal, waiting, err := s.advanceRunStepWithTools(ctx, run, step, effective, tools, prepared, calls+1, output)
		if err != nil || waiting || terminal {
			return finalText, usage, waiting, err
		}
	}
	return "", usage, false, errCategory8A92970CAF
}

func (s *Engine) finishRunStepWithoutTools(ctx context.Context, run model.Run, step model.Step, effective effectiveTextRunConfig, prepared *preparedRunStepExecution, callNumber int, text string) (string, bool, error) {
	class := classifyModelText(text)
	if class == ModelTextEmpty {
		return "", false, errCategoryDE42830626
	}
	if class != ModelTextToolProtocol && !requiresWorkspaceArtifact(effective) {
		return truncateRunResult(text), false, nil
	}
	if callNumber >= effective.MaxLLMCalls {
		return "", false, errRequiredToolCallNotProduced
	}
	if requiresWorkspaceArtifact(effective) {
		// A text-only turn cannot satisfy an artifact contract. Once every
		// frozen tool-call slot has already been consumed, forcing another
		// tool turn can only burn LLM calls until the much larger LLM budget
		// expires.
		if err := s.ensureRunCallBudgetWithReserve(ctx, run, effective, false, 0); err != nil {
			return "", false, err
		}
	}
	reason := "required_tool_missing"
	if class == ModelTextToolProtocol {
		reason = string(ModelTextToolProtocol)
	}
	if err := s.appendToolProtocolRejectedEvent(context.WithoutCancel(ctx), run, step.StepID, callNumber, reason); err != nil {
		return "", false, err
	}
	prepared.forceToolChoiceRequired = true
	prepared.messages = append(prepared.messages, Message{Role: valueUser19341906, Content: malformedToolAttemptCorrectionPrompt(effective)})
	return "", true, nil
}

func (s *Engine) advanceRunStepWithTools(ctx context.Context, run model.Run, step model.Step, effective effectiveTextRunConfig, tools map[string]ResolvedTool, prepared *preparedRunStepExecution, callNumber int, output *GenerateOutput) (string, bool, bool, error) {
	if err := s.appendRunProgress(context.WithoutCancel(ctx), run, step.StepID, publicRunProgressFromModelText(output.Text, true)); err != nil {
		return "", false, false, err
	}
	output.ToolCalls = normalizeRunToolCalls(run.RunID, step.StepID, callNumber, output.ToolCalls)
	output.ToolCalls = backfillToolCallThoughtSignatures(output.ToolCalls, output.Reasoning)
	if prepared.route != nil && shouldSerializeWorkspaceToolCalls(effective, prepared.route.Protocol) {
		output.ToolCalls = serializeWorkspaceToolCalls(output.ToolCalls)
	}
	assistantText := output.Text
	if classifyModelText(assistantText) == ModelTextToolProtocol {
		assistantText = ""
	}
	prepared.messages = append(prepared.messages, Message{Role: valueAssistantCE8D479A, Content: assistantText, ToolCalls: output.ToolCalls})
	results, waiting, err := s.executeRunStepToolCalls(ctx, run, step, effective, tools, prepared.committed, callNumber, output.ToolCalls)
	if err != nil || waiting {
		return "", false, waiting, err
	}
	if finalText, terminal := terminalWorkspaceArtifactResult(effective, results); terminal {
		return finalText, true, false, nil
	}
	prepared.messages = append(prepared.messages, Message{Role: valueToolCCF14517, ToolResults: results})
	return "", false, false, nil
}

// toolChoiceForRunStep selects provider-neutral tool calling mode.
// Explicit change_set/review contracts require native tool calls until an
// artifact is published; callers may force required after a malformed attempt.
func toolChoiceForRunStep(effective effectiveTextRunConfig, forceRequired bool) ToolChoice {
	if forceRequired || requiresWorkspaceArtifact(effective) {
		return ToolChoice{Mode: ToolChoiceRequired}
	}
	return ToolChoice{Mode: ToolChoiceAuto}
}

func (s *Engine) prepareRunStepExecution(ctx context.Context, run model.Run, step model.Step, effective effectiveTextRunConfig, tools map[string]ResolvedTool, contextMessages []Message, summaries []string) (*preparedRunStepExecution, error) {
	if s.llmGateway == nil {
		return nil, ErrModelRouteNotConfigured
	}
	route, err := s.llmGateway.PrepareTextRoute(ctx, LLMRouteInput{PlatformModelName: effective.PlatformModelName, TaskType: LLMTaskTypeText, Scope: LLMRouteScopeUser, Actor: run.Actor, Thread: run.Thread, RequestID: run.RequestID + ":" + step.StepID})
	if err != nil {
		return nil, err
	}
	hostedTools, err := hostedToolsForProtocol(effective, route.Protocol)
	if err != nil {
		return nil, err
	}
	definitions := runStepToolDefinitions(tools, effective)
	if err := s.validateWorkspaceRoute(ctx, effective, definitions, route.ModelCapabilitiesJSON, route.Protocol); err != nil {
		return nil, err
	}
	committed, committedSummaries, err := s.loadCommittedToolResults(ctx, run, step.StepID)
	if err != nil {
		return nil, err
	}
	startedCalls, err := s.loadStartedToolCalls(ctx, run, step.StepID)
	if err != nil {
		return nil, err
	}
	forceRequired, err := s.loadForceToolChoiceRequired(ctx, run, step.StepID)
	if err != nil {
		return nil, err
	}
	stepContext := append(append([]string{}, summaries...), committedSummaries...)
	messages := cloneLLMMessages(contextMessages)
	messages = append(messages, Message{Role: valueUser19341906, Content: fmt.Sprintf("执行当前计划步骤。\n当前步骤：%s\n步骤说明：%s\n已完成结果：\n%s\n已提交的工具调用不得重复执行，必须直接使用其耐久结果。", step.Title, step.Description, strings.Join(stepContext, "\n"))})
	messages = appendCommittedRunToolResults(messages, committed, startedCalls)
	if forceRequired {
		// Re-inject correction after resume rebuilds the transcript without in-memory state.
		messages = append(messages, Message{Role: valueUser19341906, Content: malformedToolAttemptCorrectionPrompt(effective)})
	}
	return &preparedRunStepExecution{
		route:                   route,
		hosted:                  hostedTools,
		tools:                   definitions,
		committed:               committed,
		messages:                messages,
		forceToolChoiceRequired: forceRequired,
	}, nil
}

func (s *Engine) appendToolProtocolRejectedEvent(ctx context.Context, run model.Run, stepID string, retryCount int, reason string) error {
	payload := toolProtocolRejectedPayload(retryCount, reason)
	return s.appendRunEvent(ctx, &run, valueModelToolProtocolRejected, stepID, "Model tool protocol rejected; next tool choice required", payload, nil)
}

func toolProtocolRejectedPayload(retryCount int, reason string) map[string]interface{} {
	if retryCount < 1 {
		retryCount = 1
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = string(ModelTextToolProtocol)
	}
	return map[string]interface{}{
		valueRetryCount:     retryCount,
		valueNextToolChoice: string(ToolChoiceRequired),
		valueReasonB5B063AA: reason,
	}
}

func forceToolChoiceRequiredFromEvents(events []model.Event, stepID string) bool {
	stepID = strings.TrimSpace(stepID)
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.EventType != valueModelToolProtocolRejected {
			continue
		}
		if stepID != "" && strings.TrimSpace(event.StepID) != stepID {
			continue
		}
		if eventForcesToolChoiceRequired(event.PayloadJSON) {
			return true
		}
	}
	return false
}

func eventForcesToolChoiceRequired(payloadJSON string) bool {
	var payload map[string]interface{}
	if json.Unmarshal([]byte(payloadJSON), &payload) != nil || payload == nil {
		return false
	}
	next, _ := payload[valueNextToolChoice].(string)
	return strings.EqualFold(strings.TrimSpace(next), string(ToolChoiceRequired))
}

func (s *Engine) loadForceToolChoiceRequired(ctx context.Context, run model.Run, stepID string) (bool, error) {
	if s.repo == nil {
		return false, nil
	}
	var cursor int64
	var matched []model.Event
	for {
		events, err := s.repo.ListRunEventsAfter(ctx, run.Actor, run.RunID, cursor, 1000)
		if err != nil {
			return false, err
		}
		for _, event := range events {
			cursor = event.Seq
			if event.EventType == valueModelToolProtocolRejected {
				matched = append(matched, event)
			}
		}
		if len(events) < 1000 {
			break
		}
	}
	return forceToolChoiceRequiredFromEvents(matched, stepID), nil
}

func (s *Engine) validateWorkspaceRoute(ctx context.Context, effective effectiveTextRunConfig, tools []ToolDefinition, capabilitiesJSON string, protocol string) error {
	if effective.Workspace == nil || s.workspaces == nil {
		return nil
	}
	provider, ok := s.workspaces.ResolveWorkspace(effective.Workspace.Request.Type)
	if !ok {
		return ErrRunSnapshotIncompatible
	}
	validator, ok := provider.(WorkspaceRouteValidator)
	if !ok {
		return nil
	}
	payloadBytes, payloadObserved := measureProviderPayloadBytesIfProtocol(protocol, tools)
	err := validator.ValidateWorkspaceRoute(ctx, WorkspaceRouteValidation{
		ModelCapabilitiesJSON:    capabilitiesJSON,
		ProviderProtocol:         protocol,
		ToolCount:                len(tools),
		ProviderToolPayloadBytes: payloadBytes,
		ProviderPayloadObserved:  payloadObserved,
	})
	return classifyWorkspaceProviderError(provider, err)
}

// runtimeControlToolDefinitionsFor returns the generic run-control tools
// allowed by the immutable workspace policy.
func runtimeControlToolDefinitionsFor(policy WorkspacePolicy) []ToolDefinition {
	askUser := ToolDefinition{Name: runControlAskUser, Description: "Ask the user for information required to continue.", InputSchema: json.RawMessage(`{"type":"object","required":["question"],"properties":{"question":{"type":"string"}}}`)}
	publishOutput := ToolDefinition{Name: runControlPublishOutput, Description: "Publish or version a durable named output with a concise summary. A file output must name either a frozen context file or the durable tool call that produced it.", InputSchema: json.RawMessage(`{"type":"object","required":["title","summary"],"additionalProperties":false,"properties":{"outputID":{"type":"string"},"title":{"type":"string"},"summary":{"type":"string"},"fileID":{"type":"string"},"sourceToolCallID":{"type":"string"}}}`)}
	controls := make([]ToolDefinition, 0, 2)
	if policy.AllowAskUser {
		controls = append(controls, askUser)
	}
	if policy.AllowPublishOutput {
		controls = append(controls, publishOutput)
	}
	return controls
}

func withRuntimeControlTools(tools []ToolDefinition, effective effectiveTextRunConfig) []ToolDefinition {
	policy := WorkspacePolicy{AllowAskUser: true, AllowPublishOutput: true}
	if effective.Workspace != nil {
		policy = effective.Workspace.Policy
	}
	controls := runtimeControlToolDefinitionsFor(policy)
	if len(controls) == 0 {
		return tools
	}
	out := make([]ToolDefinition, 0, len(tools)+len(controls))
	out = append(out, tools...)
	out = append(out, controls...)
	return out
}

func runStepToolDefinitions(tools map[string]ResolvedTool, effective effectiveTextRunConfig) []ToolDefinition {
	definitions := make([]ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		definitions = append(definitions, ToolDefinition{Name: tool.ModelName, Description: tool.Description, InputSchema: tool.InputSchema})
	}
	return withRuntimeControlTools(definitions, effective)
}

func appendCommittedRunToolResults(messages []Message, committed map[string]ToolResult, started map[string]ToolCall) []Message {
	if len(committed) == 0 {
		return messages
	}
	callIDs := make([]string, 0, len(committed))
	for callID := range committed {
		callIDs = append(callIDs, callID)
	}
	sort.Strings(callIDs)
	calls := make([]ToolCall, 0, len(callIDs))
	results := make([]ToolResult, 0, len(callIDs))
	for _, callID := range callIDs {
		result := committed[callID]
		call := ToolCall{ToolCallID: result.ToolCallID, ToolName: result.ToolName, ArgumentsJSON: `{}`}
		if startedCall, ok := started[callID]; ok {
			if name := strings.TrimSpace(startedCall.ToolName); name != "" {
				call.ToolName = name
			}
			if args := strings.TrimSpace(startedCall.ArgumentsJSON); args != "" {
				call.ArgumentsJSON = args
			}
			call.ThoughtSignature = strings.TrimSpace(startedCall.ThoughtSignature)
		}
		calls = append(calls, call)
		results = append(results, result)
	}
	return append(messages, Message{Role: valueAssistantCE8D479A, ToolCalls: calls}, Message{Role: valueToolCCF14517, ToolResults: results})
}

func (s *Engine) generateRunStepTurn(ctx context.Context, run model.Run, step model.Step, effective effectiveTextRunConfig, prepared *preparedRunStepExecution, callNumber int) (*GenerateOutput, error) {
	if err := s.ensureRunCallBudgetWithReserve(ctx, run, effective, true, 1); err != nil {
		return nil, err
	}
	fields := runTelemetryFields(run,
		String("gen_ai.operation.name", "chat"),
		String("gen_ai.request.model", effective.PlatformModelName),
		String("run.id", run.RunID),
		String("step.id", step.StepID),
		String("generation.phase", valueStepB959B536),
		String("model.name", effective.PlatformModelName),
		String("provider.protocol", prepared.route.Protocol),
		Int("generation.call_number", callNumber),
	)
	generateCtx, generationSpan := s.startSpan(ctx, "agentruntime.generation.generate", fields...)
	output, err := s.llmGateway.GenerateText(generateCtx, prepared.route, GenerateInput{
		RequestID:    fmt.Sprintf("%s:step:%s:%d", run.RunID, step.StepID, callNumber),
		Thread:       run.Thread,
		Messages:     prepared.messages,
		Instructions: strings.TrimSpace(effective.Instructions + "\n" + runPublicProgressInstruction),
		Tools:        prepared.tools,
		HostedTools:  prepared.hosted,
		DisableTools: false,
		ToolChoice:   toolChoiceForRunStep(effective, prepared.forceToolChoiceRequired),
		Options:      effective.Options,
	})
	if err != nil {
		generationSpan.RecordError(err)
	}
	generationSpan.End()
	if err != nil {
		return nil, err
	}
	if err = s.recordRunLLMUsageForStep(context.WithoutCancel(ctx), run, step.StepID, valueStepB959B536, prepared.route, output); err != nil {
		return nil, err
	}
	if err = s.evaluateAndPersistRuntimeBoundary(ctx, run, modelOutputEvaluationRequest(run, step.StepID, valueStepB959B536, output)); err != nil {
		return nil, err
	}
	return output, nil
}

func (s *Engine) appendRunProgress(ctx context.Context, run model.Run, stepID, content string) error {
	content = truncatePublicRunProgress(content)
	if content == "" {
		return nil
	}
	event := newRunEvent(run, "progress.created", stepID, content, map[string]interface{}{"contentMarkdown": content}, nil)
	event.EventID = publicRunProgressEventID(run.RunID, content)
	event.Status = model.RunStatusCompleted
	saved, created, err := s.repo.AppendRunEvent(ctx, &event)
	if err != nil {
		return err
	}
	if created {
		s.PublishRunNotification(run.RunID, runEventEnvelope(saved))
	}
	return nil
}

func publicRunProgressEventID(runID, content string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(runID) + "\x00" + strings.TrimSpace(content)))
	return "evt_progress_" + fmt.Sprintf("%x", digest[:16])
}

func truncatePublicRunProgress(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > 320 {
		return string(runes[:319]) + "…"
	}
	return string(runes)
}

// classifyModelText is the single application-layer boundary for model text
// safety. Progress, assistant finals, and stream completion all use it.
func classifyModelText(text string) ModelTextClassification {
	if strings.TrimSpace(text) == "" {
		return ModelTextEmpty
	}
	if looksLikeToolProtocolText(text) {
		return ModelTextToolProtocol
	}
	return ModelTextPublic
}

// publicRunProgressFromModelText selects text safe for user-visible run progress.
// When the model turn also issued tool calls, associated Text is treated as
// untrusted protocol/commentary and is not published as progress.created.
func publicRunProgressFromModelText(text string, hasToolCalls bool) string {
	if hasToolCalls {
		return ""
	}
	if classifyModelText(text) != ModelTextPublic {
		return ""
	}
	return truncatePublicRunProgress(text)
}

// toolProtocolMarkers is the single source of tool-call protocol markup tokens.
// looksLikeToolProtocolText and streaming incomplete-suffix holds reuse it.
func toolProtocolMarkers() []string {
	return []string{
		"<tool_call", "</tool_call",
		"<tool_calls", "</tool_calls",
		"<function_call", "</function_call",
		"<|tool_call", "<|function",
		"<|dsml|", "</|dsml|",
		"｜dsml｜", "||dsml||", "|dsml|",
		"dsml|tool_calls", "dsml|invoke", "dsml|parameter",
		`"tool_calls"`,
		`"function_call"`,
		`"functioncall"`,
	}
}

// looksLikeToolProtocolText detects known tool-call protocol markup.
func looksLikeToolProtocolText(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, marker := range toolProtocolMarkers() {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// hasIncompleteProtocolMarkerSuffix reports whether the end of text could still
// complete into a known protocol marker once more bytes arrive. Minimum prefix
// length is 2 so a lone '<' (e.g. HTML prose) does not block streaming forever.
func hasIncompleteProtocolMarkerSuffix(text string) bool {
	if text == "" {
		return false
	}
	lower := strings.ToLower(text)
	const minPrefix = 2
	// Only the trailing window can still be completing a marker.
	window := lower
	if maxMarker := longestToolProtocolMarkerLen(); len(window) > maxMarker {
		window = window[len(window)-maxMarker:]
	}
	for _, marker := range toolProtocolMarkers() {
		limit := len(marker)
		if limit <= minPrefix {
			continue
		}
		// True prefix only: full marker is already handled by looksLikeToolProtocolText.
		for n := minPrefix; n < limit; n++ {
			if strings.HasSuffix(window, marker[:n]) || strings.HasSuffix(lower, marker[:n]) {
				return true
			}
		}
	}
	return false
}

func longestToolProtocolMarkerLen() int {
	maxLen := 0
	for _, marker := range toolProtocolMarkers() {
		if len(marker) > maxLen {
			maxLen = len(marker)
		}
	}
	if maxLen < 2 {
		return 2
	}
	return maxLen
}

func malformedToolAttemptCorrectionPrompt(effective effectiveTextRunConfig) string {
	base := "The previous response encoded a tool call as plain text. Do not emit DSML, XML, JSON envelopes, or tool-call markup. Use the provider-native function/tool call mechanism."
	if requiresWorkspaceArtifact(effective) {
		if effective.Workspace != nil {
			if prompt := strings.TrimSpace(effective.Workspace.Policy.Failure.CorrectionPrompt); prompt != "" {
				return base + " " + prompt
			}
		}
		return base + " The required workspace artifact has not been published. Continue by calling the required tools with a complete schema-valid payload; a plain-text answer cannot complete this run."
	}
	return base + " If a tool is needed, call it natively; otherwise answer in plain natural language without protocol markup."
}

func normalizeRunToolCalls(runID, stepID string, callNumber int, calls []ToolCall) []ToolCall {
	result := make([]ToolCall, len(calls))
	seen := make(map[string]struct{}, len(calls))
	for index, call := range calls {
		call.ToolCallID = strings.TrimSpace(call.ToolCallID)
		if _, duplicate := seen[call.ToolCallID]; call.ToolCallID == "" || duplicate {
			seed := fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%s\x00%s", runID, stepID, callNumber, index, call.ToolName, canonicalRunJSON(json.RawMessage(call.ArgumentsJSON)))
			digest := sha256.Sum256([]byte(seed))
			call.ToolCallID = "tool_" + fmt.Sprintf("%x", digest[:16])
		}
		seen[call.ToolCallID] = struct{}{}
		result[index] = call
	}
	return result
}

// backfillToolCallThoughtSignatures copies a reasoning-part signature onto tool
// calls that arrived without one (common on Gemini Thinking multi-tool turns).
func backfillToolCallThoughtSignatures(calls []ToolCall, reasoning *ReasoningOutput) []ToolCall {
	if len(calls) == 0 || reasoning == nil {
		return calls
	}
	signature := strings.TrimSpace(reasoning.Signature)
	if signature == "" {
		return calls
	}
	for i := range calls {
		if strings.TrimSpace(calls[i].ThoughtSignature) == "" {
			calls[i].ThoughtSignature = signature
		}
	}
	return calls
}

// shouldSerializeWorkspaceToolCalls applies the immutable provider policy
// captured in the workspace snapshot.
func shouldSerializeWorkspaceToolCalls(effective effectiveTextRunConfig, protocol string) bool {
	if effective.Workspace == nil {
		return false
	}
	for _, candidate := range effective.Workspace.Policy.SerializeToolProtocols {
		if strings.EqualFold(strings.TrimSpace(protocol), strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

// serializeWorkspaceToolCalls keeps the first tool call only so assistant
// transcript tool_calls and tool results stay 1:1 for the next provider turn.
func serializeWorkspaceToolCalls(calls []ToolCall) []ToolCall {
	if len(calls) <= 1 {
		return calls
	}
	return calls[:1]
}

func (s *Engine) loadCommittedToolResults(ctx context.Context, run model.Run, stepID string) (map[string]ToolResult, []string, error) {
	results := make(map[string]ToolResult)
	summaries := make([]string, 0)
	var cursor int64
	for {
		events, err := s.repo.ListRunEventsAfter(ctx, run.Actor, run.RunID, cursor, 1000)
		if err != nil {
			return nil, nil, err
		}
		for _, event := range events {
			cursor = event.Seq
			if event.StepID != stepID || event.ToolCallID == "" || (event.EventType != valueToolCompleted8D0A12FD && event.EventType != valueToolFailedFB145984) {
				continue
			}
			result := committedToolResult(event)
			results[event.ToolCallID] = result
			summaries = append(summaries, fmt.Sprintf("已提交工具调用 %s (%s)，状态 %s，结果：%s", event.ToolCallID, event.ToolName, result.Status, truncateRunResult(result.OutputJSON)))
		}
		if len(events) < 1000 {
			break
		}
	}
	return results, summaries, nil
}

func committedToolResult(event model.Event) ToolResult {
	result := ToolResult{ToolCallID: event.ToolCallID, ToolName: event.ToolName, Status: valueSuccess4D886D19, OutputJSON: event.OutputJSON}
	if event.EventType != valueToolFailedFB145984 {
		return result
	}
	result.Status, result.OutputJSON = valueErrorA8DE48C2, event.ErrorJSON
	var payload map[string]interface{}
	if json.Unmarshal([]byte(event.ErrorJSON), &payload) == nil {
		result.Error, _ = payload[valueErrorA8DE48C2].(string)
	}
	if result.Error == "" {
		result.Error = "tool_failed"
	}
	return result
}

func (s *Engine) handleRunToolCall(ctx context.Context, run model.Run, step model.Step, effective effectiveTextRunConfig, tools map[string]ResolvedTool, call ToolCall) (ToolResult, bool, error) {
	if call.ToolName == runControlAskUser {
		return s.handleAskUserToolCall(ctx, run, step, effective, call)
	}
	if call.ToolName == runControlPublishOutput {
		return s.handlePublishOutputToolCall(ctx, run, step, effective, call)
	}
	return s.handleResolvedRunToolCall(ctx, run, step, effective, tools, call)
}

func (s *Engine) handleAskUserToolCall(ctx context.Context, run model.Run, step model.Step, effective effectiveTextRunConfig, call ToolCall) (ToolResult, bool, error) {
	if err := s.ensureRunCallBudgetWithReserve(ctx, run, effective, false, 0); err != nil {
		return ToolResult{}, false, err
	}
	started := newRunEvent(run, valueToolStartedB113F313, step.StepID, call.ToolName, map[string]interface{}{valueToolCallID64CA70DB: call.ToolCallID, valueToolName4234B607: call.ToolName}, nil)
	started.ToolCallID, started.ToolName, started.InputJSON = call.ToolCallID, call.ToolName, call.ArgumentsJSON
	if err := s.appendRunEvents(context.WithoutCancel(ctx), run.RunID, []model.Event{started}); err != nil {
		return ToolResult{}, false, err
	}
	var request map[string]interface{}
	_ = json.Unmarshal([]byte(call.ArgumentsJSON), &request)
	interaction := newRunInteraction(run, step.StepID, model.InteractionAskUser, request, effective.InteractionTTLHours)
	interaction.ToolCallID = call.ToolCallID
	checkpoint, err := newRunInteractionCheckpoint(run, interaction, "ask_user")
	if err != nil {
		return ToolResult{}, false, err
	}
	events := []model.Event{
		newRunEvent(run, "checkpoint.created", step.StepID, "Waiting checkpoint", map[string]interface{}{valueCheckpointID9CD08C70: checkpoint.CheckpointID}, nil),
		newRunEvent(run, "interaction.created", step.StepID, "User input required", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID, valueType5EE8C955: interaction.Type, valueRequest91B6AFF3: request}, nil),
		newRunEvent(run, "step.waiting_input", step.StepID, "Waiting for user input", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID}, nil),
		newRunEvent(run, "run.waiting_input", step.StepID, "Waiting for user input", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID, valueReasonB5B063AA: "ask_user"}, nil),
	}
	saved, err := s.repo.CreateRunInteractionBundle(context.WithoutCancel(ctx), run.RunID, model.RunStatusRunning, interaction, checkpoint, events)
	if err == nil {
		s.publishRunEvents(run.RunID, saved)
	}
	return ToolResult{}, true, err
}

func (s *Engine) handlePublishOutputToolCall(ctx context.Context, run model.Run, step model.Step, effective effectiveTextRunConfig, call ToolCall) (ToolResult, bool, error) {
	if err := s.ensureRunCallBudgetWithReserve(ctx, run, effective, false, 0); err != nil {
		return ToolResult{}, false, err
	}
	started := newRunEvent(run, valueToolStartedB113F313, step.StepID, call.ToolName, map[string]interface{}{valueToolCallID64CA70DB: call.ToolCallID, valueToolName4234B607: call.ToolName}, nil)
	started.ToolCallID, started.ToolName, started.InputJSON = call.ToolCallID, call.ToolName, call.ArgumentsJSON
	if err := s.appendRunEvents(context.WithoutCancel(ctx), run.RunID, []model.Event{started}); err != nil {
		return ToolResult{}, false, err
	}
	var request struct {
		OutputID, Title, Summary, FileID, SourceToolCallID string
	}
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &request); err != nil {
		return ToolResult{}, false, err
	}
	output, outputEvent, err := s.prepareOutput(ctx, run, step.StepID, call.ToolCallID, request.OutputID, request.Title, request.Summary, request.FileID, request.SourceToolCallID, 0)
	if err != nil {
		return ToolResult{}, false, err
	}
	completed := newRunEvent(run, valueToolCompleted8D0A12FD, step.StepID, call.ToolName, map[string]interface{}{valueToolCallID64CA70DB: call.ToolCallID, valueToolName4234B607: call.ToolName, valueOutputID7E64D749: output.OutputID}, nil)
	completed.ToolCallID, completed.ToolName, completed.InputJSON, completed.OutputJSON = call.ToolCallID, call.ToolName, call.ArgumentsJSON, mustRunJSON(map[string]interface{}{valueOutputID7E64D749: output.OutputID})
	checkpoint := newRunContinuationCheckpoint(run, step.StepID, "tool_result", runContinuation{SemanticVersion: RuntimeSnapshotVersion, SegmentKey: runSegmentKey(ctx, run), Type: runContinuationContinuePlan, TargetStatus: model.RunStatusRunning, StepID: step.StepID, DurableToolResult: &runDurableToolResult{ToolCallID: call.ToolCallID, EventType: valueToolCompleted8D0A12FD}})
	checkpoint.ToolCallID = call.ToolCallID
	checkpointEvent := newRunEvent(run, "checkpoint.created", step.StepID, "Published output checkpoint", map[string]interface{}{valueCheckpointID9CD08C70: checkpoint.CheckpointID, valueToolCallID64CA70DB: call.ToolCallID}, nil)
	savedOutput, saved, _, commitErr := s.repo.CommitRunToolResultBundle(context.WithoutCancel(ctx), checkpoint, output, []model.Event{outputEvent, completed, checkpointEvent})
	if commitErr != nil {
		return ToolResult{}, false, commitErr
	}
	s.publishRunEvents(run.RunID, saved)
	if savedOutput == nil {
		return ToolResult{}, false, ErrRunToolConflict
	}
	return ToolResult{ToolCallID: call.ToolCallID, ToolName: call.ToolName, Status: valueSuccess4D886D19, OutputJSON: mustRunJSON(map[string]interface{}{valueOutputID7E64D749: savedOutput.OutputID})}, false, nil
}

func (s *Engine) findCommittedReadToolResult(ctx context.Context, run model.Run, stepID string, tool ResolvedTool, call ToolCall) (model.Event, bool, error) {
	wantArguments := canonicalRunJSON(json.RawMessage(call.ArgumentsJSON))
	var cursor int64
	for {
		events, err := s.repo.ListRunEventsAfter(ctx, run.Actor, run.RunID, cursor, 1000)
		if err != nil {
			return model.Event{}, false, err
		}
		for _, event := range events {
			cursor = event.Seq
			if !committedReadToolResultMatches(event, stepID, tool, wantArguments) {
				continue
			}
			return event, true, nil
		}
		if len(events) < 1000 {
			return model.Event{}, false, nil
		}
	}
}

func (s *Engine) commitReplayedReadToolResult(ctx context.Context, run model.Run, stepID string, tool ResolvedTool, call ToolCall, source model.Event) (ToolResult, error) {
	payload := map[string]interface{}{
		valueSegmentKeyB3442EFB:   runSegmentKey(ctx, run),
		valueToolCallID64CA70DB:   call.ToolCallID,
		valueToolKey560014C9:      tool.ToolKey,
		valueToolName4234B607:     tool.ModelName,
		valueProviderKind7144A4D9: tool.ProviderKind,
		valueStatus327C4193:       valueSuccess4D886D19,
		"replayed":                true,
		"replayedFromToolCallID":  source.ToolCallID,
	}
	completed := newRunEvent(run, valueToolCompleted8D0A12FD, stepID, tool.ModelName, payload, nil)
	completed.ToolCallID, completed.ToolName, completed.InputJSON, completed.OutputJSON = call.ToolCallID, tool.ModelName, call.ArgumentsJSON, source.OutputJSON
	checkpoint := newRunContinuationCheckpoint(run, stepID, "tool_result_replayed", runContinuation{
		SemanticVersion: RuntimeSnapshotVersion,
		SegmentKey:      runSegmentKey(ctx, run),
		Type:            runContinuationContinuePlan,
		TargetStatus:    model.RunStatusRunning,
		StepID:          stepID,
		DurableToolResult: &runDurableToolResult{
			ToolCallID: call.ToolCallID,
			EventType:  valueToolCompleted8D0A12FD,
		},
	})
	checkpoint.ToolCallID = call.ToolCallID
	checkpointEvent := newRunEvent(run, "checkpoint.created", stepID, "Replayed read tool result checkpoint", map[string]interface{}{
		valueCheckpointID9CD08C70: checkpoint.CheckpointID,
		valueToolCallID64CA70DB:   call.ToolCallID,
		"replayedFromToolCallID":  source.ToolCallID,
	}, nil)
	_, saved, _, err := s.repo.CommitRunToolResultBundle(context.WithoutCancel(ctx), checkpoint, nil, []model.Event{completed, checkpointEvent})
	if err != nil {
		return ToolResult{}, err
	}
	s.publishRunEvents(run.RunID, saved)
	return ToolResult{ToolCallID: call.ToolCallID, ToolName: tool.ModelName, Status: valueSuccess4D886D19, OutputJSON: source.OutputJSON}, nil
}

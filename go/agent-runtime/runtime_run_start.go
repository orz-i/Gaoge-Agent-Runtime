package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const runCheckpointInitialContext = "initial_context"

func (s *Engine) StartTextRun(ctx context.Context, input StartTextRunInput) (*TextRunStartResult, error) {
	input.DeferInitialContinuation = false
	return s.startTextRun(ctx, input)
}

func (s *Engine) startTextRun(ctx context.Context, input StartTextRunInput) (*TextRunStartResult, error) {
	goal := strings.TrimSpace(input.Goal)
	if !validTextRunStartRequest(input, goal) {
		return nil, ErrInvalidInput
	}
	resolved, _, err := s.resolveTextRunAgentManifest(ctx, input)
	if err != nil {
		return nil, err
	}
	input = resolved
	runID := EnsureRunID(input.ClientRunID)
	fingerprint := textRunRequestFingerprint(input, goal)
	delegated := input.Delegation != nil
	existingResult, exists, err := s.existingTextRunStart(ctx, input.Actor, input.Thread, runID, fingerprint, delegated)
	if err != nil {
		return nil, err
	}
	if exists {
		return &existingResult, nil
	}
	return s.createTextRunStart(ctx, input, goal, runID, fingerprint, delegated)
}

func (s *Engine) createTextRunStart(ctx context.Context, input StartTextRunInput, goal, runID, fingerprint string, delegated bool) (*TextRunStartResult, error) {
	prepared, err := s.prepareTextRunConfiguration(ctx, input, goal)
	if err != nil {
		return nil, err
	}
	evaluation, err := s.evaluateRuntimeBoundary(ctx, EvaluationRequest{
		Stage:       EvaluationStageRunInput,
		Actor:       input.Actor,
		Thread:      input.Thread,
		RunID:       runID,
		ContentType: evaluationContentTypeText,
		Content:     goal,
		Metadata: map[string]string{
			"environmentID": strings.TrimSpace(input.Environment.ID),
			"requestID":     strings.TrimSpace(input.RequestID),
		},
	})
	if err != nil {
		return nil, err
	}
	prepared.InputEvaluation = evaluation
	return s.persistTextRunStart(ctx, input, goal, runID, fingerprint, delegated, prepared)
}

func (s *Engine) persistTextRunStart(ctx context.Context, input StartTextRunInput, goal, runID, fingerprint string, delegated bool, prepared textRunPreparedConfiguration) (*TextRunStartResult, error) {
	profile, modelName, strategy, effective, snapshot := prepared.Profile, prepared.ModelName, prepared.Effective.Strategy, prepared.Effective, prepared.Snapshot
	if err := s.EnsureRunBillingAccess(ctx, RunBillingInput{Actor: input.Actor, Thread: input.Thread, PlatformModelName: modelName, ThreadModel: input.ThreadModel, ClientRunID: runID}); err != nil {
		return nil, err
	}
	startSegmentKey := runID + ":start"
	reserved, err := s.reserveTextRunStart(ctx, input, modelName, startSegmentKey)
	if err != nil {
		return nil, err
	}
	reservation := reserved.value
	now := s.now()
	stepID := s.newRuntimeID("step")
	run := model.Run{RunID: runID, RequestID: strings.TrimSpace(input.RequestID), Actor: input.Actor, Thread: input.Thread, Environment: input.Environment, RootRunID: runID, Goal: goal, RunConfigSnapshotJSON: string(snapshot), RequestFingerprint: fingerprint, CurrentStepID: stepID, StartedBy: valueUserDD885A59, RequestedModelName: modelName, PlatformModelName: modelName, Provider: input.ThreadProvider, Status: model.RunStatusQueued, StartedAt: now}
	applyRunAgentManifest(&run, effective.AgentManifest)
	applyRunDelegation(&run, input.Delegation)
	step := model.Step{StepID: stepID, RunID: runID, StepIndex: 0, Kind: valueOrchestration1BD4660D, Title: truncateRunTitle(goal), Description: goal, Status: model.RunStatusQueued, StartedAt: now}
	continuationType, targetStatus := textRunInitialContinuation(strategy)
	checkpoint := newRunContinuationCheckpoint(run, stepID, runCheckpointInitialContext, runContinuation{SemanticVersion: RuntimeSnapshotVersion, SegmentKey: startSegmentKey, Type: continuationType, TargetStatus: targetStatus, StepID: stepID, NextRevision: 1})
	var initial textRunInitialContext
	var savedEvents []model.Event
	var parentEvents []model.Event
	if s.unitOfWork == nil || s.turnProjections == nil {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "文本运行宿主投影不可用退回预扣")
		return nil, ErrHostProjectionUnavailable
	}
	err = s.unitOfWork.Within(ctx, func(txCtx context.Context) error {
		var prepareErr error
		initial, _, prepareErr = s.prepareTextRunInitialContext(txCtx, input, effective, run)
		if prepareErr != nil {
			return prepareErr
		}
		run.InputProjection, run.OutputProjection = initial.Projection.Input, initial.Projection.Output
		checkpoint = newRunContinuationCheckpoint(run, stepID, runCheckpointInitialContext, runContinuation{SemanticVersion: RuntimeSnapshotVersion, SegmentKey: startSegmentKey, Type: continuationType, TargetStatus: targetStatus, StepID: stepID, NextRevision: 1})
		initialEvents := textRunInitialEvents(run, step, profile.Ref, strategy, effective.StrategyReason, initial.ContextSnapshot, prepared.InputEvaluation)
		initialEvents = append(initialEvents, newRunEvent(run, "checkpoint.created", stepID, "Initial context checkpoint", map[string]interface{}{valueCheckpointID7923DD64: checkpoint.CheckpointID, valueKindDAA7F13C: checkpoint.Kind}, nil))
		savedEvents, prepareErr = s.repo.CreateRunStartBundle(txCtx, &run, &step, initial.ContextSnapshot, initial.Artifacts, checkpoint, initialEvents)
		if prepareErr != nil {
			return prepareErr
		}
		parentEvents, prepareErr = s.persistRunDelegationStart(txCtx, run, input.Delegation)
		if prepareErr != nil {
			return prepareErr
		}
		return s.createTextRunStartContinuation(txCtx, input, run, *checkpoint, reservation)
	})
	if err != nil {
		return s.resolveTextRunStartPersistenceError(ctx, input, runID, fingerprint, delegated, reservation, err)
	}
	s.publishTextRunStartEvents(input, run, savedEvents, parentEvents)
	s.wakeTextRunStartContinuation(input)
	return &TextRunStartResult{Run: run, Step: step, Projection: initial.Projection}, nil
}

func (s *Engine) reserveTextRunStart(ctx context.Context, input StartTextRunInput, modelName, segmentKey string) (textRunStartReservation, error) {
	if input.DeferInitialContinuation {
		return textRunStartReservation{}, nil
	}
	reservation, _, err := s.ReserveRunUsageBalance(ctx, RunBillingInput{Actor: input.Actor, Thread: input.Thread, PlatformModelName: modelName, ThreadModel: input.ThreadModel, ClientRunID: segmentKey})
	return textRunStartReservation{value: reservation}, err
}

func (s *Engine) resolveTextRunStartPersistenceError(ctx context.Context, input StartTextRunInput, runID, fingerprint string, delegated bool, reservation *UsageBalanceReservation, cause error) (*TextRunStartResult, error) {
	_ = s.ReleaseRunUsageReservation(ctx, reservation, "文本运行创建失败退回预扣")
	if errors.Is(cause, ErrDuplicate) {
		return s.recoverDuplicateTextRunStart(ctx, input.Actor, input.Thread, runID, fingerprint, cause, delegated)
	}
	return nil, cause
}

func (s *Engine) publishTextRunStartEvents(input StartTextRunInput, run model.Run, events, parentEvents []model.Event) {
	s.publishRunEvents(run.RunID, events)
	if input.Delegation != nil {
		s.publishRunEvents(input.Delegation.Handoff.ParentRunID, parentEvents)
	}
}

func (s *Engine) createTextRunStartContinuation(ctx context.Context, input StartTextRunInput, run model.Run, checkpoint model.Checkpoint, reservation *UsageBalanceReservation) error {
	if input.DeferInitialContinuation {
		return nil
	}
	return s.createContinuationJob(ctx, run, checkpoint, "text_run_start", reservation)
}

func (s *Engine) wakeTextRunStartContinuation(input StartTextRunInput) {
	if !input.DeferInitialContinuation {
		s.wakeContinuationJobs()
	}
}

func validTextRunStartInput(goal string) bool {
	return goal != "" && len([]rune(goal)) <= 20000
}

func validTextRunStartRequest(input StartTextRunInput, goal string) bool {
	return validActorRef(input.Actor) && strings.TrimSpace(input.Thread.ID) != "" && strings.TrimSpace(input.Environment.ID) != "" && validTextRunStartInput(goal)
}

func textRunEnvironmentWorkspaceCompatible(environment *EnvironmentProfile, workspace *WorkspaceSnapshot) bool {
	if environment == nil {
		return false
	}
	if workspace != nil {
		return environment.SupportsBindingScope(strings.TrimSpace(workspace.Request.Type))
	}
	return environment.SupportsBindingScope("general")
}

func workspaceSnapshotToolKeys(workspace *WorkspaceSnapshot) []string {
	if workspace == nil {
		return nil
	}
	keys := make([]string, 0, len(workspace.Tools))
	for _, tool := range workspace.Tools {
		if key := strings.TrimSpace(tool.ToolKey); key != "" {
			keys = append(keys, key)
		}
	}
	return uniqueRuntimeStrings(keys)
}

// applyWorkspaceToolDefinitions replaces catalog tool contracts and description
// with workspace-compiled definitions, then recomputes the frozen fingerprint.
func applyWorkspaceToolDefinitions(policies []effectiveRunToolPolicy, tools []WorkspaceToolDefinition) ([]effectiveRunToolPolicy, error) {
	if len(tools) == 0 {
		return policies, nil
	}
	byKey, byName, err := indexWorkspaceToolDefinitions(tools)
	if err != nil {
		return nil, err
	}
	result := make([]effectiveRunToolPolicy, len(policies))
	copy(result, policies)
	matched := make(map[string]struct{}, len(tools))
	for index := range result {
		tool, ok := lookupWorkspaceToolDefinition(result[index], byKey, byName)
		if !ok {
			continue
		}
		if err := overlayWorkspaceToolPolicy(&result[index], tool); err != nil {
			return nil, err
		}
		matched[tool.Name] = struct{}{}
	}
	if len(matched) != len(byName) {
		// Every workspace tool must resolve into a frozen policy; otherwise the
		// model would miss a contract the workspace expects to execute.
		return nil, ErrRunToolUnavailable
	}
	return result, nil
}

func indexWorkspaceToolDefinitions(tools []WorkspaceToolDefinition) (byKey, byName map[string]WorkspaceToolDefinition, err error) {
	byKey = make(map[string]WorkspaceToolDefinition, len(tools))
	byName = make(map[string]WorkspaceToolDefinition, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" || len(tool.InputSchema) == 0 {
			return nil, nil, ErrInvalidInput
		}
		if schemaErr := validateToolContractSchemas(tool.InputSchema, tool.OutputSchema); schemaErr != nil {
			return nil, nil, errors.Join(ErrInvalidInput, schemaErr)
		}
		byName[name] = tool
		if key := strings.TrimSpace(tool.ToolKey); key != "" {
			byKey[key] = tool
		}
	}
	return byKey, byName, nil
}

func lookupWorkspaceToolDefinition(policy effectiveRunToolPolicy, byKey, byName map[string]WorkspaceToolDefinition) (WorkspaceToolDefinition, bool) {
	if tool, ok := byKey[strings.TrimSpace(policy.ToolKey)]; ok {
		return tool, true
	}
	if tool, ok := byName[strings.TrimSpace(policy.ModelName)]; ok {
		return tool, true
	}
	tool, ok := byName[strings.TrimSpace(policy.OriginalName)]
	return tool, ok
}

func overlayWorkspaceToolPolicy(policy *effectiveRunToolPolicy, tool WorkspaceToolDefinition) error {
	policy.Description = tool.Description
	policy.InputSchema = append(json.RawMessage(nil), tool.InputSchema...)
	policy.OutputSchema = append(json.RawMessage(nil), tool.OutputSchema...)
	policy.Fingerprint = fingerprintRunToolSnapshot(*policy)
	if policy.Fingerprint == "" {
		return ErrInvalidInput
	}
	return nil
}

func positiveTextRunValue(value int) int {
	if value <= 0 {
		return 8
	}
	return value
}

func nonNegativeTextRunValue(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func normalizedPlanApprovalMode(value string) string {
	if strings.TrimSpace(value) == valueAuto407FFE1D {
		return valueAuto407FFE1D
	}
	return valueRequired466769C7
}

func (s *Engine) prepareTextRunInitialContext(ctx context.Context, input StartTextRunInput, effective effectiveTextRunConfig, run model.Run) (textRunInitialContext, string, error) {
	userMessage, attachments, branch, err := s.prepareTextRunMessagePair(ctx, input, effective, run.RunID)
	if err != nil {
		return textRunInitialContext{}, "文本运行消息创建准备失败退回预扣", err
	}
	if s.llmGateway == nil {
		return textRunInitialContext{}, "文本运行上下文路由缺失退回预扣", ErrModelRouteNotConfigured
	}
	projection, err := s.turnProjections.BeginTurn(ctx, BeginTurnRequest{Actor: run.Actor, Thread: run.Thread, RunID: run.RunID, ContentType: userMessage.ContentType, Content: userMessage.Content, TokenEstimate: estimateTokens(userMessage.Content), Parent: userMessage.Parent, Source: userMessage.Source, BranchReason: input.BranchReason, Attachments: attachments})
	if err != nil {
		return textRunInitialContext{}, "文本运行宿主投影创建失败退回预扣", err
	}
	userMessage.Projection = projection.Input
	run.InputProjection, run.OutputProjection = projection.Input, projection.Output
	route, err := s.llmGateway.PrepareTextRoute(ctx, LLMRouteInput{PlatformModelName: effective.PlatformModelName, TaskType: LLMTaskTypeText, Scope: LLMRouteScopeUser, Actor: run.Actor, Thread: run.Thread, RequestID: run.RequestID})
	if err != nil {
		return textRunInitialContext{}, "文本运行上下文路由准备失败退回预扣", err
	}
	if _, err = hostedToolsForProtocol(effective, route.Protocol); err != nil {
		return textRunInitialContext{}, "文本运行工具与路由协议不兼容退回预扣", err
	}
	snapshot, artifacts, err := s.compileTextRunContext(ctx, run, effective, route, userMessage, branch)
	if err != nil {
		return textRunInitialContext{}, "文本运行上下文编译失败退回预扣", err
	}
	return textRunInitialContext{UserMessage: userMessage, Attachments: attachments, ContextSnapshot: snapshot, Artifacts: artifacts, Projection: projection}, "", nil
}

func textRunInitialContinuation(strategy string) (string, string) {
	if strategy == TextRunStrategyPlanned {
		return runContinuationStartPlanning, model.RunStatusPreparing
	}
	return runContinuationStartDirect, model.RunStatusRunning
}

func textRunInitialEvents(run model.Run, step model.Step, environmentRef model.ResourceRef, strategy, reason string, snapshot *model.ContextSnapshot, inputEvaluation EvaluationReport) []model.Event {
	events := []model.Event{
		newRunEvent(run, "run.started", step.StepID, "Text run started", map[string]interface{}{valueGoal855E06D1: run.Goal, "environmentRef": environmentRef, "strategy": strategy}, nil),
		newRunEvent(run, "run.strategy_selected", step.StepID, "Text run strategy selected", map[string]interface{}{"strategy": strategy, "reasonCode": reason}, nil),
		newRunEvent(run, "step.started", step.StepID, "Orchestration started", map[string]interface{}{valueStepID549B95DB: step.StepID, valueTitle1D003E0B: step.Title}, nil),
	}
	if strategy == TextRunStrategyPlanned {
		events = append(events, newRunEvent(run, valueRunPreparingA8E38F48, step.StepID, "Preparing plan", map[string]interface{}{valueRevision0742568C: 1}, nil))
	}
	if len(inputEvaluation.Findings) > 0 {
		events = append(events, runtimeEvaluationEvent(run, step.StepID, inputEvaluation))
	}
	return append(events, newRunEvent(run, "context.compiled", step.StepID, "Text context compiled", map[string]interface{}{"contextHash": snapshot.ContentHash, "fileCount": snapshot.FileCount, "ragCount": snapshot.RAGCount, "skillCount": snapshot.SkillCount, "memoryCount": snapshot.MemoryCount, "outputCount": snapshot.OutputCount, "evidenceCount": snapshot.EvidenceCount, "retrievalFallbackCount": snapshot.RetrievalFallbackCount, "skippedCount": snapshot.SkippedCount}, nil))
}

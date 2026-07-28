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

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

type StartWorkflowInput struct {
	Actor            model.ActorRef
	Thread           model.ThreadRef
	RequestID        string
	ClientRunID      string
	Definition       model.ResourceRef
	Input            json.RawMessage
	Environment      model.ResourceRef
	Limits           *model.WorkflowLimits
	CacheMode        string
	ParentProjection *model.ProjectionRef
	SourceProjection *model.ProjectionRef
	BranchReason     string
	ThreadModel      string
	ThreadProvider   string
	ThreadScope      string
	Workspace        *WorkspaceRequest

	// The remaining fields are Runtime-only composition inputs used for nested
	// workflows. HTTP callers never set them.
	FrozenWorkspace  *WorkspaceSnapshot
	ParentRunID      string
	RootRunID        string
	BudgetOwnerRunID string
	Depth            int
}

type WorkflowStartResult struct {
	Run        model.Run
	Step       model.Step
	Projection TurnProjection
}

type effectiveWorkflowConfig struct {
	SemanticVersion     int                        `json:"semanticVersion"`
	Definition          model.ResourceRef          `json:"definition"`
	DefinitionHash      string                     `json:"definitionHash"`
	DependencyHash      string                     `json:"dependencyHash"`
	InputJSON           json.RawMessage            `json:"input"`
	Limits              model.WorkflowLimits       `json:"limits"`
	CacheMode           string                     `json:"cacheMode"`
	Environment         model.ResourceRef          `json:"environment"`
	EnvironmentSnapshot json.RawMessage            `json:"environmentSnapshot"`
	Workspace           *WorkspaceSnapshot         `json:"workspace,omitempty"`
	ThreadSnapshotHash  string                     `json:"threadSnapshotHash"`
	ThreadModel         string                     `json:"threadModel,omitempty"`
	Dependencies        []model.WorkflowDependency `json:"dependencies"`
}

type workflowRuntimeState struct {
	SemanticVersion int                                `json:"semanticVersion"`
	Input           interface{}                        `json:"input"`
	Scopes          map[string]workflowScopeState      `json:"scopes"`
	Activations     map[string]workflowActivationState `json:"activations"`
	Effects         map[string]workflowEffectState     `json:"effects,omitempty"`
	Waits           map[string]model.WorkflowWait      `json:"waits"`
	Compensations   []model.WorkflowCompensation       `json:"compensations"`
	Result          interface{}                        `json:"result,omitempty"`
	Presentation    string                             `json:"presentation,omitempty"`
	Returned        bool                               `json:"returned"`
	CancelRequested bool                               `json:"cancelRequested"`
	ErrorCode       string                             `json:"errorCode,omitempty"`
	ErrorMessage    string                             `json:"errorMessage,omitempty"`
}

type workflowScopeState struct {
	Vars    map[string]interface{} `json:"vars"`
	Outputs map[string]interface{} `json:"outputs"`
	Item    interface{}            `json:"item,omitempty"`
	Index   *int                   `json:"index,omitempty"`
}

type workflowActivationState struct {
	NodeID           string        `json:"nodeID"`
	Path             string        `json:"path"`
	ScopeKey         string        `json:"scopeKey"`
	StepID           string        `json:"stepID"`
	Status           string        `json:"status"`
	Cursor           int           `json:"cursor"`
	Iteration        int           `json:"iteration"`
	Attempt          int           `json:"attempt"`
	CompletionOrder  int64         `json:"completionOrder"`
	Items            []interface{} `json:"items,omitempty"`
	Results          []interface{} `json:"results,omitempty"`
	ItemCursors      []int         `json:"itemCursors,omitempty"`
	Output           interface{}   `json:"output,omitempty"`
	WaitID           string        `json:"waitID,omitempty"`
	InteractionID    string        `json:"interactionID,omitempty"`
	ChildRunID       string        `json:"childRunID,omitempty"`
	EffectID         string        `json:"effectID,omitempty"`
	WakeAt           *time.Time    `json:"wakeAt,omitempty"`
	ReservedLLM      int           `json:"reservedLLM,omitempty"`
	ReservedTools    int           `json:"reservedTools,omitempty"`
	ReservedChildren int           `json:"reservedChildren,omitempty"`
	CacheChecked     bool          `json:"cacheChecked,omitempty"`
	Approved         bool          `json:"approved,omitempty"`
	ErrorCode        string        `json:"errorCode,omitempty"`
	ErrorMessage     string        `json:"errorMessage,omitempty"`
}

func (s *Engine) StartWorkflow(ctx context.Context, input StartWorkflowInput) (*WorkflowStartResult, error) {
	if s == nil || s.repo == nil || !validWorkflowStartInput(input) {
		return nil, ErrInvalidInput
	}
	preparation, err := s.prepareWorkflowStart(ctx, input)
	if err != nil {
		return nil, err
	}
	if existing, found, findErr := s.existingWorkflowStart(ctx, input, preparation.runID, preparation.fingerprint); findErr != nil || found {
		return existing, findErr
	}
	return s.persistWorkflowStart(ctx, input, preparation.definition, preparation.inputValue, preparation.runID, preparation.fingerprint, preparation.prepared)
}

type workflowStartPreparation struct {
	definition  model.WorkflowDefinition
	inputValue  interface{}
	runID       string
	fingerprint string
	prepared    preparedWorkflowStart
}

func (s *Engine) prepareWorkflowStart(ctx context.Context, input StartWorkflowInput) (workflowStartPreparation, error) {
	definition, err := s.loadWorkflowStartDefinition(ctx, input)
	if err != nil {
		return workflowStartPreparation{}, err
	}
	inputValue, canonicalInput, limits, cacheMode, err := prepareWorkflowStartValues(input, definition)
	if err != nil {
		return workflowStartPreparation{}, err
	}
	runID := EnsureRunID(input.ClientRunID)
	prepared, err := s.prepareWorkflowStartSnapshot(ctx, input, definition, canonicalInput, limits, cacheMode)
	if err != nil {
		return workflowStartPreparation{}, err
	}
	fingerprint, err := workflowStartFingerprint(input, definition, prepared)
	if err != nil {
		return workflowStartPreparation{}, err
	}
	return workflowStartPreparation{
		definition: definition, inputValue: inputValue, runID: runID,
		fingerprint: fingerprint, prepared: prepared,
	}, nil
}

func (s *Engine) loadWorkflowStartDefinition(ctx context.Context, input StartWorkflowInput) (model.WorkflowDefinition, error) {
	definition, err := s.repo.GetWorkflowDefinition(ctx, input.Actor, input.Definition)
	if err != nil {
		return model.WorkflowDefinition{}, err
	}
	if definition.Status != model.WorkflowDefinitionStatusActive {
		return model.WorkflowDefinition{}, ErrWorkflowDefinitionDisabled
	}
	if err = s.verifyWorkflowDependencies(ctx, input, *definition); err != nil {
		return model.WorkflowDefinition{}, err
	}
	return *definition, nil
}

func prepareWorkflowStartValues(input StartWorkflowInput, definition model.WorkflowDefinition) (interface{}, json.RawMessage, model.WorkflowLimits, string, error) {
	inputValue, err := decodeWorkflowJSON(input.Input)
	if err != nil {
		return nil, nil, model.WorkflowLimits{}, "", ErrInvalidInput
	}
	if err = validateWorkflowJSON(definition.InputSchema, inputValue); err != nil {
		return nil, nil, model.WorkflowLimits{}, "", err
	}
	canonicalInput, err := canonicalWorkflowJSON(inputValue)
	if err != nil {
		return nil, nil, model.WorkflowLimits{}, "", err
	}
	limits, err := narrowWorkflowLimits(definition.Limits, input.Limits)
	if err != nil {
		return nil, nil, model.WorkflowLimits{}, "", err
	}
	cacheMode, err := normalizeWorkflowCacheMode(input.CacheMode)
	if err != nil {
		return nil, nil, model.WorkflowLimits{}, "", err
	}
	return inputValue, json.RawMessage(canonicalInput), limits, cacheMode, nil
}

type preparedWorkflowStart struct {
	InputJSON           json.RawMessage
	Limits              model.WorkflowLimits
	CacheMode           string
	EnvironmentSnapshot json.RawMessage
	Workspace           *WorkspaceSnapshot
	ThreadSnapshotHash  string
	ConfigJSON          string
}

func validWorkflowStartInput(input StartWorkflowInput) bool {
	return validActorRef(input.Actor) && strings.TrimSpace(input.Thread.Kind) != "" && strings.TrimSpace(input.Thread.ID) != "" &&
		input.Definition.Kind == model.WorkflowDefinitionKind && strings.TrimSpace(input.Definition.ID) != "" &&
		strings.TrimSpace(input.Environment.ID) != "" && len(input.Input) > 0 && input.Depth >= 0
}

func (s *Engine) prepareWorkflowStartSnapshot(ctx context.Context, input StartWorkflowInput, definition model.WorkflowDefinition, canonicalInput []byte, limits model.WorkflowLimits, cacheMode string) (preparedWorkflowStart, error) {
	profile, err := s.resolveTextRunProfileAtRevision(ctx, input.Actor, input.Environment)
	if err != nil {
		return preparedWorkflowStart{}, err
	}
	scope := strings.TrimSpace(input.ThreadScope)
	if scope == "" {
		scope = workflowEnvironment
	}
	if !profile.SupportsBindingScope(scope) {
		return preparedWorkflowStart{}, ErrEnvironmentBindingNotAllowed
	}
	environmentJSON, err := canonicalWorkflowJSON(profile)
	if err != nil {
		return preparedWorkflowStart{}, err
	}
	workspaceValue, found, err := s.compileTextRunWorkspace(ctx, StartTextRunInput{
		Actor: input.Actor, Thread: input.Thread, ThreadScope: scope, Workspace: input.Workspace, FrozenWorkspace: input.FrozenWorkspace,
	}, input.ThreadModel)
	if err != nil {
		return preparedWorkflowStart{}, err
	}
	var workspace *WorkspaceSnapshot
	if found {
		workspace = &workspaceValue
	}
	threadHash, err := workflowThreadSnapshotHash(input, environmentJSON, workspace, scope)
	if err != nil {
		return preparedWorkflowStart{}, err
	}
	effective := effectiveWorkflowConfig{
		SemanticVersion: RuntimeSnapshotVersion, Definition: definition.Ref(), DefinitionHash: definition.DefinitionHash,
		DependencyHash: definition.DependencyHash, InputJSON: append(json.RawMessage(nil), canonicalInput...), Limits: limits,
		CacheMode: cacheMode, Environment: input.Environment, EnvironmentSnapshot: environmentJSON, Workspace: workspace,
		ThreadSnapshotHash: threadHash, ThreadModel: strings.TrimSpace(input.ThreadModel),
		Dependencies: append([]model.WorkflowDependency(nil), definition.Dependencies...),
	}
	configJSON, err := canonicalWorkflowJSON(effective)
	if err != nil {
		return preparedWorkflowStart{}, err
	}
	return preparedWorkflowStart{
		InputJSON: canonicalInput, Limits: limits, CacheMode: cacheMode, EnvironmentSnapshot: environmentJSON,
		Workspace: workspace, ThreadSnapshotHash: threadHash, ConfigJSON: string(configJSON),
	}, nil
}

func workflowThreadSnapshotHash(
	input StartWorkflowInput,
	environmentSnapshot json.RawMessage,
	workspace *WorkspaceSnapshot,
	scope string,
) (string, error) {
	return hashWorkflowValue(struct {
		Actor               model.ActorRef
		Thread              model.ThreadRef
		Environment         model.ResourceRef
		EnvironmentSnapshot json.RawMessage
		Workspace           *WorkspaceSnapshot
		Parent              *model.ProjectionRef
		Source              *model.ProjectionRef
		Model               string
		Provider            string
		Scope               string
	}{
		Actor:               input.Actor,
		Thread:              input.Thread,
		Environment:         input.Environment,
		EnvironmentSnapshot: environmentSnapshot,
		Workspace:           workspace,
		Parent:              input.ParentProjection,
		Source:              input.SourceProjection,
		Model:               strings.TrimSpace(input.ThreadModel),
		Provider:            strings.TrimSpace(input.ThreadProvider),
		Scope:               strings.TrimSpace(scope),
	})
}

func workflowStartFingerprint(input StartWorkflowInput, definition model.WorkflowDefinition, prepared preparedWorkflowStart) (string, error) {
	workspaceHash := ""
	if prepared.Workspace != nil {
		var err error
		workspaceHash, err = hashWorkflowValue(prepared.Workspace)
		if err != nil {
			return "", err
		}
	}
	return hashWorkflowValue(struct {
		Definition       model.ResourceRef
		DefinitionHash   string
		Input            json.RawMessage
		Limits           model.WorkflowLimits
		CacheMode        string
		Thread           model.ThreadRef
		ThreadSnapshot   string
		Environment      model.ResourceRef
		WorkspaceHash    string
		ParentRunID      string
		RootRunID        string
		BudgetOwnerRunID string
		Depth            int
	}{
		Definition: definition.Ref(), DefinitionHash: definition.DefinitionHash, Input: prepared.InputJSON,
		Limits: prepared.Limits, CacheMode: prepared.CacheMode, Thread: input.Thread, ThreadSnapshot: prepared.ThreadSnapshotHash,
		Environment: input.Environment, WorkspaceHash: workspaceHash, ParentRunID: input.ParentRunID,
		RootRunID: input.RootRunID, BudgetOwnerRunID: input.BudgetOwnerRunID, Depth: input.Depth,
	})
}

func (s *Engine) existingWorkflowStart(ctx context.Context, input StartWorkflowInput, runID, fingerprint string) (*WorkflowStartResult, bool, error) {
	run, err := s.repo.GetRun(ctx, input.Actor, runID)
	if err == nil {
		return s.existingWorkflowStartResult(ctx, input, *run, fingerprint)
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}
	if strings.TrimSpace(input.ParentRunID) != "" {
		return nil, false, nil
	}
	return nil, false, s.workflowStartThreadAvailable(ctx, input)
}

func (s *Engine) existingWorkflowStartResult(ctx context.Context, input StartWorkflowInput, run model.Run, fingerprint string) (*WorkflowStartResult, bool, error) {
	if run.RuntimeKind != model.RuntimeKindWorkflow || run.RequestFingerprint != fingerprint || run.Thread != input.Thread {
		return nil, false, ErrTextRunIdempotencyConflict
	}
	steps, err := s.repo.ListRunSteps(ctx, run.RunID)
	if err != nil {
		return nil, false, err
	}
	if len(steps) == 0 {
		return nil, false, ErrNotFound
	}
	return &WorkflowStartResult{Run: run, Step: steps[0], Projection: TurnProjection{Input: run.InputProjection, Output: run.OutputProjection}}, true, nil
}

func (s *Engine) workflowStartThreadAvailable(ctx context.Context, input StartWorkflowInput) error {
	_, err := s.repo.GetActiveRun(ctx, input.Actor, input.Thread)
	if err == nil {
		return ErrTextRunAlreadyActive
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

func (s *Engine) persistWorkflowStart(ctx context.Context, input StartWorkflowInput, definition model.WorkflowDefinition, inputValue interface{}, runID, fingerprint string, prepared preparedWorkflowStart) (*WorkflowStartResult, error) {
	if s.unitOfWork == nil || s.turnProjections == nil {
		return nil, ErrHostProjectionUnavailable
	}
	now := s.now()
	rootRunID := firstNonEmptyString(strings.TrimSpace(input.RootRunID), runID)
	budgetOwnerRunID := firstNonEmptyString(strings.TrimSpace(input.BudgetOwnerRunID), rootRunID)
	stepID := deterministicWorkflowID("step", runID, definition.Root.ID)
	run := model.Run{
		RunID: runID, RequestID: strings.TrimSpace(input.RequestID), RuntimeKind: model.RuntimeKindWorkflow,
		Actor: input.Actor, Thread: input.Thread, Environment: input.Environment, WorkflowDefinition: definition.Ref(),
		RootRunID: rootRunID, ParentRunID: strings.TrimSpace(input.ParentRunID), Depth: input.Depth,
		Goal: definition.Name, RunConfigSnapshotJSON: prepared.ConfigJSON, RequestFingerprint: fingerprint,
		CurrentStepID: stepID, StartedBy: "user", Status: model.RunStatusRunning, StartedAt: now,
		RequestedModelName: strings.TrimSpace(input.ThreadModel), PlatformModelName: strings.TrimSpace(input.ThreadModel), Provider: strings.TrimSpace(input.ThreadProvider),
	}
	step := model.Step{
		StepID: stepID, RunID: runID, StepIndex: 0, Attempt: 1, NodeID: definition.Root.ID,
		ActivationPath: definition.Root.ID, LaneID: workflowRootScope, ActivationIndex: 0,
		Kind: model.WorkflowNodeSequence, Title: definition.Name, Description: definition.Description,
		Status: model.WorkflowStepStatusReady, StartedAt: now,
	}
	state := workflowRuntimeState{
		SemanticVersion: RuntimeSnapshotVersion, Input: inputValue,
		Scopes:      map[string]workflowScopeState{workflowRootScope: {Vars: map[string]interface{}{}, Outputs: map[string]interface{}{}}},
		Activations: map[string]workflowActivationState{}, Effects: map[string]workflowEffectState{}, Waits: map[string]model.WorkflowWait{}, Compensations: []model.WorkflowCompensation{},
	}
	stateJSON, varsJSON, waitsJSON, compensationJSON, budgetJSON, err := encodeWorkflowExecutionState(state, model.WorkflowBudget{Limits: prepared.Limits})
	if err != nil {
		return nil, err
	}
	execution := model.WorkflowExecution{
		RunID: runID, WorkflowID: definition.WorkflowID, WorkflowRevision: definition.Revision,
		DefinitionHash: definition.DefinitionHash, DependencyHash: definition.DependencyHash,
		RootRunID: rootRunID, BudgetOwnerRunID: budgetOwnerRunID, ParentRunID: input.ParentRunID, Depth: input.Depth,
		Version: 1, Status: model.WorkflowExecutionQueued, StateJSON: stateJSON, VarsJSON: varsJSON,
		WaitsJSON: waitsJSON, CompensationJSON: compensationJSON, BudgetJSON: budgetJSON,
		EnvironmentSnapshot: string(prepared.EnvironmentSnapshot), WorkspaceSnapshot: canonicalWorkflowOptionalJSON(prepared.Workspace),
		ThreadSnapshotHash: prepared.ThreadSnapshotHash, StartedAt: now,
	}
	continuation := runContinuation{
		SemanticVersion: RuntimeSnapshotVersion, SegmentKey: runID + ":workflow:1",
		Type: runContinuationWorkflowExecute, TargetStatus: model.RunStatusRunning, StepID: stepID,
	}
	checkpoint := newRunContinuationCheckpoint(run, stepID, "workflow_start", continuation)
	job := s.newWorkflowContinuationJob(ctx, run, *checkpoint, "workflow_start", now)
	var projection TurnProjection
	var snapshot *model.ContextSnapshot
	var savedEvents []model.Event
	err = s.unitOfWork.Within(ctx, func(txCtx context.Context) error {
		branch, branchErr := s.resolveMessageBranch(txCtx, input.Actor, input.Thread, input.ParentProjection, input.SourceProjection, input.BranchReason)
		if branchErr != nil {
			return branchErr
		}
		projection, branchErr = s.turnProjections.BeginTurn(txCtx, BeginTurnRequest{
			Actor: input.Actor, Thread: input.Thread, RunID: runID, ContentType: "application/json",
			Content: string(prepared.InputJSON), TokenEstimate: estimateTokens(string(prepared.InputJSON)),
			Parent: branch.Parent, Source: branch.Source, BranchReason: input.BranchReason,
		})
		if branchErr != nil {
			return branchErr
		}
		run.InputProjection, run.OutputProjection = projection.Input, projection.Output
		snapshot = s.newWorkflowContextSnapshot(run, prepared.InputJSON, prepared.ThreadSnapshotHash, now)
		checkpoint.ContextSnapshotID = snapshot.SnapshotID
		events := []model.Event{
			newRunEvent(run, "run.started", stepID, "Workflow run started", map[string]interface{}{"workflowRef": definition.Ref(), workflowPayloadRuntimeKind: model.RuntimeKindWorkflow}, nil),
			newRunEvent(run, "workflow.definition_frozen", stepID, "Workflow definition frozen", map[string]interface{}{"definitionHash": definition.DefinitionHash, "dependencyHash": definition.DependencyHash}, nil),
			newRunEvent(run, "workflow.budget_initialized", stepID, "Workflow budget initialized", map[string]interface{}{"limits": prepared.Limits}, nil),
			newRunEvent(run, "step.created", stepID, definition.Root.ID, map[string]interface{}{workflowPayloadNodeID: definition.Root.ID, workflowPayloadPath: definition.Root.ID}, nil),
			newRunEvent(run, "checkpoint.created", stepID, "Workflow start checkpoint", map[string]interface{}{workflowPayloadCheckpointID: checkpoint.CheckpointID}, nil),
		}
		savedEvents, branchErr = s.repo.CreateWorkflowRunStartBundle(txCtx, &run, &step, snapshot, nil, &execution, checkpoint, job, events)
		return branchErr
	})
	if err != nil {
		if errors.Is(err, ErrDuplicate) {
			if existing, found, loadErr := s.existingWorkflowStart(ctx, input, runID, fingerprint); loadErr != nil || found {
				return existing, loadErr
			}
		}
		return nil, err
	}
	s.publishRunEvents(run.RunID, savedEvents)
	s.wakeContinuationJobs()
	return &WorkflowStartResult{Run: run, Step: step, Projection: projection}, nil
}

func (s *Engine) newWorkflowContextSnapshot(run model.Run, inputJSON []byte, threadHash string, now time.Time) *model.ContextSnapshot {
	contentHash := sha256.Sum256(inputJSON)
	return &model.ContextSnapshot{
		SnapshotID: s.newRuntimeID("context"), RunID: run.RunID,
		ThreadPathHash: threadHash, ContentJSON: string(inputJSON), ContentHash: hex.EncodeToString(contentHash[:]),
		SchemaVersion: RuntimeSnapshotVersion, Actor: run.Actor, Thread: run.Thread, InputProjection: run.InputProjection,
		TokenEstimate: estimateTokens(string(inputJSON)), CreatedAt: now, UpdatedAt: now,
	}
}

func narrowWorkflowLimits(definition model.WorkflowLimits, requested *model.WorkflowLimits) (model.WorkflowLimits, error) {
	if requested == nil {
		return definition, nil
	}
	result := definition
	pairs := []struct {
		requested int
		target    *int
		maximum   int
	}{
		{requested.MaxNodeActivations, &result.MaxNodeActivations, definition.MaxNodeActivations},
		{requested.MaxChildRuns, &result.MaxChildRuns, definition.MaxChildRuns},
		{requested.MaxConcurrentRuns, &result.MaxConcurrentRuns, definition.MaxConcurrentRuns},
		{requested.MaxTotalLLMCalls, &result.MaxTotalLLMCalls, definition.MaxTotalLLMCalls},
		{requested.MaxTotalToolCalls, &result.MaxTotalToolCalls, definition.MaxTotalToolCalls},
		{requested.MaxDurationSeconds, &result.MaxDurationSeconds, definition.MaxDurationSeconds},
		{requested.MaxLoopIterations, &result.MaxLoopIterations, definition.MaxLoopIterations},
		{requested.MaxNestedDepth, &result.MaxNestedDepth, definition.MaxNestedDepth},
		{requested.MaxStateBytes, &result.MaxStateBytes, definition.MaxStateBytes},
	}
	for _, pair := range pairs {
		if pair.requested == 0 {
			continue
		}
		if pair.requested < 0 || pair.requested > pair.maximum {
			return model.WorkflowLimits{}, ErrWorkflowBudgetExceeded
		}
		*pair.target = pair.requested
	}
	if result.MaxConcurrentRuns > result.MaxChildRuns {
		return model.WorkflowLimits{}, ErrWorkflowBudgetExceeded
	}
	return result, nil
}

func normalizeWorkflowCacheMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", model.WorkflowCacheUse:
		return model.WorkflowCacheUse, nil
	case model.WorkflowCacheRefresh:
		return model.WorkflowCacheRefresh, nil
	case model.WorkflowCacheBypass:
		return model.WorkflowCacheBypass, nil
	default:
		return "", ErrInvalidInput
	}
}

func encodeWorkflowExecutionState(state workflowRuntimeState, budget model.WorkflowBudget) (string, string, string, string, string, error) {
	stateJSON, err := canonicalWorkflowJSON(state)
	if err != nil {
		return "", "", "", "", "", err
	}
	rootScope := state.Scopes[workflowRootScope]
	varsJSON, err := canonicalWorkflowJSON(rootScope.Vars)
	if err != nil {
		return "", "", "", "", "", err
	}
	waitsJSON, err := canonicalWorkflowJSON(state.Waits)
	if err != nil {
		return "", "", "", "", "", err
	}
	compensationJSON, err := canonicalWorkflowJSON(state.Compensations)
	if err != nil {
		return "", "", "", "", "", err
	}
	budgetJSON, err := canonicalWorkflowJSON(budget)
	if err != nil {
		return "", "", "", "", "", err
	}
	return string(stateJSON), string(varsJSON), string(waitsJSON), string(compensationJSON), string(budgetJSON), nil
}

func decodeWorkflowExecutionState(execution model.WorkflowExecution) (workflowRuntimeState, model.WorkflowBudget, error) {
	var state workflowRuntimeState
	var budget model.WorkflowBudget
	decoder := json.NewDecoder(strings.NewReader(execution.StateJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&state); err != nil || state.SemanticVersion != RuntimeSnapshotVersion {
		return workflowRuntimeState{}, model.WorkflowBudget{}, ErrRunSnapshotIncompatible
	}
	if err := json.Unmarshal([]byte(execution.BudgetJSON), &budget); err != nil {
		return workflowRuntimeState{}, model.WorkflowBudget{}, ErrRunSnapshotIncompatible
	}
	if state.Scopes == nil || state.Activations == nil || state.Waits == nil {
		return workflowRuntimeState{}, model.WorkflowBudget{}, ErrRunSnapshotIncompatible
	}
	if state.Effects == nil {
		state.Effects = make(map[string]workflowEffectState)
	}
	return state, budget, nil
}

func canonicalWorkflowOptionalJSON(value interface{}) string {
	if value == nil {
		return ""
	}
	raw, err := canonicalWorkflowJSON(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func (s *Engine) newWorkflowContinuationJob(ctx context.Context, run model.Run, checkpoint model.Checkpoint, source string, availableAt time.Time) *model.ContinuationJob {
	continuation, _ := decodeRunContinuation(checkpoint)
	digest := sha256.Sum256([]byte(continuation.SegmentKey))
	trace := s.captureTraceContext(ctx)
	return &model.ContinuationJob{
		JobID: "continuation_" + fmt.Sprintf("%x", digest[:16]), SegmentKey: continuation.SegmentKey,
		RunID: run.RunID, CheckpointID: checkpoint.CheckpointID, Actor: run.Actor, Source: strings.TrimSpace(source),
		Status: model.ContinuationJobQueued, MaxAttempts: 5, AvailableAt: availableAt,
		TraceParent: trace.TraceParent, TraceState: trace.TraceState,
	}
}

func (s *Engine) verifyWorkflowDependencies(ctx context.Context, input StartWorkflowInput, definition model.WorkflowDefinition) error {
	workspaceType, workspaceMode := workflowDependencyWorkspaceScope(input)
	for _, dependency := range definition.Dependencies {
		if err := s.verifyWorkflowDependency(ctx, input, dependency, workspaceType, workspaceMode); err != nil {
			return err
		}
	}
	return nil
}

func workflowDependencyWorkspaceScope(input StartWorkflowInput) (string, string) {
	if input.FrozenWorkspace != nil {
		return textRunWorkspaceScope(input.FrozenWorkspace)
	}
	if input.Workspace != nil {
		return strings.TrimSpace(input.Workspace.Type), ""
	}
	return "", ""
}

func (s *Engine) verifyWorkflowDependency(
	ctx context.Context,
	input StartWorkflowInput,
	dependency model.WorkflowDependency,
	workspaceType string,
	workspaceMode string,
) error {
	switch dependency.Kind {
	case model.WorkflowDependencyAgent:
		return s.verifyWorkflowAgentDependency(ctx, input.Actor, dependency)
	case model.WorkflowDependencyWorkflow:
		return s.verifyNestedWorkflowDependency(ctx, input.Actor, dependency)
	case model.WorkflowDependencyTool:
		return s.verifyWorkflowToolDependency(ctx, input, dependency, workspaceType, workspaceMode)
	default:
		return ErrWorkflowDependencyMissing
	}
}

func (s *Engine) verifyWorkflowAgentDependency(
	ctx context.Context,
	actor model.ActorRef,
	dependency model.WorkflowDependency,
) error {
	item, err := s.repo.GetAgentManifest(ctx, actor, dependency.Ref)
	if err != nil {
		return errors.Join(ErrWorkflowDependencyMissing, err)
	}
	hash, err := hashWorkflowValue(item)
	if err != nil || hash != dependency.Fingerprint {
		return ErrRunSnapshotIncompatible
	}
	return nil
}

func (s *Engine) verifyNestedWorkflowDependency(
	ctx context.Context,
	actor model.ActorRef,
	dependency model.WorkflowDependency,
) error {
	item, err := s.repo.GetWorkflowDefinition(ctx, actor, dependency.Ref)
	if err != nil {
		return errors.Join(ErrWorkflowDependencyMissing, err)
	}
	if item.DefinitionHash != dependency.Fingerprint {
		return ErrRunSnapshotIncompatible
	}
	return nil
}

func (s *Engine) verifyWorkflowToolDependency(
	ctx context.Context,
	input StartWorkflowInput,
	dependency model.WorkflowDependency,
	workspaceType string,
	workspaceMode string,
) error {
	if s.toolCatalog == nil {
		return ErrWorkflowDependencyMissing
	}
	items, unavailable, err := s.toolCatalog.ResolveAvailable(
		ctx,
		input.Actor,
		[]string{dependency.ToolKey},
		workspaceType,
		workspaceMode,
		input.ThreadModel,
	)
	if err != nil || len(unavailable) != 0 || len(items) != 1 || items[0].DefinitionVersion != dependency.DefinitionVersion {
		return ErrWorkflowDependencyMissing
	}
	hash, hashErr := hashWorkflowValue(items[0])
	if hashErr != nil || hash != dependency.Fingerprint {
		return ErrRunSnapshotIncompatible
	}
	return nil
}

func deterministicWorkflowID(prefix string, parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return prefix + "_" + hex.EncodeToString(digest[:16])
}

func (s *Engine) GetRunResult(ctx context.Context, actor model.ActorRef, runID string) (*model.RunResult, error) {
	if s == nil || s.repo == nil || !validActorRef(actor) || strings.TrimSpace(runID) == "" {
		return nil, ErrInvalidInput
	}
	return s.repo.GetRunResult(ctx, actor, strings.TrimSpace(runID))
}

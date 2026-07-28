package agentruntime

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

type workflowRunner struct {
	service      *Engine
	ctx          context.Context
	run          model.Run
	definition   model.WorkflowDefinition
	execution    model.WorkflowExecution
	effective    effectiveWorkflowConfig
	state        workflowRuntimeState
	budget       model.WorkflowBudget
	now          time.Time
	steps        map[string]model.Step
	stepOrder    []string
	interactions map[string]model.Interaction

	changedSteps     map[string]struct{}
	interactionRows  []model.Interaction
	events           []model.Event
	cacheEntries     []model.WorkflowCacheEntry
	progress         bool
	dispatchEffectID string
	drainingEffects  bool
	segment          workflowSegmentState

	terminalOutcome string
	terminalCode    string
	terminalMessage string
	result          *model.RunResult

	compensationContext interface{}
}

type workflowNodeFailure struct {
	Code    string
	Message string
}

func (e workflowNodeFailure) Error() string { return e.Message }

func (s *Engine) executeWorkflowContinuation(ctx context.Context, run model.Run, _ model.Checkpoint, continuation runContinuation, _ string) error {
	if continuation.Type != runContinuationWorkflowExecute {
		return ErrRunSnapshotIncompatible
	}
	current := run
	for {
		runner, err := s.loadWorkflowRunner(ctx, current)
		if err != nil {
			return err
		}
		if err = runner.advanceContinuation(current.Status); err != nil {
			return err
		}
		applied, err := runner.commit()
		if err != nil || !applied || runner.dispatchEffectID == "" {
			return err
		}
		if err = runner.dispatchClaimedWorkflowEffect(context.WithoutCancel(ctx)); err != nil {
			return err
		}
		latest, err := s.repo.GetRun(ctx, current.Actor, current.RunID)
		if err != nil {
			return err
		}
		current = *latest
	}
}

func (r *workflowRunner) advanceContinuation(runStatus string) error {
	if runStatus == model.RunStatusCancelling {
		r.state.CancelRequested = true
	}
	r.applyDurationLimit()
	if r.state.CancelRequested {
		settled, err := r.drainWorkflowEffects()
		if err != nil || !settled {
			return err
		}
		return r.advanceCancellation()
	}
	if r.state.ErrorMessage != "" {
		settled, err := r.drainWorkflowEffects()
		if err != nil || !settled {
			return err
		}
		return r.advanceFailure(workflowNodeFailure{Code: r.state.ErrorCode, Message: r.state.ErrorMessage})
	}
	return r.advanceRoot()
}

func (r *workflowRunner) applyDurationLimit() {
	now := r.service.now()
	if !r.workflowDeadlineExceededAt(now) {
		return
	}
	r.now = now
	r.state.ErrorCode = workflowFailureDurationExceeded
	r.state.ErrorMessage = ErrWorkflowBudgetExceeded.Error()
}

func (r *workflowRunner) advanceRoot() error {
	_, _, err := r.advanceNode(&r.definition.Root, r.definition.Root.ID, workflowRootScope, "")
	if err == nil && r.state.Returned {
		err = r.completeSuccessfulRun()
	}
	if err != nil {
		return r.advanceFailure(err)
	}
	return nil
}

func decodeEffectiveWorkflowConfig(run model.Run) (effectiveWorkflowConfig, error) {
	var effective effectiveWorkflowConfig
	decoder := json.NewDecoder(strings.NewReader(run.RunConfigSnapshotJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&effective); err != nil {
		return effectiveWorkflowConfig{}, ErrRunSnapshotIncompatible
	}
	if effective.SemanticVersion != RuntimeSnapshotVersion {
		return effectiveWorkflowConfig{}, ErrRunSnapshotIncompatible
	}
	return effective, nil
}

func workflowDefinitionMatchesSnapshot(definition model.WorkflowDefinition, effective effectiveWorkflowConfig) bool {
	return definition.DefinitionHash == effective.DefinitionHash && definition.DependencyHash == effective.DependencyHash
}

func workflowExecutionMatchesDefinition(execution model.WorkflowExecution, definition model.WorkflowDefinition) bool {
	return execution.DefinitionHash == definition.DefinitionHash && execution.DependencyHash == definition.DependencyHash
}

func newLoadedWorkflowRunner(service *Engine, ctx context.Context, run model.Run, definition model.WorkflowDefinition, execution model.WorkflowExecution, effective effectiveWorkflowConfig, state workflowRuntimeState, budget model.WorkflowBudget, stepRows []model.Step, interactionRows []model.Interaction) *workflowRunner {
	now := service.now()
	runner := &workflowRunner{
		service: service, ctx: ctx, run: run, definition: definition, execution: execution, effective: effective,
		state: state, budget: budget, now: now, steps: make(map[string]model.Step, len(stepRows)),
		interactions: make(map[string]model.Interaction, len(interactionRows)), changedSteps: make(map[string]struct{}),
		segment: workflowSegmentState{
			startedAt:        now,
			startActivations: budget.NodeActivations,
			policy:           service.workflowSegmentPolicy(),
		},
	}
	sort.Slice(stepRows, func(i, j int) bool { return stepRows[i].StepIndex < stepRows[j].StepIndex })
	for _, step := range stepRows {
		runner.steps[step.StepID] = step
		runner.stepOrder = append(runner.stepOrder, step.StepID)
	}
	for _, interaction := range interactionRows {
		runner.interactions[interaction.InteractionID] = interaction
	}
	return runner
}

func (s *Engine) loadWorkflowRunner(ctx context.Context, run model.Run) (*workflowRunner, error) {
	if run.RuntimeKind != model.RuntimeKindWorkflow {
		return nil, ErrRunSnapshotIncompatible
	}
	effective, err := decodeEffectiveWorkflowConfig(run)
	if err != nil {
		return nil, err
	}
	definition, err := s.repo.GetWorkflowDefinition(ctx, run.Actor, effective.Definition)
	if err != nil {
		return nil, err
	}
	if !workflowDefinitionMatchesSnapshot(*definition, effective) {
		return nil, ErrRunSnapshotIncompatible
	}
	execution, err := s.repo.GetWorkflowExecution(ctx, run.Actor, run.RunID)
	if err != nil {
		return nil, err
	}
	if !workflowExecutionMatchesDefinition(*execution, *definition) {
		return nil, ErrRunSnapshotIncompatible
	}
	state, budget, err := decodeWorkflowExecutionState(*execution)
	if err != nil {
		return nil, err
	}
	stepRows, err := s.repo.ListRunSteps(ctx, run.RunID)
	if err != nil {
		return nil, err
	}
	interactionRows, err := s.repo.ListRunInteractions(ctx, run.Actor, run.RunID)
	if err != nil {
		return nil, err
	}
	return newLoadedWorkflowRunner(s, ctx, run, *definition, *execution, effective, state, budget, stepRows, interactionRows), nil
}

type workflowNodeAdvancer func(*workflowRunner, *model.WorkflowNode, workflowActivationState) (interface{}, bool, error)

func (r *workflowRunner) existingActivationResult(node *model.WorkflowNode, activation workflowActivationState) (interface{}, bool, bool, error) {
	switch activation.Status {
	case model.WorkflowStepStatusCompleted, model.WorkflowStepStatusCompensated:
		return activation.Output, true, true, nil
	case model.WorkflowStepStatusFailed:
		return nil, false, true, workflowNodeFailure{Code: activation.ErrorCode, Message: activation.ErrorMessage}
	case model.WorkflowStepStatusWaiting:
		value, complete, err := r.resumeWaitingNode(node, activation)
		return value, complete, true, err
	default:
		return nil, false, false, nil
	}
}

func (r *workflowRunner) cachedActivationResult(node *model.WorkflowNode, activation workflowActivationState) (interface{}, bool, bool, error) {
	output, hit, err := r.tryWorkflowCache(*node, activation)
	if err != nil {
		return nil, false, true, err
	}
	if !hit {
		return nil, false, false, nil
	}
	value, complete, err := r.completeActivation(*node, activation, output)
	return value, complete, true, err
}

func (r *workflowRunner) advanceNode(node *model.WorkflowNode, path, scopeKey, parentPath string) (value interface{}, complete bool, resultErr error) {
	if node == nil {
		return nil, false, workflowNodeFailure{Code: "workflow_node_missing", Message: "workflow node is missing"}
	}
	previousCtx := r.ctx
	nodeCtx, span := r.startWorkflowNodeSpan(*node, path, scopeKey)
	r.ctx = nodeCtx
	defer func() {
		r.ctx = previousCtx
		span.SetAttributes(Bool("workflow.node.complete", complete))
		if resultErr != nil {
			span.RecordError(resultErr)
		}
		span.End()
	}()
	blocked, err := r.workflowNodeAdvanceBlocked(path)
	if err != nil || blocked {
		return nil, false, err
	}
	activation, err := r.ensureActivation(*node, path, scopeKey, parentPath)
	if err != nil {
		return nil, false, err
	}
	if existingValue, existingComplete, handled, existingErr := r.existingActivationResult(node, activation); handled {
		return existingValue, existingComplete, existingErr
	}
	if cachedValue, cachedComplete, handled, cacheErr := r.cachedActivationResult(node, activation); handled {
		return cachedValue, cachedComplete, cacheErr
	}
	advance, ok := workflowNodeAdvancerFor(node.Type)
	if !ok {
		return nil, false, r.failActivation(*node, activation, "workflow_node_unknown", "unknown workflow node")
	}
	return advance(r, node, activation)
}

func (r *workflowRunner) workflowNodeAdvanceBlocked(path string) (bool, error) {
	if !r.drainingEffects {
		if r.workflowDeadlineExceededAt(r.service.now()) {
			return true, workflowNodeFailure{Code: workflowFailureDurationExceeded, Message: ErrWorkflowBudgetExceeded.Error()}
		}
		return r.shouldYieldWorkflowSegment() || r.dispatchEffectID != "", nil
	}
	if r.dispatchEffectID != "" {
		return true, nil
	}
	_, exists := r.state.Activations[path]
	return !exists, nil
}

func workflowNodeAdvancerFor(nodeType string) (workflowNodeAdvancer, bool) {
	advancers := map[string]workflowNodeAdvancer{
		model.WorkflowNodeSequence:    (*workflowRunner).advanceSequence,
		model.WorkflowNodeSet:         (*workflowRunner).advanceSet,
		model.WorkflowNodeLog:         (*workflowRunner).advanceLog,
		model.WorkflowNodeIf:          (*workflowRunner).advanceIf,
		model.WorkflowNodeLoop:        (*workflowRunner).advanceLoop,
		model.WorkflowNodeParallel:    (*workflowRunner).advanceParallel,
		model.WorkflowNodeForEach:     (*workflowRunner).advanceForEach,
		model.WorkflowNodePipeline:    (*workflowRunner).advancePipeline,
		model.WorkflowNodeInteraction: (*workflowRunner).advanceInteraction,
		model.WorkflowNodeTimer:       (*workflowRunner).advanceTimer,
		model.WorkflowNodeAgent:       (*workflowRunner).advanceAgent,
		model.WorkflowNodeTool:        (*workflowRunner).advanceTool,
		model.WorkflowNodeWorkflow:    (*workflowRunner).advanceNestedWorkflow,
		model.WorkflowNodeCompensate:  (*workflowRunner).advanceCompensate,
		model.WorkflowNodeReturn:      (*workflowRunner).advanceReturn,
	}
	advance, ok := advancers[nodeType]
	return advance, ok
}

func (r *workflowRunner) ensureActivation(node model.WorkflowNode, path, scopeKey, parentPath string) (workflowActivationState, error) {
	if activation, ok := r.state.Activations[path]; ok {
		return activation, nil
	}
	r.budget.NodeActivations++
	if r.budget.NodeActivations > r.budget.Limits.MaxNodeActivations {
		return workflowActivationState{}, workflowNodeFailure{Code: "workflow_node_budget_exceeded", Message: ErrWorkflowBudgetExceeded.Error()}
	}
	if _, ok := r.state.Scopes[scopeKey]; !ok {
		return workflowActivationState{}, workflowNodeFailure{Code: "workflow_scope_missing", Message: "workflow scope is missing"}
	}
	stepID := deterministicWorkflowID("step", r.run.RunID, path)
	parentStepID := ""
	if parent, ok := r.state.Activations[parentPath]; ok {
		parentStepID = parent.StepID
	}
	step, exists := r.steps[stepID]
	if !exists {
		step = model.Step{
			StepID: stepID, RunID: r.run.RunID, ParentStepID: parentStepID, StepIndex: len(r.stepOrder),
			Attempt: 1, NodeID: node.ID, ActivationPath: path, LaneID: scopeKey, ActivationIndex: r.budget.NodeActivations - 1,
			Kind: node.Type, Title: firstNonEmptyString(node.Title, node.ID), Status: model.WorkflowStepStatusRunning, StartedAt: r.now,
		}
		r.steps[stepID] = step
		r.stepOrder = append(r.stepOrder, stepID)
	} else {
		step.Status = model.WorkflowStepStatusRunning
		if step.StartedAt.IsZero() {
			step.StartedAt = r.now
		}
		r.steps[stepID] = step
	}
	r.changedSteps[stepID] = struct{}{}
	activation := workflowActivationState{
		NodeID: node.ID, Path: path, ScopeKey: scopeKey, StepID: stepID,
		Status: model.WorkflowStepStatusRunning, Attempt: 1,
	}
	r.state.Activations[path] = activation
	r.run.CurrentStepID = stepID
	r.events = append(r.events,
		newRunEvent(r.run, "step.started", stepID, node.ID, map[string]interface{}{workflowPayloadNodeID: node.ID, workflowPayloadPath: path, workflowPayloadLane: scopeKey}, nil),
		newRunEvent(r.run, "workflow.node.started", stepID, node.ID, map[string]interface{}{workflowPayloadNodeID: node.ID, workflowPayloadPath: path, workflowPayloadLane: scopeKey}, nil),
	)
	r.progress = true
	return activation, nil
}

func (r *workflowRunner) saveActivation(activation workflowActivationState) {
	r.state.Activations[activation.Path] = activation
}

func (r *workflowRunner) expressionContext(scopeKey string) workflowExpressionContext {
	scope := r.state.Scopes[scopeKey]
	return workflowExpressionContext{
		Input: r.state.Input, Vars: scope.Vars, Steps: scope.Outputs,
		Item: scope.Item, Index: scope.Index, Error: workflowErrorValue(r.state.ErrorCode, r.state.ErrorMessage),
		Compensation: r.compensationContext,
	}
}

func workflowErrorValue(code, message string) interface{} {
	if strings.TrimSpace(code) == "" && strings.TrimSpace(message) == "" {
		return nil
	}
	return map[string]interface{}{workflowPayloadCode: code, workflowPayloadMessage: message}
}

func (r *workflowRunner) evaluate(expression *model.WorkflowExpr, scopeKey string) (interface{}, error) {
	if expression == nil {
		return nil, ErrWorkflowExpressionInvalid
	}
	return r.service.evaluateWorkflowExpression(*expression, r.expressionContext(scopeKey))
}

func (r *workflowRunner) completeActivation(node model.WorkflowNode, activation workflowActivationState, output interface{}) (interface{}, bool, error) {
	activation.Status = model.WorkflowStepStatusCompleted
	activation.Output = output
	r.execution.CompletionSeq++
	activation.CompletionOrder = r.execution.CompletionSeq
	r.saveActivation(activation)
	scope := r.state.Scopes[activation.ScopeKey]
	if scope.Outputs == nil {
		scope.Outputs = make(map[string]interface{})
	}
	scope.Outputs[node.ID] = output
	r.state.Scopes[activation.ScopeKey] = scope
	step := r.steps[activation.StepID]
	step.Status = model.WorkflowStepStatusCompleted
	step.CompletionOrder = int(activation.CompletionOrder)
	step.ResultSummary = model.WorkflowStepStatusCompleted
	if raw, err := canonicalWorkflowJSON(output); err == nil {
		step.OutputJSON = string(raw)
	}
	endedAt := r.now
	step.EndedAt = &endedAt
	r.steps[activation.StepID] = step
	r.changedSteps[activation.StepID] = struct{}{}
	delete(r.state.Waits, activation.WaitID)
	r.events = append(r.events,
		newRunEvent(r.run, "workflow.node.completed", activation.StepID, node.ID, map[string]interface{}{workflowPayloadNodeID: node.ID, workflowPayloadPath: activation.Path, "completionOrder": activation.CompletionOrder}, nil),
		newRunEvent(r.run, "step.completed", activation.StepID, node.ID, map[string]interface{}{workflowPayloadNodeID: node.ID, workflowPayloadPath: activation.Path}, nil),
	)
	r.progress = true
	if node.Cache != nil && node.Cache.Enabled {
		r.storeWorkflowCache(node, activation, output)
	}
	return output, true, nil
}

func (r *workflowRunner) failActivation(node model.WorkflowNode, activation workflowActivationState, code, message string) error {
	activation.Status, activation.ErrorCode, activation.ErrorMessage = model.WorkflowStepStatusFailed, strings.TrimSpace(code), strings.TrimSpace(message)
	r.saveActivation(activation)
	step := r.steps[activation.StepID]
	step.Status, step.ErrorJSON, step.ResultSummary = model.WorkflowStepStatusFailed, mustRunJSON(map[string]interface{}{workflowPayloadCode: code, workflowPayloadMessage: message}), message
	endedAt := r.now
	step.EndedAt = &endedAt
	r.steps[activation.StepID] = step
	r.changedSteps[activation.StepID] = struct{}{}
	delete(r.state.Waits, activation.WaitID)
	event := newRunEvent(r.run, "workflow.node.failed", activation.StepID, message, map[string]interface{}{workflowPayloadNodeID: node.ID, workflowPayloadPath: activation.Path, workflowPayloadCode: code}, nil)
	event.ErrorJSON = step.ErrorJSON
	r.events = append(r.events, event, newRunEvent(r.run, "step.failed", activation.StepID, message, map[string]interface{}{workflowPayloadNodeID: node.ID, workflowPayloadPath: activation.Path}, nil))
	r.progress = true
	return workflowNodeFailure{Code: code, Message: message}
}

func (r *workflowRunner) advanceReturn(node *model.WorkflowNode, activation workflowActivationState) (interface{}, bool, error) {
	value, err := r.evaluate(node.Value, activation.ScopeKey)
	if err != nil {
		return nil, false, r.failActivation(*node, activation, "workflow_expression_failed", err.Error())
	}
	presentation := ""
	if node.Presentation != nil {
		presented, presentErr := r.evaluate(node.Presentation, activation.ScopeKey)
		if presentErr != nil {
			return nil, false, r.failActivation(*node, activation, "workflow_expression_failed", presentErr.Error())
		}
		var ok bool
		presentation, ok = presented.(string)
		if !ok {
			return nil, false, r.failActivation(*node, activation, "workflow_presentation_invalid", "return presentation must be a string")
		}
	}
	r.state.Result, r.state.Presentation, r.state.Returned = value, presentation, true
	return r.completeActivation(*node, activation, value)
}

func (r *workflowRunner) completeSuccessfulRun() error {
	if err := validateWorkflowJSON(r.definition.OutputSchema, r.state.Result); err != nil {
		return r.advanceFailure(workflowNodeFailure{Code: "workflow_result_schema_invalid", Message: err.Error()})
	}
	canonical, err := canonicalWorkflowJSON(r.state.Result)
	if err != nil {
		return err
	}
	schemaHash, err := hashWorkflowValue(r.definition.OutputSchema)
	if err != nil {
		return err
	}
	contentHash, err := hashWorkflowValue(struct {
		Value        json.RawMessage
		Presentation string
	}{canonical, r.state.Presentation})
	if err != nil {
		return err
	}
	r.result = &model.RunResult{
		RunID: r.run.RunID, RuntimeKind: model.RuntimeKindWorkflow, CanonicalJSON: string(canonical),
		Presentation: r.state.Presentation, SchemaHash: schemaHash, ContentHash: contentHash,
	}
	r.terminalOutcome = model.TerminalCompleted
	return nil
}

func (r *workflowRunner) advanceFailure(cause error) error {
	if cause == nil {
		cause = workflowNodeFailure{Code: "workflow_failed", Message: "workflow failed"}
	}
	r.state.ErrorCode, r.state.ErrorMessage = workflowFailureCode(cause), cause.Error()
	r.cancelNonChildWaits("Workflow stopped after a failure")
	r.cancelRuntimeOwnedChildren()
	if !r.settleRuntimeOwnedChildren("Workflow child cancelled after a failure") {
		return nil
	}
	if len(r.state.Compensations) > 0 {
		return r.advanceCompensations(false)
	}
	r.terminalOutcome, r.terminalCode, r.terminalMessage = model.TerminalFailed, r.state.ErrorCode, r.state.ErrorMessage
	return nil
}

func (r *workflowRunner) advanceCancellation() error {
	r.state.ErrorCode, r.state.ErrorMessage = "workflow_cancelled", "workflow run was cancelled"
	r.cancelNonChildWaits("Workflow run was cancelled")
	r.cancelRuntimeOwnedChildren()
	if !r.settleRuntimeOwnedChildren("Workflow child cancelled by parent") {
		return nil
	}
	if len(r.state.Compensations) > 0 {
		return r.advanceCompensations(true)
	}
	r.terminalOutcome, r.terminalCode, r.terminalMessage = model.TerminalCancelled, r.state.ErrorCode, r.state.ErrorMessage
	return nil
}

func (r *workflowRunner) advanceCompensations(cancelled bool) error {
	for index := len(r.state.Compensations) - 1; index >= 0; index-- {
		compensation := r.state.Compensations[index]
		if compensation.Status == model.WorkflowCompensationCompleted {
			continue
		}
		r.compensationContext = map[string]interface{}{workflowPayloadActivationKey: compensation.ActivationKey, workflowPayloadCompletionSeq: compensation.CompletionSeq}
		scopeKey := "compensation/" + compensation.ActivationKey
		if _, exists := r.state.Scopes[scopeKey]; !exists {
			scope, err := cloneWorkflowScope(r.state.Scopes["root"])
			if err != nil {
				return err
			}
			r.state.Scopes[scopeKey] = scope
		}
		path := scopeKey + "/" + compensation.Undo.ID
		_, complete, err := r.advanceNode(&compensation.Undo, path, scopeKey, "")
		if err != nil {
			return r.suspendCompensation(index, compensation, err)
		}
		if !complete {
			compensation.Status = model.WorkflowCompensationRunning
			r.state.Compensations[index] = compensation
			return nil
		}
		compensation.Status, compensation.Error = model.WorkflowCompensationCompleted, ""
		compensation.Attempt++
		r.state.Compensations[index] = compensation
		r.events = append(r.events, newRunEvent(r.run, "workflow.compensation.completed", "", compensation.ActivationKey, map[string]interface{}{workflowPayloadActivationKey: compensation.ActivationKey, workflowPayloadCompletionSeq: compensation.CompletionSeq}, nil))
	}
	if cancelled {
		r.terminalOutcome, r.terminalCode, r.terminalMessage = model.TerminalCancelled, r.state.ErrorCode, r.state.ErrorMessage
	} else {
		r.terminalOutcome, r.terminalCode, r.terminalMessage = model.TerminalFailed, r.state.ErrorCode, r.state.ErrorMessage
	}
	return nil
}

func (r *workflowRunner) suspendCompensation(index int, compensation model.WorkflowCompensation, cause error) error {
	compensation.Status, compensation.Error, compensation.Attempt = model.WorkflowCompensationFailed, cause.Error(), compensation.Attempt+1
	r.state.Compensations[index] = compensation
	r.terminalOutcome, r.terminalCode, r.terminalMessage = model.RunStatusSuspended, "workflow_compensation_failed", cause.Error()
	return nil
}

func (r *workflowRunner) advanceCompensate(node *model.WorkflowNode, activation workflowActivationState) (interface{}, bool, error) {
	value, complete, err := r.advanceNode(node.Do, activation.Path+"/do/"+node.Do.ID, activation.ScopeKey, activation.Path)
	if err != nil {
		return nil, false, r.failActivation(*node, activation, workflowFailureCode(err), err.Error())
	}
	if !complete {
		return nil, false, nil
	}
	for _, existing := range r.state.Compensations {
		if existing.ActivationKey == activation.Path {
			return r.completeActivation(*node, activation, value)
		}
	}
	r.execution.CompletionSeq++
	r.state.Compensations = append(r.state.Compensations, model.WorkflowCompensation{
		ActivationKey: activation.Path, CompletionSeq: r.execution.CompletionSeq, Undo: *node.Undo, Status: model.WorkflowCompensationPending,
	})
	r.events = append(r.events, newRunEvent(r.run, "workflow.compensation.registered", activation.StepID, node.ID, map[string]interface{}{workflowPayloadActivationKey: activation.Path, workflowPayloadCompletionSeq: r.execution.CompletionSeq}, nil))
	return r.completeActivation(*node, activation, value)
}

var workflowContinuationStatuses = map[string]struct{}{
	model.RunStatusRunning:        {},
	model.RunStatusCancelling:     {},
	model.RunStatusCompensating:   {},
	model.RunStatusWaitingHandoff: {},
}

type workflowWaitSummary struct {
	earliest       *time.Time
	hasInteraction bool
	hasChild       bool
}

type workflowChildSettlement struct {
	llmCalls       int
	toolCalls      int
	actualChildren int
}

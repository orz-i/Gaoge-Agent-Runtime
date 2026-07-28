package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func (r *workflowRunner) advanceSequence(node *model.WorkflowNode, activation workflowActivationState) (interface{}, bool, error) {
	for activation.Cursor < len(node.Children) {
		child := &node.Children[activation.Cursor]
		value, complete, err := r.advanceNode(child, activation.Path+"/"+child.ID, activation.ScopeKey, activation.Path)
		if err != nil {
			return nil, false, r.failActivation(*node, activation, workflowFailureCode(err), err.Error())
		}
		if !complete {
			return nil, false, nil
		}
		activation.Cursor++
		activation.Output = value
		r.saveActivation(activation)
		if r.state.Returned {
			activation.Cursor = len(node.Children)
			break
		}
	}
	return r.completeActivation(*node, activation, activation.Output)
}

func (r *workflowRunner) advanceSet(node *model.WorkflowNode, activation workflowActivationState) (interface{}, bool, error) {
	keys := make([]string, 0, len(node.Assignments))
	for key := range node.Assignments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make(map[string]interface{}, len(keys))
	for _, key := range keys {
		value, err := r.service.evaluateWorkflowExpression(node.Assignments[key], r.expressionContext(activation.ScopeKey))
		if err != nil {
			return nil, false, r.failActivation(*node, activation, "workflow_expression_failed", err.Error())
		}
		values[key] = value
	}
	scope := r.state.Scopes[activation.ScopeKey]
	if scope.Vars == nil {
		scope.Vars = make(map[string]interface{})
	}
	for _, key := range keys {
		scope.Vars[key] = values[key]
	}
	r.state.Scopes[activation.ScopeKey] = scope
	return r.completeActivation(*node, activation, values)
}

func (r *workflowRunner) advanceLog(node *model.WorkflowNode, activation workflowActivationState) (interface{}, bool, error) {
	message, err := r.evaluate(node.Message, activation.ScopeKey)
	if err != nil {
		return nil, false, r.failActivation(*node, activation, "workflow_expression_failed", err.Error())
	}
	text, ok := message.(string)
	if !ok {
		return nil, false, r.failActivation(*node, activation, "workflow_log_message_invalid", "log message must be a string")
	}
	var data interface{}
	if node.Data != nil {
		data, err = r.evaluate(node.Data, activation.ScopeKey)
		if err != nil {
			return nil, false, r.failActivation(*node, activation, "workflow_expression_failed", err.Error())
		}
	}
	r.events = append(r.events, newRunEvent(r.run, "workflow.log", activation.StepID, text, map[string]interface{}{"level": node.Level, "data": data}, nil))
	return r.completeActivation(*node, activation, data)
}

func (r *workflowRunner) advanceIf(node *model.WorkflowNode, activation workflowActivationState) (interface{}, bool, error) {
	if activation.Cursor == 0 {
		condition, err := r.evaluate(node.Condition, activation.ScopeKey)
		if err != nil {
			return nil, false, r.failActivation(*node, activation, "workflow_expression_failed", err.Error())
		}
		boolean, ok := condition.(bool)
		if !ok {
			return nil, false, r.failActivation(*node, activation, "workflow_condition_invalid", "if condition must be boolean")
		}
		if boolean {
			activation.Cursor = 1
		} else {
			activation.Cursor = 2
		}
		r.saveActivation(activation)
	}
	selected := node.Then
	label := "then"
	if activation.Cursor == 2 {
		selected, label = node.Else, "else"
	}
	if selected == nil {
		return r.completeActivation(*node, activation, nil)
	}
	value, complete, err := r.advanceNode(selected, activation.Path+"/"+label+"/"+selected.ID, activation.ScopeKey, activation.Path)
	if err != nil {
		return nil, false, r.failActivation(*node, activation, workflowFailureCode(err), err.Error())
	}
	if !complete {
		return nil, false, nil
	}
	return r.completeActivation(*node, activation, value)
}

func (r *workflowRunner) advanceLoop(node *model.WorkflowNode, activation workflowActivationState) (interface{}, bool, error) {
	for {
		condition, err := r.evaluate(node.Condition, activation.ScopeKey)
		if err != nil {
			return nil, false, r.failActivation(*node, activation, "workflow_expression_failed", err.Error())
		}
		boolean, ok := condition.(bool)
		if !ok {
			return nil, false, r.failActivation(*node, activation, "workflow_condition_invalid", "loop condition must be boolean")
		}
		if !boolean {
			return r.completeActivation(*node, activation, activation.Results)
		}
		if activation.Iteration >= node.MaxIterations || r.budget.LoopIterations >= r.budget.Limits.MaxLoopIterations {
			return nil, false, r.failActivation(*node, activation, "workflow_loop_budget_exceeded", ErrWorkflowBudgetExceeded.Error())
		}
		path := fmt.Sprintf("%s/iteration:%d/%s", activation.Path, activation.Iteration, node.Body.ID)
		value, complete, childErr := r.advanceNode(node.Body, path, activation.ScopeKey, activation.Path)
		if childErr != nil {
			return nil, false, r.failActivation(*node, activation, workflowFailureCode(childErr), childErr.Error())
		}
		if !complete {
			return nil, false, nil
		}
		activation.Results = append(activation.Results, value)
		activation.Iteration++
		r.budget.LoopIterations++
		r.saveActivation(activation)
	}
}

func (r *workflowRunner) advanceParallel(node *model.WorkflowNode, activation workflowActivationState) (interface{}, bool, error) {
	if activation.Results == nil {
		activation.Results = make([]interface{}, len(node.Branches))
		activation.ItemCursors = make([]int, len(node.Branches))
		r.saveActivation(activation)
	}
	allComplete := true
	for index := range node.Branches {
		complete, err := r.advanceParallelBranch(node, &activation, index)
		if err != nil {
			return nil, false, err
		}
		allComplete = allComplete && complete
	}
	if !allComplete {
		return nil, false, nil
	}
	return r.completeActivation(*node, activation, activation.Results)
}

func (r *workflowRunner) advanceParallelBranch(node *model.WorkflowNode, activation *workflowActivationState, index int) (bool, error) {
	if activation.ItemCursors[index] == 1 {
		return true, nil
	}
	scopeKey := fmt.Sprintf("%s/lane:%d", activation.Path, index)
	if err := r.ensureIsolatedWorkflowScope(activation.ScopeKey, scopeKey); err != nil {
		return false, err
	}
	child := &node.Branches[index]
	value, complete, err := r.advanceNode(child, activation.Path+fmt.Sprintf("/branch:%d/", index)+child.ID, scopeKey, activation.Path)
	if err != nil {
		if node.FailurePolicy == model.WorkflowFailureFailFast {
			r.cancelRuntimeOwnedChildren()
			return false, r.failActivation(*node, *activation, workflowFailureCode(err), err.Error())
		}
		activation.Results[index] = workflowCollectEnvelope(nil, err)
		activation.ItemCursors[index] = 1
		r.saveActivation(*activation)
		return true, nil
	}
	if !complete {
		return false, nil
	}
	activation.Results[index] = workflowMaybeCollect(node.FailurePolicy, value)
	activation.ItemCursors[index] = 1
	r.saveActivation(*activation)
	return true, nil
}

func (r *workflowRunner) ensureIsolatedWorkflowScope(parentScopeKey, scopeKey string) error {
	if _, exists := r.state.Scopes[scopeKey]; exists {
		return nil
	}
	scope, err := cloneWorkflowScope(r.state.Scopes[parentScopeKey])
	if err != nil {
		return err
	}
	r.state.Scopes[scopeKey] = scope
	return nil
}

func (r *workflowRunner) advanceForEach(node *model.WorkflowNode, activation workflowActivationState) (interface{}, bool, error) {
	if err := r.initializeCollectionActivation(node, &activation, "forEach"); err != nil {
		return nil, false, err
	}
	active := countWorkflowCursors(activation.ItemCursors, 1, 1)
	for index, item := range activation.Items {
		if activation.ItemCursors[index] == 2 {
			continue
		}
		if activation.ItemCursors[index] == 0 && active >= node.MaxConcurrency {
			continue
		}
		var err error
		active, err = r.advanceForEachItem(node, &activation, index, item, active)
		if err != nil {
			return nil, false, err
		}
	}
	if !workflowCursorsComplete(activation.ItemCursors, 2) {
		return nil, false, nil
	}
	return r.completeActivation(*node, activation, activation.Results)
}

func (r *workflowRunner) advanceForEachItem(node *model.WorkflowNode, activation *workflowActivationState, index int, item interface{}, active int) (int, error) {
	scopeKey := fmt.Sprintf("%s/item:%d", activation.Path, index)
	if err := r.ensureWorkflowItemScope(activation.ScopeKey, scopeKey, item, index); err != nil {
		return active, err
	}
	if activation.ItemCursors[index] == 0 {
		activation.ItemCursors[index] = 1
		active++
		r.saveActivation(*activation)
	}
	value, complete, err := r.advanceNode(node.Body, scopeKey+"/"+node.Body.ID, scopeKey, activation.Path)
	if err != nil {
		return r.finishConcurrentItemFailure(node, activation, index, 2, active, err)
	}
	if !complete {
		return active, nil
	}
	activation.Results[index] = workflowMaybeCollect(node.FailurePolicy, value)
	activation.ItemCursors[index] = 2
	r.saveActivation(*activation)
	return active - 1, nil
}

func (r *workflowRunner) advancePipeline(node *model.WorkflowNode, activation workflowActivationState) (interface{}, bool, error) {
	if err := r.initializeCollectionActivation(node, &activation, "pipeline"); err != nil {
		return nil, false, err
	}
	active := countWorkflowCursors(activation.ItemCursors, 1, len(node.Stages))
	doneCursor := len(node.Stages) + 1
	for index, item := range activation.Items {
		cursor := activation.ItemCursors[index]
		if cursor == doneCursor {
			continue
		}
		if cursor == 0 && active >= node.MaxConcurrency {
			continue
		}
		var err error
		active, err = r.advancePipelineItem(node, &activation, index, item, active, doneCursor)
		if err != nil {
			return nil, false, err
		}
	}
	if !workflowCursorsComplete(activation.ItemCursors, doneCursor) {
		return nil, false, nil
	}
	return r.completeActivation(*node, activation, activation.Results)
}

func (r *workflowRunner) advancePipelineItem(node *model.WorkflowNode, activation *workflowActivationState, index int, item interface{}, active, doneCursor int) (int, error) {
	scopeKey := fmt.Sprintf("%s/item:%d", activation.Path, index)
	if err := r.ensureWorkflowItemScope(activation.ScopeKey, scopeKey, item, index); err != nil {
		return active, err
	}
	cursor := activation.ItemCursors[index]
	if cursor == 0 {
		cursor = 1
		active++
		activation.ItemCursors[index] = cursor
		r.saveActivation(*activation)
	}
	stage := &node.Stages[cursor-1]
	value, complete, err := r.advanceNode(stage, fmt.Sprintf("%s/item:%d/stage:%d/%s", activation.Path, index, cursor-1, stage.ID), scopeKey, activation.Path)
	if err != nil {
		return r.finishConcurrentItemFailure(node, activation, index, doneCursor, active, err)
	}
	if !complete {
		return active, nil
	}
	scope := r.state.Scopes[scopeKey]
	scope.Item = value
	r.state.Scopes[scopeKey] = scope
	cursor++
	activation.ItemCursors[index] = cursor
	if cursor == doneCursor {
		activation.Results[index] = workflowMaybeCollect(node.FailurePolicy, value)
		active--
	}
	r.saveActivation(*activation)
	return active, nil
}

func (r *workflowRunner) initializeCollectionActivation(node *model.WorkflowNode, activation *workflowActivationState, kind string) error {
	if activation.Items != nil {
		return nil
	}
	value, err := r.evaluate(node.ItemsExpr, activation.ScopeKey)
	if err != nil {
		return r.failActivation(*node, *activation, "workflow_expression_failed", err.Error())
	}
	items, ok := value.([]interface{})
	if !ok {
		return r.failActivation(*node, *activation, "workflow_items_invalid", kind+" items must be an array")
	}
	activation.Items = items
	activation.Results = make([]interface{}, len(items))
	activation.ItemCursors = make([]int, len(items))
	r.saveActivation(*activation)
	return nil
}

func (r *workflowRunner) ensureWorkflowItemScope(parentScopeKey, scopeKey string, item interface{}, index int) error {
	if _, exists := r.state.Scopes[scopeKey]; exists {
		return nil
	}
	scope, err := cloneWorkflowScope(r.state.Scopes[parentScopeKey])
	if err != nil {
		return err
	}
	itemIndex := index
	scope.Item, scope.Index = item, &itemIndex
	r.state.Scopes[scopeKey] = scope
	return nil
}

func (r *workflowRunner) finishConcurrentItemFailure(node *model.WorkflowNode, activation *workflowActivationState, index, doneCursor, active int, cause error) (int, error) {
	if node.FailurePolicy == model.WorkflowFailureFailFast {
		r.cancelRuntimeOwnedChildren()
		return active, r.failActivation(*node, *activation, workflowFailureCode(cause), cause.Error())
	}
	activation.Results[index] = workflowCollectEnvelope(nil, cause)
	activation.ItemCursors[index] = doneCursor
	r.saveActivation(*activation)
	return active - 1, nil
}

func workflowMaybeCollect(failurePolicy string, value interface{}) interface{} {
	if failurePolicy == model.WorkflowFailureCollect {
		return workflowCollectEnvelope(value, nil)
	}
	return value
}

func countWorkflowCursors(cursors []int, minimum, maximum int) int {
	count := 0
	for _, cursor := range cursors {
		if cursor >= minimum && cursor <= maximum {
			count++
		}
	}
	return count
}

func workflowCursorsComplete(cursors []int, completeValue int) bool {
	for _, cursor := range cursors {
		if cursor != completeValue {
			return false
		}
	}
	return true
}

func cloneWorkflowScope(input workflowScopeState) (workflowScopeState, error) {
	value, err := cloneWorkflowValue(map[string]interface{}{"vars": input.Vars, "outputs": input.Outputs})
	if err != nil {
		return workflowScopeState{}, err
	}
	object, ok := value.(map[string]interface{})
	if !ok {
		return workflowScopeState{}, ErrWorkflowStateInvalid
	}
	vars, _ := object["vars"].(map[string]interface{})
	outputs, _ := object["outputs"].(map[string]interface{})
	if vars == nil {
		vars = make(map[string]interface{})
	}
	if outputs == nil {
		outputs = make(map[string]interface{})
	}
	return workflowScopeState{Vars: vars, Outputs: outputs}, nil
}

func workflowCollectEnvelope(value interface{}, err error) map[string]interface{} {
	if err == nil {
		return map[string]interface{}{workflowPayloadStatus: model.WorkflowExecutionCompleted, workflowPayloadValue: value, workflowPayloadError: nil}
	}
	return map[string]interface{}{workflowPayloadStatus: model.WorkflowExecutionFailed, workflowPayloadValue: nil, workflowPayloadError: map[string]interface{}{workflowPayloadCode: workflowFailureCode(err), workflowPayloadMessage: err.Error()}}
}

func workflowFailureCode(err error) string {
	var failure workflowNodeFailure
	if errors.As(err, &failure) && strings.TrimSpace(failure.Code) != "" {
		return failure.Code
	}
	if errors.Is(err, ErrWorkflowBudgetExceeded) {
		return "workflow_budget_exceeded"
	}
	return "workflow_node_failed"
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

func (r *workflowRunner) commit() (bool, error) {
	transition, nextJob, err := r.buildTransition()
	if err != nil {
		return false, err
	}
	var saved []model.Event
	var applied bool
	commit := func(txCtx context.Context) error {
		_, events, wasApplied, applyErr := r.service.repo.ApplyWorkflowTransition(txCtx, r.run.Actor, r.run.RunID, transition)
		if applyErr != nil {
			return applyErr
		}
		saved, applied = events, wasApplied
		if !applied {
			return nil
		}
		return r.applyTerminalProjection(txCtx, transition.Run)
	}
	if r.service.unitOfWork != nil {
		err = r.service.unitOfWork.Within(r.ctx, commit)
	} else {
		err = commit(r.ctx)
	}
	if err != nil {
		return false, err
	}
	if !applied {
		return false, nil
	}
	r.service.publishRunEvents(r.run.RunID, saved)
	if nextJob {
		r.service.wakeContinuationJobs()
	}
	return true, nil
}

func (r *workflowRunner) buildTransition() (model.WorkflowTransition, bool, error) {
	status, nextAt := r.nextRunStatus()
	nextRun, nextExecution := r.transitionBase(status)
	r.appendWorkflowSegmentYieldEvent()
	r.appendRunTransitionEvent(nextRun)
	checkpointRows, jobRows, nextJob := []model.Checkpoint(nil), []model.ContinuationJob(nil), false
	if r.terminalOutcome != "" {
		r.applyTerminalTransition(&nextRun, &nextExecution)
	} else if r.dispatchEffectID == "" && r.shouldScheduleContinuation(status, nextAt) {
		checkpointRows, jobRows = r.transitionContinuation(nextRun, nextExecution, status, nextAt)
		nextJob = true
	}
	if err := r.applyTransitionState(&nextExecution); err != nil {
		return model.WorkflowTransition{}, false, err
	}
	return model.WorkflowTransition{
		ExpectedVersion: r.execution.Version, Execution: nextExecution, Run: nextRun,
		Steps: r.transitionSteps(), Interactions: r.interactionRows, Checkpoints: checkpointRows, ContinuationJobs: jobRows,
		Events: r.events, Result: r.result, CacheEntries: r.cacheEntries,
	}, nextJob, nil
}

func (r *workflowRunner) transitionBase(status string) (model.Run, model.WorkflowExecution) {
	nextRun := r.run
	nextRun.Status, nextRun.ErrorCode, nextRun.ErrorMessage = status, "", ""
	nextRun.PendingInteractionID = r.pendingInteractionSummary()
	nextRun.StatusReason = workflowStatusReason(status, len(r.state.Waits))
	nextExecution := r.execution
	nextExecution.Version++
	nextExecution.Status = workflowExecutionStatus(status)
	return nextRun, nextExecution
}

func (r *workflowRunner) appendRunTransitionEvent(nextRun model.Run) {
	if r.terminalOutcome != "" || nextRun.Status == r.run.Status {
		return
	}
	eventType := "run." + nextRun.Status
	if nextRun.Status == model.RunStatusRunning {
		eventType = "run.resumed"
	}
	r.events = append(r.events, newRunEvent(nextRun, eventType, nextRun.CurrentStepID, nextRun.StatusReason, map[string]interface{}{workflowPayloadRuntimeKind: model.RuntimeKindWorkflow, "waitCount": len(r.state.Waits)}, nil))
}

func (r *workflowRunner) applyTerminalTransition(run *model.Run, execution *model.WorkflowExecution) {
	switch r.terminalOutcome {
	case model.TerminalCompleted:
		run.Status, execution.Status = model.RunStatusCompleted, model.WorkflowExecutionCompleted
		run.StatusReason = "Workflow completed"
	case model.TerminalFailed:
		r.applyTerminalFailure(run, execution, model.RunStatusFailed, model.WorkflowExecutionFailed)
	case model.TerminalCancelled:
		r.applyTerminalFailure(run, execution, model.RunStatusCancelled, model.WorkflowExecutionCancelled)
	case model.RunStatusSuspended:
		r.applyTerminalFailure(run, execution, model.RunStatusSuspended, model.WorkflowExecutionSuspended)
	}
	if r.terminalOutcome != model.RunStatusSuspended {
		run.EndedAt, execution.EndedAt = &r.now, &r.now
		run.TotalLatencyMS = r.now.Sub(run.StartedAt).Milliseconds()
	}
	r.appendTerminalEvents(*run)
}

func (r *workflowRunner) applyTerminalFailure(run *model.Run, execution *model.WorkflowExecution, runStatus, executionStatus string) {
	run.Status, execution.Status = runStatus, executionStatus
	run.ErrorCode, run.ErrorMessage = r.terminalCode, r.terminalMessage
	execution.ErrorCode, execution.ErrorMessage = r.terminalCode, r.terminalMessage
}

func (r *workflowRunner) shouldScheduleContinuation(status string, nextAt *time.Time) bool {
	if nextAt != nil || r.hasPollableWaits() {
		return true
	}
	_, ok := workflowContinuationStatuses[status]
	return ok
}

var workflowContinuationStatuses = map[string]struct{}{
	model.RunStatusRunning:        {},
	model.RunStatusCancelling:     {},
	model.RunStatusCompensating:   {},
	model.RunStatusWaitingHandoff: {},
}

func (r *workflowRunner) transitionContinuation(run model.Run, execution model.WorkflowExecution, status string, nextAt *time.Time) ([]model.Checkpoint, []model.ContinuationJob) {
	continuation := runContinuation{
		SemanticVersion: RuntimeSnapshotVersion,
		SegmentKey:      fmt.Sprintf("%s:workflow:%d", r.run.RunID, execution.Version),
		Type:            runContinuationWorkflowExecute,
		TargetStatus:    model.RunStatusRunning,
		StepID:          firstNonEmptyString(run.CurrentStepID, r.run.CurrentStepID),
	}
	checkpoint := newRunContinuationCheckpoint(run, continuation.StepID, "workflow_transition", continuation)
	availableAt := workflowContinuationAvailableAt(r.now, status, nextAt)
	job := r.service.newWorkflowContinuationJob(r.ctx, run, *checkpoint, "workflow_transition", availableAt)
	r.events = append(r.events, newRunEvent(run, "checkpoint.created", continuation.StepID, "Workflow transition checkpoint", map[string]interface{}{workflowPayloadCheckpointID: checkpoint.CheckpointID, "executionVersion": execution.Version}, nil))
	return []model.Checkpoint{*checkpoint}, []model.ContinuationJob{*job}
}

func workflowContinuationAvailableAt(now time.Time, status string, nextAt *time.Time) time.Time {
	if nextAt != nil {
		return *nextAt
	}
	if status == model.RunStatusWaitingHandoff || status == model.RunStatusCancelling || status == model.RunStatusCompensating {
		return now.Add(time.Second)
	}
	return now
}

func (r *workflowRunner) applyTransitionState(execution *model.WorkflowExecution) error {
	stateJSON, varsJSON, waitsJSON, compensationJSON, budgetJSON, err := encodeWorkflowExecutionState(r.state, r.budget)
	if err != nil {
		return err
	}
	if len(stateJSON) > r.budget.Limits.MaxStateBytes {
		return ErrWorkflowStateTooLarge
	}
	execution.StateJSON, execution.VarsJSON, execution.WaitsJSON = stateJSON, varsJSON, waitsJSON
	execution.CompensationJSON, execution.BudgetJSON = compensationJSON, budgetJSON
	return nil
}

func (r *workflowRunner) transitionSteps() []model.Step {
	steps := make([]model.Step, 0, len(r.changedSteps))
	for _, stepID := range r.stepOrder {
		if _, changed := r.changedSteps[stepID]; changed {
			steps = append(steps, r.steps[stepID])
		}
	}
	return steps
}

type workflowWaitSummary struct {
	earliest       *time.Time
	hasInteraction bool
	hasChild       bool
}

func (r *workflowRunner) summarizeWaits() workflowWaitSummary {
	var summary workflowWaitSummary
	for _, wait := range r.state.Waits {
		switch wait.Kind {
		case model.WorkflowWaitInteraction:
			summary.hasInteraction = true
		case model.WorkflowWaitAgent, model.WorkflowWaitWorkflow:
			summary.hasChild = true
		case model.WorkflowWaitTimer:
			summary.earliest = earlierWorkflowWake(summary.earliest, wait.WakeAt)
		}
	}
	return summary
}

func earlierWorkflowWake(current, candidate *time.Time) *time.Time {
	if candidate == nil {
		return current
	}
	if current == nil || candidate.Before(*current) {
		value := *candidate
		return &value
	}
	return current
}

func (r *workflowRunner) hasRunningCompensation() bool {
	for _, compensation := range r.state.Compensations {
		if compensation.Status == model.WorkflowCompensationRunning {
			return true
		}
	}
	return false
}

func (r *workflowRunner) nextRunStatus() (string, *time.Time) {
	if r.state.CancelRequested {
		return model.RunStatusCancelling, nil
	}
	waits := r.summarizeWaits()
	if r.state.ErrorMessage != "" && waits.hasChild {
		return model.RunStatusWaitingHandoff, nil
	}
	if r.hasRunningCompensation() {
		return model.RunStatusCompensating, nil
	}
	if r.state.ErrorMessage != "" && len(r.state.Compensations) > 0 {
		return model.RunStatusCompensating, nil
	}
	return workflowStatusForWaits(waits)
}

func workflowStatusForWaits(waits workflowWaitSummary) (string, *time.Time) {
	if waits.hasInteraction {
		return model.RunStatusWaitingInput, waits.earliest
	}
	if waits.earliest != nil {
		return model.RunStatusWaitingTimer, waits.earliest
	}
	if waits.hasChild {
		return model.RunStatusWaitingHandoff, nil
	}
	return model.RunStatusRunning, nil
}

func workflowExecutionStatus(runStatus string) string {
	switch runStatus {
	case model.RunStatusCancelling:
		return model.WorkflowExecutionCancelling
	case model.RunStatusCompensating:
		return model.WorkflowExecutionCompensating
	case model.RunStatusWaitingInput, model.RunStatusWaitingTimer, model.RunStatusWaitingHandoff:
		return model.WorkflowExecutionWaiting
	default:
		return model.WorkflowExecutionRunning
	}
}

func workflowStatusReason(status string, waits int) string {
	switch status {
	case model.RunStatusWaitingInput, model.RunStatusWaitingTimer, model.RunStatusWaitingHandoff:
		return fmt.Sprintf("Workflow has %d pending wait(s)", waits)
	case model.RunStatusCancelling:
		return "Workflow cancellation is settling child runs"
	case model.RunStatusCompensating:
		return "Workflow compensation is running"
	default:
		return "Workflow is running"
	}
}

func (r *workflowRunner) pendingInteractionSummary() string {
	ids := make([]string, 0)
	for _, wait := range r.state.Waits {
		if wait.Kind == model.WorkflowWaitInteraction && wait.InteractionID != "" {
			ids = append(ids, wait.InteractionID)
		}
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

func (r *workflowRunner) appendTerminalEvents(run model.Run) {
	switch run.Status {
	case model.RunStatusCompleted:
		r.events = append(r.events, newRunEvent(run, "workflow.completed", run.CurrentStepID, "Workflow completed", map[string]interface{}{workflowPayloadContentHash: r.result.ContentHash}, &run.OutputProjection))
		r.events = append(r.events, newRunEvent(run, "run.completed", run.CurrentStepID, "Workflow completed", map[string]interface{}{workflowPayloadRuntimeKind: model.RuntimeKindWorkflow}, &run.OutputProjection))
	case model.RunStatusFailed:
		event := newRunEvent(run, "workflow.failed", run.CurrentStepID, run.ErrorMessage, map[string]interface{}{workflowPayloadCode: run.ErrorCode}, &run.OutputProjection)
		event.ErrorJSON = mustRunJSON(map[string]interface{}{workflowPayloadCode: run.ErrorCode, workflowPayloadMessage: run.ErrorMessage})
		r.events = append(r.events, event, newRunEvent(run, "run.failed", run.CurrentStepID, run.ErrorMessage, map[string]interface{}{workflowPayloadCode: run.ErrorCode}, &run.OutputProjection))
	case model.RunStatusCancelled:
		r.events = append(r.events, newRunEvent(run, "workflow.cancelled", run.CurrentStepID, run.ErrorMessage, map[string]interface{}{workflowPayloadCode: run.ErrorCode}, &run.OutputProjection))
		r.events = append(r.events, newRunEvent(run, "run.cancelled", run.CurrentStepID, run.ErrorMessage, map[string]interface{}{workflowPayloadCode: run.ErrorCode}, &run.OutputProjection))
	case model.RunStatusSuspended:
		r.events = append(r.events, newRunEvent(run, "workflow.compensation.failed", run.CurrentStepID, run.ErrorMessage, map[string]interface{}{workflowPayloadCode: run.ErrorCode}, nil))
		r.events = append(r.events, newRunEvent(run, "run.suspended", run.CurrentStepID, run.ErrorMessage, map[string]interface{}{workflowPayloadCode: run.ErrorCode}, nil))
	}
}

func (r *workflowRunner) applyTerminalProjection(ctx context.Context, run model.Run) error {
	if r.terminalOutcome == "" || r.terminalOutcome == model.RunStatusSuspended || r.service.turnProjections == nil {
		return nil
	}
	projection := TurnProjection{Input: run.InputProjection, Output: run.OutputProjection}
	usage := TurnUsage{
		InputTokens: run.InputTokens, OutputTokens: run.OutputTokens, CacheReadTokens: run.CacheReadTokens,
		CacheWriteTokens: run.CacheWriteTokens, ReasoningTokens: run.ReasoningTokens, LatencyMS: run.TotalLatencyMS,
		BilledCurrency: run.BilledCurrency, BilledNanousd: run.BilledNanousd,
	}
	switch r.terminalOutcome {
	case model.TerminalCompleted:
		_, err := r.service.turnProjections.CompleteTurn(ctx, CompleteTurnRequest{
			Actor: run.Actor, Thread: run.Thread, RunID: run.RunID, Projection: projection,
			ContentType: workflowContentTypeText, Content: r.state.Presentation, Usage: usage,
		})
		return err
	case model.TerminalFailed:
		_, err := r.service.turnProjections.FailTurn(ctx, FailTurnRequest{
			Actor: run.Actor, Thread: run.Thread, RunID: run.RunID, Projection: projection,
			ContentType: workflowContentTypeText, Content: r.state.Presentation, Usage: usage, ErrorCode: run.ErrorCode, ErrorMessage: run.ErrorMessage,
		})
		return err
	case model.TerminalCancelled:
		_, err := r.service.turnProjections.CancelTurn(ctx, CancelTurnRequest{
			Actor: run.Actor, Thread: run.Thread, RunID: run.RunID, Projection: projection,
			ErrorCode: run.ErrorCode, ErrorMessage: run.ErrorMessage,
		})
		return err
	default:
		return nil
	}
}

func (r *workflowRunner) cancelRuntimeOwnedChildren() {
	for _, activation := range r.state.Activations {
		if activation.ChildRunID == "" {
			continue
		}
		child, err := r.service.repo.GetRun(r.ctx, r.run.Actor, activation.ChildRunID)
		if err != nil || child.EndedAt != nil {
			continue
		}
		_, _ = r.service.CancelRun(context.WithoutCancel(r.ctx), r.run.Actor, child.RunID)
	}
}

func (r *workflowRunner) cancelNonChildWaits(reason string) {
	waitIDs := make([]string, 0, len(r.state.Waits))
	for waitID := range r.state.Waits {
		waitIDs = append(waitIDs, waitID)
	}
	sort.Strings(waitIDs)
	for _, waitID := range waitIDs {
		wait := r.state.Waits[waitID]
		if wait.Kind == model.WorkflowWaitAgent || wait.Kind == model.WorkflowWaitWorkflow {
			continue
		}
		delete(r.state.Waits, waitID)
		activation, ok := r.state.Activations[wait.ActivationKey]
		if ok && activation.Status == model.WorkflowStepStatusWaiting {
			activation.Status, activation.WaitID, activation.WakeAt = model.WorkflowStepStatusCancelled, "", nil
			activation.ErrorCode, activation.ErrorMessage = "workflow_wait_cancelled", reason
			r.saveActivation(activation)
			step := r.steps[activation.StepID]
			step.Status, step.WaitingKind, step.WaitingID = model.WorkflowStepStatusCancelled, "", ""
			step.ResultSummary = reason
			step.ErrorJSON = mustRunJSON(map[string]interface{}{workflowPayloadCode: activation.ErrorCode, workflowPayloadMessage: reason})
			endedAt := r.now
			step.EndedAt = &endedAt
			r.steps[activation.StepID] = step
			r.changedSteps[activation.StepID] = struct{}{}
			r.events = append(r.events, newRunEvent(r.run, "step.cancelled", activation.StepID, reason, map[string]interface{}{workflowPayloadNodeID: activation.NodeID, workflowPayloadWaitKind: wait.Kind}, nil))
		}
		if wait.InteractionID != "" {
			interaction, exists := r.interactions[wait.InteractionID]
			if exists && interaction.Status == model.InteractionPending {
				interaction.Status, interaction.ResolvedAt, interaction.UpdatedAt = model.InteractionCancelled, &r.now, r.now
				r.interactions[interaction.InteractionID] = interaction
				r.interactionRows = append(r.interactionRows, interaction)
				r.events = append(r.events, newRunEvent(r.run, "interaction.cancelled", wait.StepID, reason, map[string]interface{}{workflowPayloadInteractionID: interaction.InteractionID}, nil))
			}
		}
	}
}

func (r *workflowRunner) settleRuntimeOwnedChildren(reason string) bool {
	paths := make([]string, 0, len(r.state.Activations))
	for path := range r.state.Activations {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	allSettled := true
	for _, path := range paths {
		activation := r.state.Activations[path]
		if !workflowActivationHasReservedChild(activation) {
			continue
		}
		if !r.settleRuntimeOwnedChild(activation, reason) {
			allSettled = false
		}
	}
	return allSettled
}

func workflowActivationHasReservedChild(activation workflowActivationState) bool {
	if activation.ChildRunID == "" {
		return false
	}
	return activation.WaitID != "" || activation.ReservedLLM != 0 || activation.ReservedTools != 0 || activation.ReservedChildren != 0
}

type workflowChildSettlement struct {
	llmCalls       int
	toolCalls      int
	actualChildren int
}

func (r *workflowRunner) settleRuntimeOwnedChild(activation workflowActivationState, reason string) bool {
	child, err := r.service.repo.GetRun(r.ctx, r.run.Actor, activation.ChildRunID)
	if err == nil && child.EndedAt == nil {
		return false
	}
	settlement := r.workflowChildSettlement(child, err)
	r.applyWorkflowChildSettlementBudget(activation, settlement)
	r.recordWorkflowChildSettlement(activation, settlement, reason)
	return true
}

func (r *workflowRunner) workflowChildSettlement(child *model.Run, loadErr error) workflowChildSettlement {
	settlement := workflowChildSettlement{actualChildren: 1}
	if loadErr != nil || child == nil {
		return settlement
	}
	settlement.llmCalls, settlement.toolCalls = child.LLMCallsCount, child.ToolCallsCount
	if child.RuntimeKind != model.RuntimeKindWorkflow {
		return settlement
	}
	execution, err := r.service.repo.GetWorkflowExecution(r.ctx, r.run.Actor, child.RunID)
	if err != nil {
		return settlement
	}
	var childBudget model.WorkflowBudget
	if json.Unmarshal([]byte(execution.BudgetJSON), &childBudget) != nil {
		return settlement
	}
	settlement.llmCalls, settlement.toolCalls = childBudget.UsedLLMCalls, childBudget.UsedToolCalls
	settlement.actualChildren += childBudget.ChildRuns
	return settlement
}

func (r *workflowRunner) applyWorkflowChildSettlementBudget(activation workflowActivationState, settlement workflowChildSettlement) {
	if unusedChildren := activation.ReservedChildren - settlement.actualChildren; unusedChildren > 0 {
		r.budget.ChildRuns = max(0, r.budget.ChildRuns-unusedChildren)
	}
	r.budget.ConcurrentRuns = max(0, r.budget.ConcurrentRuns-1)
	r.budget.ReservedLLMCalls = max(0, r.budget.ReservedLLMCalls-activation.ReservedLLM)
	r.budget.ReservedToolCalls = max(0, r.budget.ReservedToolCalls-activation.ReservedTools)
	r.budget.UsedLLMCalls += settlement.llmCalls
	r.budget.UsedToolCalls += settlement.toolCalls
	r.run.LLMCallsCount += settlement.llmCalls
	r.run.ToolCallsCount += settlement.toolCalls
}

func (r *workflowRunner) recordWorkflowChildSettlement(activation workflowActivationState, settlement workflowChildSettlement, reason string) {
	delete(r.state.Waits, activation.WaitID)
	activation.Status, activation.WaitID, activation.WakeAt = model.WorkflowStepStatusCancelled, "", nil
	activation.ReservedLLM, activation.ReservedTools, activation.ReservedChildren = 0, 0, 0
	activation.ErrorCode, activation.ErrorMessage = "workflow_child_cancelled", reason
	r.saveActivation(activation)
	step := r.steps[activation.StepID]
	step.Status, step.WaitingKind, step.WaitingID = model.WorkflowStepStatusCancelled, "", ""
	step.ResultSummary = reason
	step.ErrorJSON = mustRunJSON(map[string]interface{}{workflowPayloadCode: activation.ErrorCode, workflowPayloadMessage: reason})
	endedAt := r.now
	step.EndedAt = &endedAt
	r.steps[activation.StepID] = step
	r.changedSteps[activation.StepID] = struct{}{}
	r.events = append(r.events,
		newRunEvent(r.run, "workflow.child.settled", activation.StepID, reason, map[string]interface{}{workflowPayloadChildRunID: activation.ChildRunID, workflowPayloadLLMCalls: settlement.llmCalls, workflowPayloadToolCalls: settlement.toolCalls}, nil),
		newRunEvent(r.run, "workflow.budget.settled", activation.StepID, activation.NodeID, map[string]interface{}{workflowPayloadChildRunID: activation.ChildRunID, workflowPayloadLLMCalls: settlement.llmCalls, workflowPayloadToolCalls: settlement.toolCalls}, nil),
		newRunEvent(r.run, "step.cancelled", activation.StepID, reason, map[string]interface{}{workflowPayloadNodeID: activation.NodeID, workflowPayloadChildRunID: activation.ChildRunID}, nil),
	)
}

func (s *Engine) failWorkflowRun(ctx context.Context, run model.Run, cause error) {
	execution, err := s.repo.GetWorkflowExecution(ctx, run.Actor, run.RunID)
	if err != nil {
		return
	}
	state, budget, err := decodeWorkflowExecutionState(*execution)
	if err != nil {
		return
	}
	state.ErrorCode, state.ErrorMessage = workflowFailureCode(cause), cause.Error()
	stateJSON, varsJSON, waitsJSON, compensationJSON, budgetJSON, err := encodeWorkflowExecutionState(state, budget)
	if err != nil {
		return
	}
	nextExecution := *execution
	nextExecution.Version++
	nextExecution.StateJSON, nextExecution.VarsJSON, nextExecution.WaitsJSON = stateJSON, varsJSON, waitsJSON
	nextExecution.CompensationJSON, nextExecution.BudgetJSON = compensationJSON, budgetJSON
	nextExecution.Status = model.WorkflowExecutionRunning
	nextRun := run
	nextRun.Status, nextRun.ErrorCode, nextRun.ErrorMessage = model.RunStatusRunning, state.ErrorCode, state.ErrorMessage
	continuation := runContinuation{
		SemanticVersion: RuntimeSnapshotVersion, SegmentKey: fmt.Sprintf("%s:workflow:%d", run.RunID, nextExecution.Version),
		Type: runContinuationWorkflowExecute, TargetStatus: model.RunStatusRunning, StepID: run.CurrentStepID,
	}
	checkpoint := newRunContinuationCheckpoint(run, run.CurrentStepID, "workflow_failure", continuation)
	job := s.newWorkflowContinuationJob(ctx, run, *checkpoint, "workflow_failure", s.now())
	transition := model.WorkflowTransition{
		ExpectedVersion: execution.Version, Execution: nextExecution, Run: nextRun,
		Checkpoints: []model.Checkpoint{*checkpoint}, ContinuationJobs: []model.ContinuationJob{*job},
		Events: []model.Event{newRunEvent(run, "workflow.failure_requested", run.CurrentStepID, cause.Error(), map[string]interface{}{workflowPayloadCode: state.ErrorCode}, nil)},
	}
	_, events, applied, _ := s.repo.ApplyWorkflowTransition(ctx, run.Actor, run.RunID, transition)
	if applied {
		s.publishRunEvents(run.RunID, events)
		s.wakeContinuationJobs()
	}
}

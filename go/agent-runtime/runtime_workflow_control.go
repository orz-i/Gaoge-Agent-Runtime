package agentruntime

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

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

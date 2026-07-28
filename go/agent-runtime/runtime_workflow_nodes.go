package agentruntime

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const workflowInteractionType = "workflow"

func (r *workflowRunner) addWait(activation *workflowActivationState, kind string, wakeAt *time.Time, interactionID, childRunID string, payload interface{}) {
	waitID := deterministicWorkflowID("wait", r.run.RunID, activation.Path, kind)
	var raw json.RawMessage
	if payload != nil {
		if encoded, err := canonicalWorkflowJSON(payload); err == nil {
			raw = encoded
		}
	}
	wait := model.WorkflowWait{
		WaitID: waitID, Kind: kind, ActivationKey: activation.Path, StepID: activation.StepID,
		InteractionID: interactionID, ChildRunID: childRunID, WakeAt: wakeAt, Payload: raw, CreatedAt: r.now,
	}
	r.recordWorkflowWaitSpan(wait)
	r.state.Waits[waitID] = wait
	activation.Status, activation.WaitID = model.WorkflowStepStatusWaiting, waitID
	activation.InteractionID, activation.ChildRunID, activation.WakeAt = interactionID, childRunID, wakeAt
	r.saveActivation(*activation)
	step := r.steps[activation.StepID]
	step.Status, step.WaitingKind, step.WaitingID, step.ChildRunID = model.WorkflowStepStatusWaiting, kind, waitID, childRunID
	r.steps[activation.StepID] = step
	r.changedSteps[activation.StepID] = struct{}{}
	r.progress = true
}

func (r *workflowRunner) clearWait(activation *workflowActivationState) {
	delete(r.state.Waits, activation.WaitID)
	activation.Status, activation.WaitID, activation.WakeAt = model.WorkflowStepStatusRunning, "", nil
	step := r.steps[activation.StepID]
	step.Status, step.WaitingKind, step.WaitingID = model.WorkflowStepStatusRunning, "", ""
	r.steps[activation.StepID] = step
	r.changedSteps[activation.StepID] = struct{}{}
	r.saveActivation(*activation)
}

func (r *workflowRunner) resumeWaitingNode(node *model.WorkflowNode, activation workflowActivationState) (interface{}, bool, error) {
	switch node.Type {
	case model.WorkflowNodeTimer:
		if activation.WakeAt == nil || r.now.Before(*activation.WakeAt) {
			return nil, false, nil
		}
		firedAt := *activation.WakeAt
		r.clearWait(&activation)
		r.events = append(r.events, newRunEvent(r.run, "workflow.timer.fired", activation.StepID, node.ID, map[string]interface{}{workflowPayloadWakeAt: firedAt}, nil))
		return r.completeActivation(*node, activation, map[string]interface{}{workflowPayloadWakeAt: firedAt.Format(time.RFC3339Nano)})
	case model.WorkflowNodeInteraction:
		return r.resumeWorkflowInteraction(node, activation, false)
	case model.WorkflowNodeTool:
		return r.resumeWorkflowInteraction(node, activation, true)
	case model.WorkflowNodeAgent, model.WorkflowNodeWorkflow:
		return r.resumeWorkflowChild(node, activation)
	default:
		return nil, false, workflowNodeFailure{Code: "workflow_wait_invalid", Message: "node cannot be resumed from a wait"}
	}
}

func (r *workflowRunner) advanceInteraction(node *model.WorkflowNode, activation workflowActivationState) (interface{}, bool, error) {
	interactionID := deterministicWorkflowID("interaction", r.run.RunID, activation.Path)
	expiresAt := r.now.Add(time.Duration(node.ExpiresAfterSeconds) * time.Second)
	request, _ := canonicalWorkflowJSON(map[string]interface{}{"title": node.Title, "prompt": node.Prompt, workflowPayloadNodeID: node.ID})
	interaction := model.Interaction{
		InteractionID: interactionID, RunID: r.run.RunID, StepID: activation.StepID,
		Type: workflowInteractionType, Status: model.InteractionPending, RequestPayloadJSON: string(request),
		ResponseSchemaJSON: string(node.Schema), RequestedAt: r.now, ExpiresAt: &expiresAt,
	}
	r.interactions[interactionID] = interaction
	r.interactionRows = append(r.interactionRows, interaction)
	r.addWait(&activation, model.WorkflowWaitInteraction, nil, interactionID, "", nil)
	r.events = append(r.events,
		newRunEvent(r.run, "interaction.created", activation.StepID, node.Title, map[string]interface{}{workflowPayloadInteractionID: interactionID, workflowPayloadType: workflowInteractionType, workflowPayloadNodeID: node.ID}, nil),
		newRunEvent(r.run, "workflow.node.waiting", activation.StepID, node.ID, map[string]interface{}{workflowPayloadWaitKind: model.WorkflowWaitInteraction, workflowPayloadInteractionID: interactionID}, nil),
		newRunEvent(r.run, "step.waiting_input", activation.StepID, node.Title, map[string]interface{}{workflowPayloadInteractionID: interactionID}, nil),
	)
	return nil, false, nil
}

func (r *workflowRunner) resumeWorkflowInteraction(node *model.WorkflowNode, activation workflowActivationState, approval bool) (interface{}, bool, error) {
	interaction, ok := r.interactions[activation.InteractionID]
	if !ok || interaction.Status == model.InteractionPending {
		return nil, false, nil
	}
	if interaction.Status != model.InteractionResolved {
		return nil, false, r.failActivation(*node, activation, "workflow_interaction_unavailable", "workflow interaction was not resolved")
	}
	value, err := decodeWorkflowJSON(json.RawMessage(interaction.ResponseJSON))
	if err != nil {
		return nil, false, r.failActivation(*node, activation, "workflow_interaction_invalid", err.Error())
	}
	r.clearWait(&activation)
	r.events = append(r.events,
		newRunEvent(r.run, "workflow.node.resumed", activation.StepID, node.ID, map[string]interface{}{workflowPayloadInteractionID: interaction.InteractionID}, nil),
		newRunEvent(r.run, "step.resumed", activation.StepID, node.ID, map[string]interface{}{workflowPayloadInteractionID: interaction.InteractionID}, nil),
	)
	if !approval {
		return r.completeActivation(*node, activation, value)
	}
	object, ok := value.(map[string]interface{})
	approved, approvedOK := object["approved"].(bool)
	if !ok || !approvedOK || !approved {
		return nil, false, r.failActivation(*node, activation, "workflow_tool_rejected", "workflow tool approval was rejected")
	}
	activation.InteractionID = ""
	activation.Approved = true
	r.saveActivation(activation)
	return r.advanceTool(node, activation)
}

func (r *workflowRunner) advanceTimer(node *model.WorkflowNode, activation workflowActivationState) (interface{}, bool, error) {
	var wakeAt time.Time
	if node.DelaySeconds != nil {
		value, err := r.evaluate(node.DelaySeconds, activation.ScopeKey)
		if err != nil {
			return nil, false, r.failActivation(*node, activation, "workflow_expression_failed", err.Error())
		}
		number, ok := value.(json.Number)
		if !ok {
			return nil, false, r.failActivation(*node, activation, "workflow_timer_invalid", "timer delay must be a number")
		}
		seconds, err := strconv.ParseInt(number.String(), 10, 64)
		if err != nil || seconds < 0 {
			return nil, false, r.failActivation(*node, activation, "workflow_timer_invalid", "timer delay must be a non-negative integer")
		}
		wakeAt = r.now.Add(time.Duration(seconds) * time.Second)
	} else {
		value, err := r.evaluate(node.WakeAt, activation.ScopeKey)
		if err != nil {
			return nil, false, r.failActivation(*node, activation, "workflow_expression_failed", err.Error())
		}
		text, ok := value.(string)
		if !ok {
			return nil, false, r.failActivation(*node, activation, "workflow_timer_invalid", "timer wakeAt must be an RFC3339 string")
		}
		wakeAt, err = time.Parse(time.RFC3339Nano, text)
		if err != nil {
			return nil, false, r.failActivation(*node, activation, "workflow_timer_invalid", err.Error())
		}
	}
	if !wakeAt.After(r.now) {
		return r.completeActivation(*node, activation, map[string]interface{}{workflowPayloadWakeAt: wakeAt.Format(time.RFC3339Nano)})
	}
	r.addWait(&activation, model.WorkflowWaitTimer, &wakeAt, "", "", nil)
	r.events = append(r.events,
		newRunEvent(r.run, "workflow.timer.scheduled", activation.StepID, node.ID, map[string]interface{}{workflowPayloadWakeAt: wakeAt}, nil),
		newRunEvent(r.run, "workflow.node.waiting", activation.StepID, node.ID, map[string]interface{}{workflowPayloadWaitKind: model.WorkflowWaitTimer, workflowPayloadWakeAt: wakeAt}, nil),
		newRunEvent(r.run, "step.waiting_timer", activation.StepID, node.ID, map[string]interface{}{workflowPayloadWakeAt: wakeAt}, nil),
	)
	return nil, false, nil
}

func (r *workflowRunner) advanceAgent(node *model.WorkflowNode, activation workflowActivationState) (interface{}, bool, error) {
	if activation.EffectID != "" {
		return r.advanceWorkflowEffect(node, activation)
	}
	goal, err := r.workflowAgentGoal(node, activation)
	if err != nil {
		return nil, false, r.failActivation(*node, activation, workflowFailureCode(err), err.Error())
	}
	manifest, err := r.service.repo.GetAgentManifest(r.ctx, r.run.Actor, node.ManifestRef)
	if err != nil {
		return nil, false, r.failActivation(*node, activation, "workflow_agent_unavailable", err.Error())
	}
	maxLLM, maxTools := r.workflowAgentLimits(*manifest, node.PerNodeLimits)
	if err = r.reserveChildBudget(&activation, maxLLM, maxTools, 1); err != nil {
		return nil, false, r.failActivation(*node, activation, "workflow_budget_exceeded", err.Error())
	}
	clientID := deterministicWorkflowID("workflow_agent", r.run.RunID, activation.Path, strconv.Itoa(activation.Attempt))
	_, childRunID := delegatedPublicIDs(r.run.Actor, clientID)
	err = r.registerWorkflowChildEffect(*node, activation, workflowEffectKindAgent, childRunID, workflowAgentEffectPayload{
		ClientHandoffID:        clientID,
		ManifestRef:            node.ManifestRef,
		Goal:                   goal,
		RequestID:              r.run.RequestID + ":" + node.ID,
		MaxLLMCalls:            maxLLM,
		MaxToolCalls:           maxTools,
		StructuredOutputSchema: append(json.RawMessage(nil), node.OutputSchema...),
		ResultAttempts:         node.ResultAttempts,
	})
	if err != nil {
		r.releaseFailedChildReservation(&activation)
		return nil, false, r.failActivation(*node, activation, workflowChildFailureInvalid, err.Error())
	}
	return nil, false, nil
}

func (r *workflowRunner) workflowAgentGoal(node *model.WorkflowNode, activation workflowActivationState) (string, error) {
	value, err := r.evaluate(node.Goal, activation.ScopeKey)
	if err != nil {
		return "", workflowNodeFailure{Code: "workflow_expression_failed", Message: err.Error()}
	}
	goal, ok := value.(string)
	if !ok || strings.TrimSpace(goal) == "" {
		return "", workflowNodeFailure{Code: "workflow_agent_goal_invalid", Message: "agent goal must be a non-empty string"}
	}
	return goal, nil
}

func (r *workflowRunner) workflowAgentLimits(manifest model.AgentManifest, nodeLimits *model.WorkflowNodeLimits) (int, int) {
	maxLLM := manifest.MaxLLMCalls
	if maxLLM <= 0 {
		maxLLM = r.service.resolveMaxLLMCallsPerRun()
	}
	maxTools := manifest.MaxToolCalls
	if maxTools <= 0 {
		maxTools = r.service.resolveMaxToolCallsPerRun()
	}
	if nodeLimits == nil {
		return maxLLM, maxTools
	}
	if nodeLimits.MaxLLMCalls > 0 {
		maxLLM = min(maxLLM, nodeLimits.MaxLLMCalls)
	}
	if nodeLimits.MaxToolCalls > 0 {
		maxTools = min(maxTools, nodeLimits.MaxToolCalls)
	}
	return maxLLM, maxTools
}

func (r *workflowRunner) advanceNestedWorkflow(node *model.WorkflowNode, activation workflowActivationState) (interface{}, bool, error) {
	if activation.EffectID != "" {
		return r.advanceWorkflowEffect(node, activation)
	}
	inputValue, err := r.evaluate(node.Input, activation.ScopeKey)
	if err != nil {
		return nil, false, r.failActivation(*node, activation, "workflow_expression_failed", err.Error())
	}
	definition, err := r.service.repo.GetWorkflowDefinition(r.ctx, r.run.Actor, node.DefinitionRef)
	if err != nil {
		return nil, false, r.failActivation(*node, activation, "nested_workflow_unavailable", err.Error())
	}
	if r.run.Depth+1 > r.budget.Limits.MaxNestedDepth {
		return nil, false, r.failActivation(*node, activation, "workflow_nested_depth_exceeded", ErrWorkflowBudgetExceeded.Error())
	}
	canonicalInput, err := canonicalWorkflowJSON(inputValue)
	if err != nil {
		return nil, false, r.failActivation(*node, activation, "nested_workflow_input_invalid", err.Error())
	}
	if err = validateWorkflowJSON(definition.InputSchema, inputValue); err != nil {
		return nil, false, r.failActivation(*node, activation, "nested_workflow_input_invalid", err.Error())
	}
	reservedChildren := 1 + definition.Limits.MaxChildRuns
	if err = r.reserveChildBudget(&activation, definition.Limits.MaxTotalLLMCalls, definition.Limits.MaxTotalToolCalls, reservedChildren); err != nil {
		return nil, false, r.failActivation(*node, activation, "workflow_budget_exceeded", err.Error())
	}
	childRunID := deterministicWorkflowID("run", r.run.RunID, activation.Path, strconv.Itoa(activation.Attempt))
	err = r.registerWorkflowChildEffect(*node, activation, workflowEffectKindWorkflow, childRunID, workflowNestedEffectPayload{
		ClientRunID: childRunID,
		Definition:  node.DefinitionRef,
		Input:       append(json.RawMessage(nil), canonicalInput...),
		Limits:      definition.Limits,
		RequestID:   r.run.RequestID + ":" + node.ID,
	})
	if err != nil {
		r.releaseFailedChildReservation(&activation)
		return nil, false, r.failActivation(*node, activation, workflowChildFailureInvalid, err.Error())
	}
	return nil, false, nil
}

func workflowEffectiveThreadScope(effective effectiveWorkflowConfig) string {
	if effective.Workspace != nil {
		return strings.TrimSpace(effective.Workspace.Request.Type)
	}
	return workflowEnvironment
}

func (r *workflowRunner) reserveChildBudget(activation *workflowActivationState, llm, tools, children int) error {
	if r.budget.ChildRuns+children > r.budget.Limits.MaxChildRuns ||
		r.budget.ConcurrentRuns+1 > r.budget.Limits.MaxConcurrentRuns ||
		r.budget.UsedLLMCalls+r.budget.ReservedLLMCalls+llm > r.budget.Limits.MaxTotalLLMCalls ||
		r.budget.UsedToolCalls+r.budget.ReservedToolCalls+tools > r.budget.Limits.MaxTotalToolCalls {
		return ErrWorkflowBudgetExceeded
	}
	r.budget.ChildRuns += children
	r.budget.ConcurrentRuns++
	r.budget.ReservedLLMCalls += llm
	r.budget.ReservedToolCalls += tools
	activation.ReservedLLM, activation.ReservedTools, activation.ReservedChildren = llm, tools, children
	r.saveActivation(*activation)
	r.events = append(r.events, newRunEvent(r.run, "workflow.budget.reserved", activation.StepID, activation.NodeID, map[string]interface{}{workflowPayloadLLMCalls: llm, workflowPayloadToolCalls: tools, "childRuns": children}, nil))
	return nil
}

func (r *workflowRunner) releaseFailedChildReservation(activation *workflowActivationState) {
	r.budget.ConcurrentRuns = max(0, r.budget.ConcurrentRuns-1)
	r.budget.ReservedLLMCalls = max(0, r.budget.ReservedLLMCalls-activation.ReservedLLM)
	r.budget.ReservedToolCalls = max(0, r.budget.ReservedToolCalls-activation.ReservedTools)
	r.budget.ChildRuns = max(0, r.budget.ChildRuns-activation.ReservedChildren)
	activation.ReservedLLM, activation.ReservedTools, activation.ReservedChildren = 0, 0, 0
	r.saveActivation(*activation)
}

func (r *workflowRunner) resumeChildRun(node *model.WorkflowNode, activation workflowActivationState) (interface{}, bool, error) {
	child, err := r.service.repo.GetRun(r.ctx, r.run.Actor, activation.ChildRunID)
	if err != nil {
		return nil, false, r.failActivation(*node, activation, "workflow_child_missing", err.Error())
	}
	if child.EndedAt == nil {
		return nil, false, nil
	}
	settlement, err := r.settleFinishedWorkflowChildBudget(*child, &activation)
	if err != nil {
		return nil, false, r.failActivation(*node, activation, "workflow_budget_exceeded", ErrWorkflowBudgetExceeded.Error())
	}
	if child.Status != model.RunStatusCompleted {
		r.clearWait(&activation)
		return nil, false, r.failActivation(*node, activation, "workflow_child_failed", firstNonEmptyString(child.ErrorMessage, "child run failed"))
	}
	result, err := r.workflowChildResult(*node, *child)
	if err != nil {
		code := workflowFailureCode(err)
		if code == "workflow_node_failed" {
			code = "workflow_child_result_invalid"
		}
		return nil, false, r.failActivation(*node, activation, code, err.Error())
	}
	r.clearWait(&activation)
	r.events = append(r.events,
		newRunEvent(r.run, "workflow.child.completed", activation.StepID, node.ID, map[string]interface{}{workflowPayloadChildRunID: child.RunID, workflowPayloadLLMCalls: settlement.llmCalls, workflowPayloadToolCalls: settlement.toolCalls}, nil),
		newRunEvent(r.run, "workflow.budget.settled", activation.StepID, node.ID, map[string]interface{}{workflowPayloadChildRunID: child.RunID, workflowPayloadLLMCalls: settlement.llmCalls, workflowPayloadToolCalls: settlement.toolCalls}, nil),
	)
	return r.completeActivation(*node, activation, result)
}

func (r *workflowRunner) settleFinishedWorkflowChildBudget(child model.Run, activation *workflowActivationState) (workflowChildSettlement, error) {
	settlement := r.workflowChildSettlement(&child, nil)
	if settlement.llmCalls > activation.ReservedLLM || settlement.toolCalls > activation.ReservedTools {
		return workflowChildSettlement{}, ErrWorkflowBudgetExceeded
	}
	r.applyWorkflowChildSettlementBudget(*activation, settlement)
	activation.ReservedLLM, activation.ReservedTools, activation.ReservedChildren = 0, 0, 0
	return settlement, nil
}

func (r *workflowRunner) workflowChildResult(node model.WorkflowNode, child model.Run) (interface{}, error) {
	result, err := r.loadChildRunValue(child)
	if err != nil {
		return nil, err
	}
	if node.Type != model.WorkflowNodeAgent {
		return result, nil
	}
	result, err = normalizeAgentWorkflowResult(result, node.OutputSchema)
	if err != nil {
		return nil, workflowNodeFailure{Code: "workflow_agent_result_invalid", Message: err.Error()}
	}
	return result, nil
}

func (r *workflowRunner) loadChildRunValue(child model.Run) (interface{}, error) {
	result, err := r.service.repo.GetRunResult(r.ctx, child.Actor, child.RunID)
	if err == nil {
		return decodeWorkflowJSON(json.RawMessage(result.CanonicalJSON))
	}
	if !errors.Is(err, ErrNotFound) || r.service.projectionContent == nil {
		return nil, err
	}
	content, err := r.service.projectionContent.ResolveProjectionContent(r.ctx, ResolveProjectionContentRequest{
		Actor: child.Actor, Thread: child.Thread, Projection: child.OutputProjection,
	})
	if err != nil {
		return nil, err
	}
	return content.Content, nil
}

func normalizeAgentWorkflowResult(value interface{}, schema json.RawMessage) (interface{}, error) {
	candidate := value
	if text, ok := value.(string); ok {
		if decoded, err := decodeWorkflowJSON(json.RawMessage(text)); err == nil {
			candidate = decoded
		}
	}
	if err := validateWorkflowJSON(schema, candidate); err != nil {
		return nil, err
	}
	return candidate, nil
}

func (r *workflowRunner) advanceTool(node *model.WorkflowNode, activation workflowActivationState) (interface{}, bool, error) {
	if activation.EffectID != "" {
		return r.advanceWorkflowEffect(node, activation)
	}
	invocation, err := r.prepareWorkflowToolInvocation(node, activation)
	if err != nil {
		return nil, false, r.failActivation(*node, activation, workflowFailureCode(err), err.Error())
	}
	if workflowToolNeedsApproval(invocation.policy) && !activation.Approved {
		return r.waitForWorkflowToolApproval(node, activation, invocation.tool, invocation.arguments)
	}
	toolRetryCount, remainingToolCalls, err := workflowToolRetryBudget(invocation.policy, r.budget)
	if err != nil {
		return nil, false, r.failActivation(*node, activation, "workflow_budget_exceeded", ErrWorkflowBudgetExceeded.Error())
	}
	call := ToolCall{ToolCallID: deterministicWorkflowID("tool_call", r.run.RunID, activation.Path), ToolName: invocation.tool.ModelName, ArgumentsJSON: invocation.arguments}
	reservedAttempts := min(toolRetryCount+1, remainingToolCalls)
	effectID := workflowEffectID(r.run.RunID, activation.Path)
	r.state.Effects[effectID] = workflowEffectState{
		EffectID:         effectID,
		Kind:             workflowEffectKindTool,
		Status:           workflowEffectStatusIntent,
		ActivationPath:   activation.Path,
		NodeID:           node.ID,
		StepID:           activation.StepID,
		ToolCallID:       call.ToolCallID,
		ToolKey:          invocation.tool.ToolKey,
		ToolName:         invocation.tool.ModelName,
		ArgumentsJSON:    invocation.arguments,
		SideEffectLevel:  invocation.tool.SideEffectLevel,
		IdempotencyMode:  normalizeToolIdempotencyMode(invocation.tool.IdempotencyMode),
		ReservedAttempts: reservedAttempts,
	}
	r.budget.ReservedToolCalls += reservedAttempts
	activation.EffectID = effectID
	activation.ReservedTools = reservedAttempts
	r.saveActivation(activation)
	started := newRunEvent(r.run, "tool.started", activation.StepID, invocation.tool.ModelName, map[string]interface{}{workflowPayloadToolCallID: call.ToolCallID, valueToolKey560014C9: invocation.tool.ToolKey, workflowPayloadNodeID: node.ID}, nil)
	started.ToolCallID, started.ToolName, started.InputJSON = call.ToolCallID, invocation.tool.ModelName, invocation.arguments
	r.events = append(r.events, started)
	return nil, false, nil
}

func workflowToolRetryBudget(policy effectiveRunToolPolicy, budget model.WorkflowBudget) (int, int, error) {
	remaining := budget.Limits.MaxTotalToolCalls - budget.UsedToolCalls - budget.ReservedToolCalls
	if remaining <= 0 {
		return 0, 0, ErrWorkflowBudgetExceeded
	}
	return min(policy.RetryCount, remaining-1), remaining, nil
}

type workflowToolInvocation struct {
	arguments string
	tool      ResolvedTool
	policy    effectiveRunToolPolicy
}

func (r *workflowRunner) prepareWorkflowToolInvocation(node *model.WorkflowNode, activation workflowActivationState) (workflowToolInvocation, error) {
	arguments, err := r.evaluate(node.Arguments, activation.ScopeKey)
	if err != nil {
		return workflowToolInvocation{}, workflowNodeFailure{Code: "workflow_expression_failed", Message: err.Error()}
	}
	argumentsJSON, err := canonicalWorkflowJSON(arguments)
	if err != nil {
		return workflowToolInvocation{}, workflowNodeFailure{Code: "workflow_tool_arguments_invalid", Message: err.Error()}
	}
	tool, policy, err := r.resolveWorkflowTool(node.ToolKey)
	if err != nil {
		return workflowToolInvocation{}, workflowNodeFailure{Code: "workflow_tool_unavailable", Message: err.Error()}
	}
	normalized, err := normalizeToolArgumentsAgainstSchema(string(argumentsJSON), tool.InputSchema)
	if err != nil {
		return workflowToolInvocation{}, workflowNodeFailure{Code: "workflow_tool_arguments_invalid", Message: err.Error()}
	}
	return workflowToolInvocation{arguments: normalized, tool: tool, policy: policy}, nil
}

func (r *workflowRunner) finishWorkflowToolInvocation(node *model.WorkflowNode, activation workflowActivationState, invocation workflowToolInvocation, call ToolCall, execution ToolExecutionResult, executionErr error) (interface{}, bool, error) {
	output := execution.OutputJSON
	if executionErr == nil {
		output, executionErr = normalizeToolOutputAgainstSchema(output, invocation.tool.OutputSchema, invocation.tool.ProviderKind)
	}
	if executionErr == nil {
		if evaluationErr := r.service.evaluateAndPersistRuntimeBoundary(r.ctx, r.run, toolOutputEvaluationRequest(r.run, activation.StepID, invocation.tool, call, output)); evaluationErr != nil {
			output, executionErr = "", evaluationErr
		}
	}
	completedType := "tool.completed"
	if executionErr != nil {
		completedType = "tool.failed"
	}
	completed := newRunEvent(r.run, completedType, activation.StepID, invocation.tool.ModelName, map[string]interface{}{
		workflowPayloadToolCallID: call.ToolCallID,
		workflowPayloadToolCalls:  execution.Attempts,
		valueToolKey560014C9:      invocation.tool.ToolKey,
		"executionReceipt":        execution.Receipt,
	}, nil)
	completed.ToolCallID, completed.ToolName, completed.InputJSON, completed.OutputJSON = call.ToolCallID, invocation.tool.ModelName, invocation.arguments, output
	if executionErr != nil {
		completed.ErrorJSON = mustRunJSON(map[string]interface{}{workflowPayloadError: executionErr.Error()})
		r.events = append(r.events, completed)
		return nil, false, r.failActivation(*node, activation, "workflow_tool_failed", executionErr.Error())
	}
	r.events = append(r.events, completed)
	value, err := decodeWorkflowJSON(json.RawMessage(output))
	if err != nil {
		return nil, false, r.failActivation(*node, activation, "workflow_tool_output_invalid", err.Error())
	}
	return r.completeActivation(*node, activation, value)
}

func (r *workflowRunner) resolveWorkflowTool(toolKey string) (ResolvedTool, effectiveRunToolPolicy, error) {
	if r.service.toolCatalog == nil {
		return ResolvedTool{}, effectiveRunToolPolicy{}, ErrWorkflowDependencyMissing
	}
	workspaceType, workspaceMode := textRunWorkspaceScope(r.effective.Workspace)
	items, unavailable, err := r.service.toolCatalog.ResolveAvailable(r.ctx, r.run.Actor, []string{toolKey}, workspaceType, workspaceMode, r.effective.ThreadModel)
	if err != nil || len(unavailable) != 0 || len(items) != 1 {
		if err == nil {
			err = ErrRunToolUnavailable
		}
		return ResolvedTool{}, effectiveRunToolPolicy{}, err
	}
	tool := items[0]
	dependency, ok := workflowToolDependency(r.definition, toolKey)
	if !ok || tool.DefinitionVersion != dependency.DefinitionVersion {
		return ResolvedTool{}, effectiveRunToolPolicy{}, ErrRunSnapshotIncompatible
	}
	fingerprint, err := hashWorkflowValue(tool)
	if err != nil || fingerprint != dependency.Fingerprint {
		return ResolvedTool{}, effectiveRunToolPolicy{}, ErrRunSnapshotIncompatible
	}
	cfg := r.service.cfg.Snapshot()
	policy, err := snapshotResolvedRunTool(tool, nonNegativeTextRunValue(cfg.Tools.RetryCount), positiveTextRunValue(cfg.Tools.MaxConcurrentCalls))
	return tool, policy, err
}

func workflowToolDependency(definition model.WorkflowDefinition, toolKey string) (model.WorkflowDependency, bool) {
	for _, dependency := range definition.Dependencies {
		if dependency.Kind == model.WorkflowDependencyTool && dependency.ToolKey == toolKey {
			return dependency, true
		}
	}
	return model.WorkflowDependency{}, false
}

func workflowToolNeedsApproval(policy effectiveRunToolPolicy) bool {
	return policy.ApprovalCapability == workflowApprovalPerCall && policy.ApprovalMode != "never"
}

func (r *workflowRunner) waitForWorkflowToolApproval(node *model.WorkflowNode, activation workflowActivationState, tool ResolvedTool, arguments string) (interface{}, bool, error) {
	interactionID := deterministicWorkflowID("interaction", r.run.RunID, activation.Path, "tool_approval")
	expiresAt := r.now.Add(24 * time.Hour)
	schema := `{"type":"object","properties":{"approved":{"type":"boolean"}},"required":["approved"],"additionalProperties":false}`
	request, _ := canonicalWorkflowJSON(map[string]interface{}{"title": "Approve tool execution", valueToolKey560014C9: tool.ToolKey, "toolName": tool.ModelName, "arguments": json.RawMessage(arguments), "sideEffectLevel": tool.SideEffectLevel})
	interaction := model.Interaction{
		InteractionID: interactionID, RunID: r.run.RunID, StepID: activation.StepID, ToolCallID: deterministicWorkflowID("tool_call", r.run.RunID, activation.Path),
		Type: model.InteractionApproveTool, Status: model.InteractionPending, RequestPayloadJSON: string(request),
		ResponseSchemaJSON: schema, RequestedAt: r.now, ExpiresAt: &expiresAt,
	}
	r.interactions[interactionID] = interaction
	r.interactionRows = append(r.interactionRows, interaction)
	r.addWait(&activation, model.WorkflowWaitInteraction, nil, interactionID, "", map[string]interface{}{valueToolKey560014C9: tool.ToolKey})
	r.events = append(r.events,
		newRunEvent(r.run, "interaction.created", activation.StepID, "Tool approval required", map[string]interface{}{workflowPayloadInteractionID: interactionID, valueToolKey560014C9: tool.ToolKey}, nil),
		newRunEvent(r.run, "workflow.node.waiting", activation.StepID, node.ID, map[string]interface{}{workflowPayloadWaitKind: model.WorkflowWaitInteraction, workflowPayloadInteractionID: interactionID}, nil),
		newRunEvent(r.run, "step.waiting_input", activation.StepID, "Tool approval required", map[string]interface{}{workflowPayloadInteractionID: interactionID}, nil),
	)
	return nil, false, nil
}

func (r *workflowRunner) tryWorkflowCache(node model.WorkflowNode, activation workflowActivationState) (interface{}, bool, error) {
	if node.Cache == nil || !node.Cache.Enabled || activation.CacheChecked {
		return nil, false, nil
	}
	activation.CacheChecked = true
	r.saveActivation(activation)
	if r.effective.CacheMode == model.WorkflowCacheBypass || r.effective.CacheMode == model.WorkflowCacheRefresh {
		return nil, false, nil
	}
	key, _, _, err := r.workflowCacheIdentity(node, activation)
	if err != nil {
		return nil, false, err
	}
	entry, err := r.service.repo.GetWorkflowCacheEntry(r.ctx, r.run.Actor, key, r.now)
	if errors.Is(err, ErrNotFound) {
		r.events = append(r.events, newRunEvent(r.run, "workflow.cache.miss", activation.StepID, node.ID, map[string]interface{}{workflowPayloadCacheKey: key}, nil))
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	value, err := decodeWorkflowJSON(json.RawMessage(entry.ValueJSON))
	if err != nil {
		return nil, false, ErrRunSnapshotIncompatible
	}
	r.events = append(r.events, newRunEvent(r.run, "workflow.cache.hit", activation.StepID, node.ID, map[string]interface{}{workflowPayloadCacheKey: key, workflowPayloadContentHash: entry.ContentHash}, nil))
	return value, true, nil
}

func (r *workflowRunner) storeWorkflowCache(node model.WorkflowNode, activation workflowActivationState, output interface{}) {
	if node.Cache == nil || !node.Cache.Enabled || r.effective.CacheMode == model.WorkflowCacheBypass {
		return
	}
	key, inputHash, schemaHash, err := r.workflowCacheIdentity(node, activation)
	if err != nil {
		return
	}
	valueJSON, err := canonicalWorkflowJSON(output)
	if err != nil {
		return
	}
	contentHash, err := hashWorkflowValue(json.RawMessage(valueJSON))
	if err != nil {
		return
	}
	r.cacheEntries = append(r.cacheEntries, model.WorkflowCacheEntry{
		CacheKey: key, Actor: r.run.Actor, WorkflowRef: r.definition.Ref(), NodeID: node.ID,
		DependencyHash: r.definition.DependencyHash, SchemaHash: schemaHash, ContextHash: r.effective.ThreadSnapshotHash,
		InputHash: inputHash, ValueJSON: string(valueJSON), ContentHash: contentHash,
		ExpiresAt: r.now.Add(time.Duration(node.Cache.TTLSeconds) * time.Second),
	})
	r.events = append(r.events, newRunEvent(r.run, "workflow.cache.stored", activation.StepID, node.ID, map[string]interface{}{workflowPayloadCacheKey: key, workflowPayloadContentHash: contentHash}, nil))
}

func (r *workflowRunner) workflowCacheIdentity(node model.WorkflowNode, activation workflowActivationState) (string, string, string, error) {
	input, schemaValue, err := r.workflowCacheMaterial(node, activation)
	if err != nil {
		return "", "", "", err
	}
	inputHash, err := hashWorkflowValue(input)
	if err != nil {
		return "", "", "", err
	}
	schemaHash, err := hashWorkflowValue(schemaValue)
	if err != nil {
		return "", "", "", err
	}
	key, err := hashWorkflowValue(struct {
		Actor          model.ActorRef
		Definition     model.ResourceRef
		NodeID         string
		DependencyHash string
		SchemaHash     string
		ContextHash    string
		InputHash      string
	}{
		Actor: r.run.Actor, Definition: r.definition.Ref(), NodeID: node.ID,
		DependencyHash: r.definition.DependencyHash, SchemaHash: schemaHash,
		ContextHash: r.effective.ThreadSnapshotHash, InputHash: inputHash,
	})
	return key, inputHash, schemaHash, err
}

func (r *workflowRunner) workflowCacheMaterial(node model.WorkflowNode, activation workflowActivationState) (interface{}, interface{}, error) {
	switch node.Type {
	case model.WorkflowNodeAgent:
		return r.evaluateCacheInput(node.Goal, activation.ScopeKey, node.OutputSchema)
	case model.WorkflowNodeTool:
		dependency, ok := workflowToolDependency(r.definition, node.ToolKey)
		if !ok {
			return nil, nil, ErrWorkflowDependencyMissing
		}
		return r.evaluateCacheInput(node.Arguments, activation.ScopeKey, dependency.Fingerprint)
	case model.WorkflowNodeWorkflow:
		definition, err := r.service.repo.GetWorkflowDefinition(r.ctx, r.run.Actor, node.DefinitionRef)
		if err != nil {
			return nil, nil, err
		}
		return r.evaluateCacheInput(node.Input, activation.ScopeKey, definition.OutputSchema)
	default:
		return nil, nil, ErrWorkflowDefinitionInvalid
	}
}

func (r *workflowRunner) evaluateCacheInput(expression *model.WorkflowExpr, scopeKey string, schema interface{}) (interface{}, interface{}, error) {
	input, err := r.evaluate(expression, scopeKey)
	return input, schema, err
}

func (r *workflowRunner) hasPollableWaits() bool {
	for _, wait := range r.state.Waits {
		if wait.Kind == model.WorkflowWaitAgent || wait.Kind == model.WorkflowWaitWorkflow || wait.Kind == model.WorkflowWaitTimer {
			return true
		}
	}
	return false
}

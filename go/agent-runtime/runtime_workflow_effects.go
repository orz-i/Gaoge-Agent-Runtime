package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	workflowEffectKindTool          = "tool"
	workflowEffectStatusIntent      = "intent"
	workflowEffectStatusDispatching = "dispatching"

	workflowEffectFailureAborted = "workflow_tool_aborted_before_dispatch"
	workflowEffectFailureUnknown = "workflow_tool_outcome_unknown"
)

type workflowEffectState struct {
	EffectID         string `json:"effectID"`
	Kind             string `json:"kind"`
	Status           string `json:"status"`
	ActivationPath   string `json:"activationPath"`
	NodeID           string `json:"nodeID"`
	StepID           string `json:"stepID"`
	ToolCallID       string `json:"toolCallID,omitempty"`
	ToolKey          string `json:"toolKey,omitempty"`
	ToolName         string `json:"toolName,omitempty"`
	ArgumentsJSON    string `json:"argumentsJSON,omitempty"`
	SideEffectLevel  string `json:"sideEffectLevel,omitempty"`
	IdempotencyMode  string `json:"idempotencyMode,omitempty"`
	ChildRunID       string `json:"childRunID,omitempty"`
	PayloadJSON      string `json:"payloadJSON,omitempty"`
	ReservedAttempts int    `json:"reservedAttempts,omitempty"`
	DispatchAttempt  int    `json:"dispatchAttempt,omitempty"`
}

type workflowToolEffectPayload struct {
	EffectID string               `json:"effectID"`
	ToolKey  string               `json:"toolKey"`
	Attempts int                  `json:"toolCalls"`
	Receipt  ToolExecutionReceipt `json:"executionReceipt"`
}

func workflowEffectID(runID, activationPath string) string {
	return deterministicWorkflowID("effect", runID, activationPath)
}

func workflowEffectTerminalEventID(effectID string) string {
	return deterministicWorkflowID("effect_terminal", effectID)
}

func (r *workflowRunner) dispatchClaimedWorkflowEffect(ctx context.Context) error {
	effect, ok := r.state.Effects[r.dispatchEffectID]
	if !ok || effect.Status != workflowEffectStatusDispatching {
		return ErrRunSnapshotIncompatible
	}
	switch effect.Kind {
	case workflowEffectKindTool:
		return r.dispatchWorkflowToolEffect(ctx, effect)
	case workflowEffectKindAgent, workflowEffectKindWorkflow:
		return r.dispatchWorkflowChildEffect(ctx, effect)
	default:
		return ErrRunSnapshotIncompatible
	}
}

func (r *workflowRunner) advanceWorkflowEffect(
	node *model.WorkflowNode,
	activation workflowActivationState,
) (interface{}, bool, error) {
	effect, ok := r.state.Effects[activation.EffectID]
	if !ok {
		return nil, false, r.failActivation(*node, activation, "workflow_effect_invalid", ErrRunSnapshotIncompatible.Error())
	}
	switch effect.Kind {
	case workflowEffectKindTool:
		return r.advanceWorkflowToolEffect(node, activation)
	case workflowEffectKindAgent, workflowEffectKindWorkflow:
		return r.advanceWorkflowChildEffect(node, activation)
	default:
		return nil, false, r.failActivation(*node, activation, "workflow_effect_invalid", ErrRunSnapshotIncompatible.Error())
	}
}

func (r *workflowRunner) dispatchWorkflowToolEffect(ctx context.Context, effect workflowEffectState) error {
	tool, policy, err := r.resolveWorkflowTool(effect.ToolKey)
	call := ToolCall{ToolCallID: effect.ToolCallID, ToolName: effect.ToolName, ArgumentsJSON: effect.ArgumentsJSON}
	execution := ToolExecutionResult{}
	if err == nil {
		err = r.service.evaluateAndPersistRuntimeBoundary(ctx, r.run, toolInputEvaluationRequest(r.run, effect.StepID, tool, call))
	}
	if err == nil {
		effective := effectiveTextRunConfig{
			SemanticVersion: RuntimeSnapshotVersion,
			Workspace:       r.effective.Workspace,
			MaxLLMCalls:     r.budget.Limits.MaxTotalLLMCalls,
			MaxToolCalls:    r.budget.Limits.MaxTotalToolCalls,
			ToolRetryCount:  effect.ReservedAttempts - 1,
			ToolConcurrency: policy.Concurrency,
			ToolPolicies:    []effectiveRunToolPolicy{policy},
		}
		execution, err = r.service.executeFrozenToolProvider(
			context.WithValue(ctx, runSegmentKeyContextKey{}, r.run.RunID+":workflow:"+effect.ActivationPath),
			r.run,
			effect.StepID,
			effective,
			tool,
			call,
			&TextRunExecutionLimits{
				MaxLLMCalls:     effective.MaxLLMCalls,
				MaxToolCalls:    effective.MaxToolCalls,
				ToolRetryCount:  effective.ToolRetryCount,
				ToolConcurrency: policy.Concurrency,
			},
		)
	}
	output := execution.OutputJSON
	if err == nil {
		output, err = normalizeToolOutputAgainstSchema(output, tool.OutputSchema, tool.ProviderKind)
	}
	if err == nil {
		if evaluationErr := r.service.evaluateAndPersistRuntimeBoundary(ctx, r.run, toolOutputEvaluationRequest(r.run, effect.StepID, tool, call, output)); evaluationErr != nil {
			output, err = "", evaluationErr
		}
	}
	return r.appendWorkflowToolTerminalEvent(ctx, effect, output, execution, err)
}

func (r *workflowRunner) appendWorkflowToolTerminalEvent(
	ctx context.Context,
	effect workflowEffectState,
	output string,
	execution ToolExecutionResult,
	executionErr error,
) error {
	event := workflowToolTerminalEvent(r.run, effect, output, execution, executionErr, "")
	saved, created, err := r.service.repo.AppendRunEvent(ctx, &event)
	if err != nil {
		return err
	}
	if created {
		r.service.PublishRunNotification(r.run.RunID, runEventEnvelope(saved))
	}
	return nil
}

func workflowToolTerminalEvent(
	run model.Run,
	effect workflowEffectState,
	output string,
	execution ToolExecutionResult,
	executionErr error,
	errorCode string,
) model.Event {
	eventType := valueToolCompleted8D0A12FD
	if executionErr != nil {
		eventType = valueToolFailedFB145984
	}
	event := newRunEvent(run, eventType, effect.StepID, effect.ToolName, map[string]interface{}{
		workflowPayloadToolCallID: effect.ToolCallID,
		workflowPayloadToolCalls:  execution.Attempts,
		valueToolKey560014C9:      effect.ToolKey,
		workflowPayloadEffectID:   effect.EffectID,
		"executionReceipt":        execution.Receipt,
	}, nil)
	event.EventID = workflowEffectTerminalEventID(effect.EffectID)
	event.ToolCallID = effect.ToolCallID
	event.ToolName = effect.ToolName
	event.InputJSON = effect.ArgumentsJSON
	event.OutputJSON = output
	if executionErr != nil {
		failure := map[string]interface{}{workflowPayloadError: executionErr.Error()}
		if strings.TrimSpace(errorCode) != "" {
			failure[workflowPayloadCode] = strings.TrimSpace(errorCode)
		}
		event.ErrorJSON = mustRunJSON(failure)
	}
	return event
}

func (r *workflowRunner) advanceWorkflowToolEffect(
	node *model.WorkflowNode,
	activation workflowActivationState,
) (interface{}, bool, error) {
	effect, ok := r.state.Effects[activation.EffectID]
	if !ok || effect.Kind != workflowEffectKindTool || effect.ActivationPath != activation.Path {
		return nil, false, r.failActivation(*node, activation, "workflow_effect_invalid", ErrRunSnapshotIncompatible.Error())
	}
	terminal, err := r.service.repo.GetRunEvent(
		r.ctx,
		r.run.Actor,
		r.run.RunID,
		workflowEffectTerminalEventID(effect.EffectID),
	)
	if err == nil {
		return r.consumeWorkflowToolEffect(node, activation, effect, *terminal)
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}
	return r.claimOrRecoverWorkflowToolEffect(node, activation, effect)
}

func (r *workflowRunner) claimOrRecoverWorkflowToolEffect(
	node *model.WorkflowNode,
	activation workflowActivationState,
	effect workflowEffectState,
) (interface{}, bool, error) {
	switch effect.Status {
	case workflowEffectStatusIntent:
		if r.drainingEffects {
			return r.failWorkflowToolEffect(
				node,
				activation,
				effect,
				workflowEffectFailureAborted,
				"workflow tool effect was aborted before provider dispatch",
			)
		}
	case workflowEffectStatusDispatching:
		if !workflowToolEffectReplaySafe(effect) {
			return r.failWorkflowToolEffect(
				node,
				activation,
				effect,
				workflowEffectFailureUnknown,
				"workflow tool provider outcome is unknown and cannot be safely replayed",
			)
		}
	default:
		return nil, false, r.failActivation(*node, activation, "workflow_effect_invalid", ErrRunSnapshotIncompatible.Error())
	}
	effect.Status = workflowEffectStatusDispatching
	effect.DispatchAttempt++
	r.state.Effects[effect.EffectID] = effect
	r.dispatchEffectID = effect.EffectID
	r.progress = true
	return nil, false, nil
}

func workflowToolEffectReplaySafe(effect workflowEffectState) bool {
	switch normalizeToolIdempotencyMode(effect.IdempotencyMode) {
	case ToolIdempotencyRequestKey, ToolIdempotencyProviderReceipt:
		return true
	default:
		return strings.EqualFold(strings.TrimSpace(effect.SideEffectLevel), ToolSideEffectRead)
	}
}

func (r *workflowRunner) failWorkflowToolEffect(
	node *model.WorkflowNode,
	activation workflowActivationState,
	effect workflowEffectState,
	code string,
	message string,
) (interface{}, bool, error) {
	terminal := workflowToolTerminalEvent(
		r.run,
		effect,
		"",
		ToolExecutionResult{},
		workflowNodeFailure{Code: code, Message: message},
		code,
	)
	r.events = append(r.events, terminal)
	return r.consumeWorkflowToolEffect(node, activation, effect, terminal)
}

func (r *workflowRunner) consumeWorkflowToolEffect(
	node *model.WorkflowNode,
	activation workflowActivationState,
	effect workflowEffectState,
	terminal model.Event,
) (interface{}, bool, error) {
	payload, err := validatedWorkflowToolEffectPayload(effect, terminal)
	if err != nil {
		return nil, false, r.failActivation(*node, activation, "workflow_effect_invalid", ErrRunSnapshotIncompatible.Error())
	}
	r.budget.ReservedToolCalls = max(0, r.budget.ReservedToolCalls-effect.ReservedAttempts)
	r.budget.UsedToolCalls += payload.Attempts
	r.run.ToolCallsCount += payload.Attempts
	activation.ReservedTools = 0
	activation.EffectID = ""
	delete(r.state.Effects, effect.EffectID)
	r.saveActivation(activation)
	if terminal.EventType == valueToolFailedFB145984 {
		code, message := workflowToolTerminalFailure(terminal)
		return nil, false, r.failActivation(*node, activation, code, message)
	}
	if terminal.EventType != valueToolCompleted8D0A12FD {
		return nil, false, r.failActivation(*node, activation, "workflow_effect_invalid", ErrRunSnapshotIncompatible.Error())
	}
	value, err := decodeWorkflowJSON(json.RawMessage(terminal.OutputJSON))
	if err != nil {
		return nil, false, r.failActivation(*node, activation, "workflow_tool_output_invalid", err.Error())
	}
	return r.completeActivation(*node, activation, value)
}

func validatedWorkflowToolEffectPayload(effect workflowEffectState, terminal model.Event) (workflowToolEffectPayload, error) {
	var payload workflowToolEffectPayload
	if err := json.Unmarshal([]byte(terminal.PayloadJSON), &payload); err != nil {
		return workflowToolEffectPayload{}, ErrRunSnapshotIncompatible
	}
	validIdentity := terminal.EventID == workflowEffectTerminalEventID(effect.EffectID) &&
		terminal.ToolCallID == effect.ToolCallID &&
		payload.EffectID == effect.EffectID &&
		payload.ToolKey == effect.ToolKey
	validAttempts := payload.Attempts >= 0 && payload.Attempts <= effect.ReservedAttempts
	if !validIdentity || !validAttempts {
		return workflowToolEffectPayload{}, ErrRunSnapshotIncompatible
	}
	return payload, nil
}

func workflowToolTerminalFailure(event model.Event) (string, string) {
	var value map[string]interface{}
	if json.Unmarshal([]byte(event.ErrorJSON), &value) == nil {
		code, _ := value[workflowPayloadCode].(string)
		if message, ok := value[workflowPayloadError].(string); ok && message != "" {
			if strings.TrimSpace(code) == "" {
				code = "workflow_tool_failed"
			}
			return code, message
		}
	}
	return "workflow_tool_failed", "workflow tool execution failed"
}

func (r *workflowRunner) drainWorkflowEffects() (bool, error) {
	r.drainingEffects = true
	defer func() { r.drainingEffects = false }()

	for len(r.state.Effects) > 0 {
		before := len(r.state.Effects)
		if err := r.advanceWorkflowEffectDrainRoot(); err != nil {
			return false, err
		}
		if r.dispatchEffectID != "" {
			return false, nil
		}
		if len(r.state.Effects) == 0 {
			return true, nil
		}
		if len(r.state.Effects) < before {
			continue
		}
		if err := r.advanceNextWorkflowEffectForDrain(); err != nil {
			return false, err
		}
		if r.dispatchEffectID != "" {
			return false, nil
		}
		if len(r.state.Effects) >= before {
			return false, ErrRunSnapshotIncompatible
		}
	}
	return true, nil
}

func (r *workflowRunner) advanceWorkflowEffectDrainRoot() error {
	_, _, err := r.advanceNode(&r.definition.Root, r.definition.Root.ID, workflowRootScope, "")
	return ignoreWorkflowNodeFailure(err)
}

func (r *workflowRunner) advanceNextWorkflowEffectForDrain() error {
	effect, err := firstWorkflowEffect(r.state.Effects)
	if err != nil {
		return err
	}
	activation, ok := r.state.Activations[effect.ActivationPath]
	node := workflowNodeByID(&r.definition.Root, effect.NodeID)
	if !ok || node == nil || activation.EffectID != effect.EffectID {
		return ErrRunSnapshotIncompatible
	}
	_, _, err = r.advanceWorkflowEffect(node, activation)
	return ignoreWorkflowNodeFailure(err)
}

func firstWorkflowEffect(effects map[string]workflowEffectState) (workflowEffectState, error) {
	if len(effects) == 0 {
		return workflowEffectState{}, ErrRunSnapshotIncompatible
	}
	effectIDs := make([]string, 0, len(effects))
	for effectID := range effects {
		effectIDs = append(effectIDs, effectID)
	}
	sort.Strings(effectIDs)
	return effects[effectIDs[0]], nil
}

func ignoreWorkflowNodeFailure(err error) error {
	if err == nil {
		return nil
	}
	var failure workflowNodeFailure
	if errors.As(err, &failure) {
		return nil
	}
	return err
}

func workflowNodeByID(node *model.WorkflowNode, nodeID string) *model.WorkflowNode {
	if node == nil {
		return nil
	}
	if node.ID == nodeID {
		return node
	}
	children := make([]*model.WorkflowNode, 0, len(node.Children)+len(node.Branches)+len(node.Stages)+5)
	for index := range node.Children {
		children = append(children, &node.Children[index])
	}
	for index := range node.Branches {
		children = append(children, &node.Branches[index])
	}
	for index := range node.Stages {
		children = append(children, &node.Stages[index])
	}
	children = append(children, node.Then, node.Else, node.Body, node.Do, node.Undo)
	for _, child := range children {
		if found := workflowNodeByID(child, nodeID); found != nil {
			return found
		}
	}
	return nil
}

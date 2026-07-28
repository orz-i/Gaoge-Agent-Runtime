package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	workflowEffectKindAgent    = "agent"
	workflowEffectKindWorkflow = "workflow"

	workflowChildEventStartRequested = "workflow.child.start_requested"
	workflowChildEventStarted        = "workflow.child.started"
	workflowChildEventStartFailed    = "workflow.child.start_failed"

	workflowChildFailureAborted   = "workflow_child_aborted_before_dispatch"
	workflowChildFailureInvalid   = "workflow_child_effect_invalid"
	workflowAgentStartFailureCode = "workflow_agent_start_failed"
)

type workflowAgentEffectPayload struct {
	ClientHandoffID        string            `json:"clientHandoffID"`
	ManifestRef            model.ResourceRef `json:"manifestRef"`
	Goal                   string            `json:"goal"`
	RequestID              string            `json:"requestID"`
	MaxLLMCalls            int               `json:"maxLLMCalls"`
	MaxToolCalls           int               `json:"maxToolCalls"`
	StructuredOutputSchema json.RawMessage   `json:"structuredOutputSchema,omitempty"`
	ResultAttempts         int               `json:"resultAttempts,omitempty"`
}

type workflowNestedEffectPayload struct {
	ClientRunID string               `json:"clientRunID"`
	Definition  model.ResourceRef    `json:"definition"`
	Input       json.RawMessage      `json:"input"`
	Limits      model.WorkflowLimits `json:"limits"`
	RequestID   string               `json:"requestID"`
}

type workflowChildTerminalPayload struct {
	EffectID   string `json:"effectID"`
	ChildRunID string `json:"childRunID"`
	WaitKind   string `json:"waitKind"`
}

func (r *workflowRunner) registerWorkflowChildEffect(
	node model.WorkflowNode,
	activation workflowActivationState,
	kind string,
	childRunID string,
	payload interface{},
) error {
	encoded, err := canonicalWorkflowJSON(payload)
	if err != nil {
		return err
	}
	effectID := workflowEffectID(r.run.RunID, activation.Path)
	r.state.Effects[effectID] = workflowEffectState{
		EffectID:       effectID,
		Kind:           kind,
		Status:         workflowEffectStatusIntent,
		ActivationPath: activation.Path,
		NodeID:         node.ID,
		StepID:         activation.StepID,
		ChildRunID:     childRunID,
		PayloadJSON:    string(encoded),
	}
	activation.EffectID = effectID
	r.saveActivation(activation)
	r.events = append(r.events, newRunEvent(r.run, workflowChildEventStartRequested, activation.StepID, node.ID, map[string]interface{}{
		workflowPayloadEffectID:   effectID,
		workflowPayloadChildRunID: childRunID,
		workflowPayloadWaitKind:   workflowChildWaitKind(kind),
	}, nil))
	r.progress = true
	return nil
}

func (r *workflowRunner) advanceWorkflowChildEffect(
	node *model.WorkflowNode,
	activation workflowActivationState,
) (interface{}, bool, error) {
	effect, ok := r.state.Effects[activation.EffectID]
	if !ok || !workflowChildEffectMatchesNode(effect, *node, activation) {
		return nil, false, r.failActivation(*node, activation, "workflow_effect_invalid", ErrRunSnapshotIncompatible.Error())
	}
	terminal, err := r.service.repo.GetRunEvent(
		r.ctx,
		r.run.Actor,
		r.run.RunID,
		workflowEffectTerminalEventID(effect.EffectID),
	)
	if err == nil {
		return r.consumeWorkflowChildEffect(node, activation, effect, *terminal)
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}
	return r.claimOrRecoverWorkflowChildEffect(node, activation, effect)
}

func workflowChildEffectMatchesNode(effect workflowEffectState, node model.WorkflowNode, activation workflowActivationState) bool {
	if effect.ActivationPath != activation.Path || effect.NodeID != node.ID || strings.TrimSpace(effect.ChildRunID) == "" {
		return false
	}
	return (node.Type == model.WorkflowNodeAgent && effect.Kind == workflowEffectKindAgent) ||
		(node.Type == model.WorkflowNodeWorkflow && effect.Kind == workflowEffectKindWorkflow)
}

func (r *workflowRunner) claimOrRecoverWorkflowChildEffect(
	node *model.WorkflowNode,
	activation workflowActivationState,
	effect workflowEffectState,
) (interface{}, bool, error) {
	waitKind := workflowChildWaitKind(effect.Kind)
	switch effect.Status {
	case workflowEffectStatusIntent:
		if r.drainingEffects {
			return r.failWorkflowChildEffect(node, activation, effect, workflowChildFailureAborted, "workflow child effect was aborted before dispatch")
		}
		r.addWait(&activation, waitKind, nil, "", effect.ChildRunID, workflowChildWaitPayload(effect))
		r.events = append(r.events,
			newRunEvent(r.run, "workflow.node.waiting", activation.StepID, node.ID, map[string]interface{}{workflowPayloadWaitKind: waitKind, workflowPayloadChildRunID: effect.ChildRunID}, nil),
			newRunEvent(r.run, "step.waiting_handoff", activation.StepID, node.ID, map[string]interface{}{workflowPayloadChildRunID: effect.ChildRunID}, nil),
		)
	case workflowEffectStatusDispatching:
		if activation.WaitID == "" || activation.ChildRunID != effect.ChildRunID || activation.Status != model.WorkflowStepStatusWaiting {
			return nil, false, r.failActivation(*node, activation, "workflow_effect_invalid", ErrRunSnapshotIncompatible.Error())
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

func workflowChildWaitKind(kind string) string {
	if kind == workflowEffectKindAgent {
		return model.WorkflowWaitAgent
	}
	if kind == workflowEffectKindWorkflow {
		return model.WorkflowWaitWorkflow
	}
	return ""
}

func workflowChildWaitPayload(effect workflowEffectState) map[string]interface{} {
	return map[string]interface{}{
		workflowPayloadEffectID:   effect.EffectID,
		workflowPayloadChildRunID: effect.ChildRunID,
		workflowPayloadWaitKind:   workflowChildWaitKind(effect.Kind),
	}
}

func (r *workflowRunner) dispatchWorkflowChildEffect(ctx context.Context, effect workflowEffectState) error {
	childRunID, err := r.startWorkflowChildEffect(ctx, effect)
	if err == nil && childRunID != effect.ChildRunID {
		err = ErrRunSnapshotIncompatible
	}
	return r.appendWorkflowChildTerminalEvent(ctx, effect, childRunID, err)
}

func (r *workflowRunner) startWorkflowChildEffect(ctx context.Context, effect workflowEffectState) (string, error) {
	switch effect.Kind {
	case workflowEffectKindAgent:
		return r.startWorkflowAgentEffect(ctx, effect)
	case workflowEffectKindWorkflow:
		return r.startNestedWorkflowEffect(ctx, effect)
	default:
		return "", ErrRunSnapshotIncompatible
	}
}

func (r *workflowRunner) startWorkflowAgentEffect(ctx context.Context, effect workflowEffectState) (string, error) {
	var payload workflowAgentEffectPayload
	if err := json.Unmarshal([]byte(effect.PayloadJSON), &payload); err != nil {
		return "", ErrRunSnapshotIncompatible
	}
	result, err := r.service.DelegateTextRun(ctx, DelegateTextRunInput{
		Actor:                  r.run.Actor,
		ParentRunID:            r.run.RunID,
		ClientHandoffID:        payload.ClientHandoffID,
		AgentManifest:          payload.ManifestRef,
		Goal:                   payload.Goal,
		ContentType:            workflowContentTypeText,
		RequestID:              payload.RequestID,
		MaxLLMCalls:            payload.MaxLLMCalls,
		MaxToolCalls:           payload.MaxToolCalls,
		StructuredOutputSchema: append(json.RawMessage(nil), payload.StructuredOutputSchema...),
		ResultAttempts:         payload.ResultAttempts,
	})
	if err != nil {
		return "", err
	}
	return result.Run.RunID, nil
}

func (r *workflowRunner) startNestedWorkflowEffect(ctx context.Context, effect workflowEffectState) (string, error) {
	var payload workflowNestedEffectPayload
	if err := json.Unmarshal([]byte(effect.PayloadJSON), &payload); err != nil {
		return "", ErrRunSnapshotIncompatible
	}
	limits := payload.Limits
	start, err := r.service.StartWorkflow(ctx, StartWorkflowInput{
		Actor:            r.run.Actor,
		Thread:           r.run.Thread,
		RequestID:        payload.RequestID,
		ClientRunID:      payload.ClientRunID,
		Definition:       payload.Definition,
		Input:            append(json.RawMessage(nil), payload.Input...),
		Environment:      r.run.Environment,
		Limits:           &limits,
		CacheMode:        r.effective.CacheMode,
		ParentProjection: &r.run.OutputProjection,
		ThreadModel:      r.effective.ThreadModel,
		ThreadScope:      workflowEffectiveThreadScope(r.effective),
		FrozenWorkspace:  r.effective.Workspace,
		ParentRunID:      r.run.RunID,
		RootRunID:        r.execution.RootRunID,
		BudgetOwnerRunID: r.execution.BudgetOwnerRunID,
		Depth:            r.run.Depth + 1,
	})
	if err != nil {
		return "", err
	}
	return start.Run.RunID, nil
}

func (r *workflowRunner) appendWorkflowChildTerminalEvent(ctx context.Context, effect workflowEffectState, childRunID string, startErr error) error {
	event := workflowChildTerminalEvent(r.run, effect, childRunID, startErr, "")
	saved, created, err := r.service.repo.AppendRunEvent(ctx, &event)
	if err != nil {
		return err
	}
	if created {
		r.service.PublishRunNotification(r.run.RunID, runEventEnvelope(saved))
	}
	return nil
}

func workflowChildTerminalEvent(run model.Run, effect workflowEffectState, childRunID string, startErr error, errorCode string) model.Event {
	eventType := workflowChildEventStarted
	if startErr != nil {
		eventType = workflowChildEventStartFailed
	}
	payload := workflowChildTerminalPayload{
		EffectID:   effect.EffectID,
		ChildRunID: firstNonEmptyString(childRunID, effect.ChildRunID),
		WaitKind:   workflowChildWaitKind(effect.Kind),
	}
	event := newRunEvent(run, eventType, effect.StepID, effect.NodeID, map[string]interface{}{
		workflowPayloadEffectID:   payload.EffectID,
		workflowPayloadChildRunID: payload.ChildRunID,
		workflowPayloadWaitKind:   payload.WaitKind,
		valueKindE5B2EFB3:         payload.WaitKind,
	}, nil)
	event.EventID = workflowEffectTerminalEventID(effect.EffectID)
	if startErr != nil {
		failure := map[string]interface{}{workflowPayloadError: startErr.Error()}
		if strings.TrimSpace(errorCode) != "" {
			failure[workflowPayloadCode] = strings.TrimSpace(errorCode)
		}
		event.ErrorJSON = mustRunJSON(failure)
	}
	return event
}

func (r *workflowRunner) consumeWorkflowChildEffect(
	node *model.WorkflowNode,
	activation workflowActivationState,
	effect workflowEffectState,
	terminal model.Event,
) (interface{}, bool, error) {
	if err := validateWorkflowChildTerminal(effect, terminal); err != nil {
		return nil, false, r.failActivation(*node, activation, "workflow_effect_invalid", ErrRunSnapshotIncompatible.Error())
	}
	activation.EffectID = ""
	delete(r.state.Effects, effect.EffectID)
	r.saveActivation(activation)
	if terminal.EventType == workflowChildEventStartFailed {
		code, message := workflowChildTerminalFailure(effect.Kind, terminal)
		r.clearFailedWorkflowChildStart(&activation)
		return nil, false, r.failActivation(*node, activation, code, message)
	}
	if terminal.EventType != workflowChildEventStarted {
		return nil, false, r.failActivation(*node, activation, "workflow_effect_invalid", ErrRunSnapshotIncompatible.Error())
	}
	return nil, false, nil
}

func validateWorkflowChildTerminal(effect workflowEffectState, terminal model.Event) error {
	var payload workflowChildTerminalPayload
	if err := json.Unmarshal([]byte(terminal.PayloadJSON), &payload); err != nil {
		return ErrRunSnapshotIncompatible
	}
	if terminal.EventID != workflowEffectTerminalEventID(effect.EffectID) ||
		payload.EffectID != effect.EffectID ||
		payload.ChildRunID != effect.ChildRunID ||
		payload.WaitKind != workflowChildWaitKind(effect.Kind) {
		return ErrRunSnapshotIncompatible
	}
	return nil
}

func (r *workflowRunner) failWorkflowChildEffect(
	node *model.WorkflowNode,
	activation workflowActivationState,
	effect workflowEffectState,
	code string,
	message string,
) (interface{}, bool, error) {
	terminal := workflowChildTerminalEvent(r.run, effect, effect.ChildRunID, workflowNodeFailure{Code: code, Message: message}, code)
	r.events = append(r.events, terminal)
	return r.consumeWorkflowChildEffect(node, activation, effect, terminal)
}

func (r *workflowRunner) clearFailedWorkflowChildStart(activation *workflowActivationState) {
	if activation.WaitID != "" {
		r.clearWait(activation)
	}
	activation.ChildRunID = ""
	step := r.steps[activation.StepID]
	step.ChildRunID = ""
	r.steps[activation.StepID] = step
	r.changedSteps[activation.StepID] = struct{}{}
	r.releaseFailedChildReservation(activation)
}

func workflowChildTerminalFailure(kind string, event model.Event) (string, string) {
	defaultCode := "nested_workflow_start_failed"
	if kind == workflowEffectKindAgent {
		defaultCode = workflowAgentStartFailureCode
	}
	var failure map[string]interface{}
	if json.Unmarshal([]byte(event.ErrorJSON), &failure) == nil {
		code, _ := failure[workflowPayloadCode].(string)
		message, _ := failure[workflowPayloadError].(string)
		if strings.TrimSpace(code) == "" {
			code = defaultCode
		}
		if strings.TrimSpace(message) != "" {
			return code, message
		}
	}
	return defaultCode, "workflow child start failed"
}

func (r *workflowRunner) resumeWorkflowChild(node *model.WorkflowNode, activation workflowActivationState) (interface{}, bool, error) {
	if activation.EffectID != "" {
		_, _, err := r.advanceWorkflowChildEffect(node, activation)
		if err != nil {
			return nil, false, err
		}
		if r.dispatchEffectID != "" {
			return nil, false, nil
		}
		activation = r.state.Activations[activation.Path]
		if activation.EffectID != "" {
			return nil, false, nil
		}
	}
	return r.resumeChildRun(node, activation)
}

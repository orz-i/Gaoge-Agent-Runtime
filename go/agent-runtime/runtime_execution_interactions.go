package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func (s *Engine) GetPlan(ctx context.Context, actor model.ActorRef, runID string) (*PlanView, error) {
	current, err := s.repo.GetCurrentPlan(ctx, actor, runID)
	if err != nil {
		return nil, err
	}
	revisions, err := s.repo.ListPlans(ctx, actor, runID)
	if err != nil {
		return nil, err
	}
	steps, err := s.repo.ListRunSteps(ctx, runID)
	if err != nil {
		return nil, err
	}
	return &PlanView{Current: current, Revisions: revisions, Steps: steps}, nil
}

func (s *Engine) ListRunInteractions(ctx context.Context, actor model.ActorRef, runID string) ([]model.Interaction, error) {
	return s.repo.ListRunInteractions(ctx, actor, runID)
}

func (s *Engine) ListRunCheckpoints(ctx context.Context, actor model.ActorRef, runID string) ([]model.Checkpoint, error) {
	return s.repo.ListRunCheckpoints(ctx, actor, runID)
}

func (s *Engine) ListOutputs(ctx context.Context, actor model.ActorRef, runID string) ([]model.OutputRef, error) {
	return s.repo.ListOutputs(ctx, actor, runID)
}

func (s *Engine) ListUserOutputs(ctx context.Context, actor model.ActorRef, query, cursor string, limit int) ([]model.OutputListItem, string, error) {
	if !validActorRef(actor) {
		return nil, "", ErrInvalidInput
	}
	return s.repo.ListUserOutputs(ctx, actor, strings.TrimSpace(query), strings.TrimSpace(cursor), limit)
}

func (s *Engine) ResolveRunInteraction(ctx context.Context, input ResolveRunInteractionInput) (*model.Interaction, error) {
	if !validResolveRunInteractionInput(input) {
		return nil, ErrInvalidInput
	}
	if run, err := s.repo.GetRun(ctx, input.Actor, input.RunID); err != nil {
		return nil, err
	} else if run.RuntimeKind == model.RuntimeKindWorkflow {
		return s.resolveWorkflowRunInteraction(ctx, *run, input)
	}
	prepared, err := s.prepareInteractionResolution(ctx, input)
	if err != nil {
		return nil, err
	}
	bundle, err := s.buildInteractionResolutionBundle(ctx, input, prepared.run, prepared.interaction, prepared.resolution)
	if err != nil {
		return nil, err
	}
	effective, err := s.validateInteractionResolutionRuntime(ctx, prepared.run)
	if err != nil {
		return nil, err
	}
	var reservation *UsageBalanceReservation
	if prepared.resolution.shouldContinue {
		reservation, _, err = s.ReserveRunUsageBalance(ctx, RunBillingInput{Actor: prepared.run.Actor, Thread: prepared.run.Thread, PlatformModelName: effective.PlatformModelName, ClientRunID: prepared.run.RunID + ":resolve:" + input.ClientResolveID})
		if err != nil {
			return nil, err
		}
	}
	return s.commitInteractionResolution(ctx, input, prepared.run, prepared.interaction, prepared.responseJSON, prepared.fingerprint, prepared.resolution, bundle, reservation)
}

func (s *Engine) prepareInteractionResolution(ctx context.Context, input ResolveRunInteractionInput) (preparedInteractionResolution, error) {
	if !validResolveRunInteractionInput(input) {
		return preparedInteractionResolution{}, ErrInvalidInput
	}
	run, err := s.repo.GetRun(ctx, input.Actor, input.RunID)
	if err != nil {
		return preparedInteractionResolution{}, err
	}
	interaction, err := s.repo.GetRunInteraction(ctx, input.Actor, input.RunID, input.InteractionID)
	if err != nil {
		return preparedInteractionResolution{}, err
	}
	responseJSON, responseMap, err := normalizeRunInteractionResponse(input.Response)
	if err != nil {
		return preparedInteractionResolution{}, ErrRunInteractionResponseInvalid
	}
	if err = validateRunInteractionResponse(interaction.ResponseSchemaJSON, responseMap); err != nil {
		return preparedInteractionResolution{}, err
	}
	resolution := newInteractionResolution(*run, *interaction, responseJSON, responseMap)
	if err = s.applyInteractionResolution(ctx, input, *run, *interaction, responseMap, &resolution); err != nil {
		return preparedInteractionResolution{}, err
	}
	if err = s.appendSupersededPlanSteps(ctx, *run, &resolution); err != nil {
		return preparedInteractionResolution{}, err
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(responseJSON)))
	return preparedInteractionResolution{run: *run, interaction: *interaction, responseJSON: responseJSON, fingerprint: fingerprint, resolution: resolution}, nil
}

func (s *Engine) buildInteractionResolutionBundle(ctx context.Context, input ResolveRunInteractionInput, run model.Run, interaction model.Interaction, resolution interactionResolution) (interactionResolutionBundle, error) {
	bundle := interactionResolutionBundle{stepID: interaction.StepID, events: resolution.events}
	if !resolution.shouldContinue {
		return bundle, nil
	}
	if resolution.reviseFeedback != "" {
		root, err := s.runRootStep(ctx, run.RunID)
		if err != nil {
			return interactionResolutionBundle{}, err
		}
		bundle.stepID = root.StepID
	}
	continuation, err := buildRunResolutionContinuation(run, interaction, bundle.stepID, run.RunID+":resolve:"+input.ClientResolveID, resolution.reviseFeedback, resolution.nextRevision, resolution.approvedTool, resolution.frozenApprovedTool)
	if err != nil {
		return interactionResolutionBundle{}, err
	}
	bundle.checkpoint = newRunContinuationCheckpoint(run, bundle.stepID, "post_interaction", continuation)
	bundle.checkpoint.CheckpointID = deterministicRunCheckpointID(run.RunID, interaction.InteractionID, input.ClientResolveID, "post_interaction")
	bundle.events = append(bundle.events, newRunEvent(run, "checkpoint.created", bundle.stepID, "Post-interaction execution checkpoint", map[string]interface{}{valueCheckpointID9CD08C70: bundle.checkpoint.CheckpointID, valueKindE5B2EFB3: bundle.checkpoint.Kind, valueContinuationTypeDCB4DE9C: continuation.Type}, nil))
	resumed := newRunEvent(run, "run.resumed", bundle.stepID, "Text run resumed", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID, valueStatus327C4193: resolution.nextStatus}, nil)
	resumed.Status = resolution.nextStatus
	bundle.events = append(bundle.events, resumed)
	return bundle, nil
}

func (s *Engine) validateInteractionResolutionRuntime(ctx context.Context, run model.Run) (effectiveTextRunConfig, error) {
	var effective effectiveTextRunConfig
	if json.Unmarshal([]byte(run.RunConfigSnapshotJSON), &effective) != nil || effective.SemanticVersion != RuntimeSnapshotVersion {
		return effectiveTextRunConfig{}, ErrInvalidInput
	}
	if _, err := s.loadTextRunContextMessages(ctx, run); err != nil {
		return effectiveTextRunConfig{}, ErrRunSnapshotIncompatible
	}
	if _, err := s.resolveRunTools(ctx, run.Actor, effective); err != nil {
		return effectiveTextRunConfig{}, ErrRunSnapshotIncompatible
	}
	return effective, nil
}

func (s *Engine) commitInteractionResolution(ctx context.Context, input ResolveRunInteractionInput, run model.Run, interaction model.Interaction, responseJSON, fingerprint string, resolution interactionResolution, bundle interactionResolutionBundle, reservation *UsageBalanceReservation) (*model.Interaction, error) {
	var resolved *model.Interaction
	var continuationCheckpoint *model.Checkpoint
	var saved []model.Event
	var applied bool
	work := func(txCtx context.Context) error {
		var resolveErr error
		resolved, continuationCheckpoint, saved, applied, resolveErr = s.repo.ResolveRunInteractionWithCheckpoint(txCtx, input.Actor, input.RunID, input.InteractionID, input.ClientResolveID, responseJSON, fingerprint, resolution.nextStatus, bundle.checkpoint, bundle.events)
		if resolveErr != nil || !applied || !resolution.shouldContinue {
			return resolveErr
		}
		if continuationCheckpoint == nil {
			return ErrRunSnapshotIncompatible
		}
		run.Status, run.PendingInteractionID = resolution.nextStatus, ""
		resolveErr = s.createContinuationJob(txCtx, run, *continuationCheckpoint, "interaction_resolve", reservation)
		return resolveErr
	}
	var err error
	if s.unitOfWork == nil {
		err = ErrHostProjectionUnavailable
	} else {
		err = s.unitOfWork.Within(ctx, work)
	}
	if err != nil {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "运行交互解决失败退回预扣")
		return nil, err
	}
	if !applied {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "运行交互幂等复用退回预扣")
		return resolved, nil
	}
	s.publishRunEvents(run.RunID, saved)
	if !resolution.shouldContinue {
		_ = s.cancelTextRun(context.WithoutCancel(ctx), run, interaction.StepID, "Interaction rejected")
		s.FinishRunNotifications(run.RunID)
		return resolved, nil
	}
	s.wakeContinuationJobs()
	return resolved, nil
}

func validResolveRunInteractionInput(input ResolveRunInteractionInput) bool {
	return validActorRef(input.Actor) && strings.TrimSpace(input.RunID) != "" && strings.TrimSpace(input.InteractionID) != "" && strings.TrimSpace(input.ClientResolveID) != "" && len(input.ClientResolveID) <= 64
}

func newInteractionResolution(run model.Run, interaction model.Interaction, responseJSON string, response map[string]interface{}) interactionResolution {
	events := make([]model.Event, 0, 4)
	if interaction.Type == model.InteractionAskUser {
		toolResult := newRunEvent(run, valueToolCompleted8D0A12FD, interaction.StepID, runControlAskUser, map[string]interface{}{valueToolCallID64CA70DB: interaction.ToolCallID, valueToolName4234B607: runControlAskUser, valueAnswer89191F03: response[valueAnswer89191F03]}, nil)
		toolResult.ToolCallID, toolResult.ToolName, toolResult.OutputJSON = interaction.ToolCallID, runControlAskUser, responseJSON
		events = append(events, toolResult)
	}
	events = append(events, newRunEvent(run, "interaction.resolved", interaction.StepID, "Interaction resolved", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID, valueType5EE8C955: interaction.Type}, nil))
	return interactionResolution{nextStatus: model.RunStatusRunning, events: events, shouldContinue: true}
}

func (s *Engine) applyInteractionResolution(ctx context.Context, input ResolveRunInteractionInput, run model.Run, interaction model.Interaction, response map[string]interface{}, resolution *interactionResolution) error {
	switch interaction.Type {
	case model.InteractionSubmitPlan:
		return s.applyPlanInteractionResolution(ctx, input, run, interaction, response, resolution)
	case model.InteractionAskUser:
		return validateAskUserResolution(response)
	case model.InteractionApproveTool:
		return applyToolInteractionResolution(run, interaction, response, resolution)
	case model.InteractionApproveToolSet:
		return applyToolSetInteractionResolution(run, interaction, response, resolution)
	case model.InteractionApproveStep:
		return s.applyStepInteractionResolution(ctx, input, run, interaction, response, resolution)
	default:
		return ErrInvalidInput
	}
}

func (s *Engine) applyPlanInteractionResolution(ctx context.Context, input ResolveRunInteractionInput, run model.Run, interaction model.Interaction, response map[string]interface{}, resolution *interactionResolution) error {
	action, _ := response["action"].(string)
	switch strings.TrimSpace(action) {
	case valueApproveFF07A766:
		resolution.events = append(resolution.events, newRunEvent(run, "plan.approved", interaction.StepID, "Plan approved", map[string]interface{}{valuePlanID320F2BB9: run.CurrentPlanID, valueMode06EC588F: valueUser19341906}, nil))
		return nil
	case valueRevise9EA811FD:
		feedback, _ := response[valueFeedback83F69355].(string)
		return s.applyPlanRevision(ctx, input, run, interaction, feedback, false, resolution)
	case "reject":
		resolution.shouldContinue = false
		resolution.events = append(resolution.events, newRunEvent(run, "plan.rejected", interaction.StepID, "Plan rejected", map[string]interface{}{valuePlanID320F2BB9: run.CurrentPlanID}, nil))
		return nil
	default:
		return ErrInvalidInput
	}
}

func validateAskUserResolution(response map[string]interface{}) error {
	answer, _ := response[valueAnswer89191F03].(string)
	if strings.TrimSpace(answer) == "" {
		return ErrRunInteractionResponseInvalid
	}
	return nil
}

func applyToolInteractionResolution(run model.Run, interaction model.Interaction, response map[string]interface{}, resolution *interactionResolution) error {
	resolution.approvedTool, _ = response["approved"].(bool)
	var frozen runFrozenToolCall
	if err := json.Unmarshal([]byte(interaction.RequestPayloadJSON), &frozen); err != nil || frozen.ToolCallID == "" {
		return ErrRunSnapshotIncompatible
	}
	if resolution.approvedTool {
		resolution.frozenApprovedTool = &frozen
		return nil
	}
	denied := newRunEvent(run, valueToolFailedFB145984, interaction.StepID, "Tool execution denied by user", map[string]interface{}{valueToolCallID64CA70DB: frozen.ToolCallID, valueToolName4234B607: frozen.ToolName, valueStatus327C4193: "user_denied"}, nil)
	denied.ToolCallID, denied.ToolName = frozen.ToolCallID, frozen.ToolName
	denied.ErrorJSON = mustRunJSON(map[string]interface{}{valueErrorA8DE48C2: "user_denied"})
	resolution.events = append([]model.Event{denied}, resolution.events...)
	return nil
}

func applyToolSetInteractionResolution(run model.Run, interaction model.Interaction, response map[string]interface{}, resolution *interactionResolution) error {
	approved, _ := response["approved"].(bool)
	if !approved {
		resolution.shouldContinue = false
		resolution.events = append(resolution.events, newRunEvent(run, "tool_set.rejected", interaction.StepID, "Hosted tool activation rejected", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID}, nil))
		return nil
	}
	var request struct {
		ContinuationType string `json:"continuationType"`
	}
	if json.Unmarshal([]byte(interaction.RequestPayloadJSON), &request) != nil {
		return ErrRunSnapshotIncompatible
	}
	if request.ContinuationType == runContinuationStartPlanning {
		resolution.nextStatus = model.RunStatusPreparing
	}
	resolution.events = append(resolution.events, newRunEvent(run, "tool_set.approved", interaction.StepID, "Hosted tool activation approved", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID}, nil))
	return nil
}

func (s *Engine) applyStepInteractionResolution(ctx context.Context, input ResolveRunInteractionInput, run model.Run, interaction model.Interaction, response map[string]interface{}, resolution *interactionResolution) error {
	action, _ := response["action"].(string)
	switch strings.TrimSpace(action) {
	case valueApproveFF07A766:
		resolution.events = append(resolution.events, newRunEvent(run, "step.approved", interaction.StepID, "Step approved", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID, valueStepID23C5C586: interaction.StepID}, nil))
		return nil
	case valueRevise9EA811FD:
		feedback, _ := response[valueFeedback83F69355].(string)
		return s.applyPlanRevision(ctx, input, run, interaction, feedback, true, resolution)
	default:
		return ErrInvalidInput
	}
}

func (s *Engine) applyPlanRevision(ctx context.Context, input ResolveRunInteractionInput, run model.Run, interaction model.Interaction, feedback string, stepRevision bool, resolution *interactionResolution) error {
	if strings.TrimSpace(feedback) == "" {
		return ErrRunInteractionResponseInvalid
	}
	var config effectiveTextRunConfig
	if json.Unmarshal([]byte(run.RunConfigSnapshotJSON), &config) != nil {
		if stepRevision {
			return ErrRunSnapshotIncompatible
		}
		return ErrInvalidInput
	}
	plans, err := s.repo.ListPlans(ctx, input.Actor, input.RunID)
	if err != nil {
		return err
	}
	if len(plans) >= config.PlanMaxRevisions {
		return ErrPlanRevisionLimit
	}
	resolution.reviseFeedback, resolution.nextRevision, resolution.nextStatus = feedback, len(plans)+1, model.RunStatusPreparing
	payload := map[string]interface{}{valuePlanID320F2BB9: run.CurrentPlanID, valueFeedback83F69355: feedback}
	title := "Plan revision requested"
	if stepRevision {
		payload[valueStepID23C5C586], title = interaction.StepID, "Step revision requested"
	}
	resolution.events = append(resolution.events, newRunEvent(run, "plan.revised", interaction.StepID, title, payload, nil))
	return nil
}

func (s *Engine) appendSupersededPlanSteps(ctx context.Context, run model.Run, resolution *interactionResolution) error {
	if resolution.reviseFeedback == "" {
		return nil
	}
	steps, err := s.repo.ListRunSteps(ctx, run.RunID)
	if err != nil {
		return err
	}
	for _, step := range steps {
		if step.PlanID == run.CurrentPlanID && supersedableRunStepStatus(step.Status) {
			resolution.events = append(resolution.events, newRunEvent(run, "step.skipped", step.StepID, "Plan superseded by user feedback", map[string]interface{}{valuePlanID320F2BB9: step.PlanID, valueReasonB5B063AA: "plan_superseded"}, nil))
		}
	}
	return nil
}

func supersedableRunStepStatus(status string) bool {
	return status == model.RunStatusQueued || status == model.RunStatusWaitingInput || status == model.RunStatusRunning || status == model.RunStatusSuspended
}

func buildRunResolutionContinuation(run model.Run, interaction model.Interaction, executionStepID, segmentKey, reviseFeedback string, nextRevision int, approvedTool bool, frozenTool *runFrozenToolCall) (runContinuation, error) {
	continuation := runContinuation{SemanticVersion: RuntimeSnapshotVersion, SegmentKey: strings.TrimSpace(segmentKey), Type: runContinuationContinuePlan, TargetStatus: model.RunStatusRunning, InteractionID: interaction.InteractionID, PlanID: run.CurrentPlanID, StepID: executionStepID}
	if strings.TrimSpace(reviseFeedback) != "" {
		continuation.Type, continuation.TargetStatus, continuation.SourceStepID, continuation.Feedback, continuation.NextRevision = runContinuationReplan, model.RunStatusPreparing, interaction.StepID, reviseFeedback, nextRevision
	} else if err := applyInteractionResolutionContinuation(&continuation, interaction, approvedTool, frozenTool); err != nil {
		return runContinuation{}, err
	}
	if err := validateRunContinuation(continuation); err != nil {
		return runContinuation{}, err
	}
	return continuation, nil
}

func applyInteractionResolutionContinuation(continuation *runContinuation, interaction model.Interaction, approvedTool bool, frozenTool *runFrozenToolCall) error {
	switch interaction.Type {
	case model.InteractionAskUser:
		continuation.DurableToolResult = &runDurableToolResult{ToolCallID: interaction.ToolCallID, EventType: valueToolCompleted8D0A12FD}
	case model.InteractionApproveTool:
		if approvedTool {
			continuation.Type, continuation.FrozenToolCall = runContinuationExecuteApprovedTool, frozenTool
		} else {
			continuation.DurableToolResult = &runDurableToolResult{ToolCallID: interaction.ToolCallID, EventType: valueToolFailedFB145984}
		}
	case model.InteractionApproveToolSet:
		return applyToolSetResolutionContinuation(continuation, interaction.RequestPayloadJSON)
	}
	return nil
}

func applyToolSetResolutionContinuation(continuation *runContinuation, requestJSON string) error {
	var request struct {
		ContinuationType string `json:"continuationType"`
	}
	if json.Unmarshal([]byte(requestJSON), &request) != nil {
		return ErrRunSnapshotIncompatible
	}
	switch request.ContinuationType {
	case runContinuationStartDirect:
		continuation.Type, continuation.TargetStatus = runContinuationStartDirect, model.RunStatusRunning
	case runContinuationStartPlanning:
		continuation.Type, continuation.TargetStatus, continuation.NextRevision = runContinuationStartPlanning, model.RunStatusPreparing, 1
	default:
		return ErrRunSnapshotIncompatible
	}
	return nil
}

func (s *Engine) ensureHostedToolSetApproval(ctx context.Context, run model.Run, effective effectiveTextRunConfig, continuation runContinuation) (bool, error) {
	toolKeys := inheritedHighRiskHostedToolKeys(effective.ToolPolicies)
	if len(toolKeys) == 0 {
		return false, nil
	}
	interactions, err := s.repo.ListRunInteractions(ctx, run.Actor, run.RunID)
	if err != nil {
		return false, err
	}
	if found, waiting := hostedToolSetApprovalState(interactions); found {
		return waiting, nil
	}
	return s.createHostedToolSetApproval(ctx, run, effective, continuation, toolKeys)
}

func inheritedHighRiskHostedToolKeys(policies []effectiveRunToolPolicy) []string {
	toolKeys := make([]string, 0)
	for _, policy := range policies {
		if policy.ExecutionMode == valueProviderHostedF3C237B6 && policy.RiskLevel == valueHighB19D217F {
			toolKeys = append(toolKeys, policy.ToolKey)
		}
	}
	sort.Strings(toolKeys)
	return toolKeys
}

func hostedToolSetApprovalState(interactions []model.Interaction) (bool, bool) {
	for _, interaction := range interactions {
		if interaction.Type != model.InteractionApproveToolSet {
			continue
		}
		switch interaction.Status {
		case model.InteractionPending:
			return true, true
		case model.InteractionResolved:
			var response struct {
				Approved bool `json:"approved"`
			}
			approved := json.Unmarshal([]byte(interaction.ResponseJSON), &response) == nil && response.Approved
			return true, !approved
		}
	}
	return false, false
}

func (s *Engine) createHostedToolSetApproval(ctx context.Context, run model.Run, effective effectiveTextRunConfig, continuation runContinuation, toolKeys []string) (bool, error) {
	request := map[string]interface{}{"toolKeys": toolKeys, valueContinuationTypeDCB4DE9C: continuation.Type}
	interaction := newRunInteraction(run, continuation.StepID, model.InteractionApproveToolSet, request, effective.InteractionTTLHours)
	checkpoint, err := newRunInteractionCheckpoint(run, interaction, "approve_tool_set")
	if err != nil {
		return false, err
	}
	events := []model.Event{
		newRunEvent(run, "checkpoint.created", continuation.StepID, "Hosted tool activation checkpoint", map[string]interface{}{valueCheckpointID9CD08C70: checkpoint.CheckpointID, valueKindE5B2EFB3: checkpoint.Kind}, nil),
		newRunEvent(run, "interaction.created", continuation.StepID, "Hosted tool activation required", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID, valueType5EE8C955: interaction.Type, "toolKeys": toolKeys}, nil),
		newRunEvent(run, "run.waiting_input", continuation.StepID, "Waiting for hosted tool activation", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID, valueReasonB5B063AA: "approve_tool_set"}, nil),
	}
	saved, err := s.repo.CreateRunInteractionBundle(context.WithoutCancel(ctx), run.RunID, continuation.TargetStatus, interaction, checkpoint, events)
	if err != nil {
		return false, err
	}
	s.publishRunEvents(run.RunID, saved)
	return true, nil
}

func (s *Engine) executeRunContinuation(ctx context.Context, run model.Run, root model.Step, effective effectiveTextRunConfig, reservation *UsageBalanceReservation, checkpoint model.Checkpoint, source string) {
	continuation, err := decodeRunContinuation(checkpoint)
	if err != nil {
		_ = s.ReleaseRunUsageReservation(context.WithoutCancel(ctx), reservation, "Text Run continuation 不兼容退回预扣")
		s.failTextRun(context.WithoutCancel(ctx), run, checkpoint.StepID, err)
		s.FinishRunNotifications(run.RunID)
		return
	}
	s.logger.Info("run_continuation_started", String("run_id", run.RunID), String("checkpoint_id", checkpoint.CheckpointID), String("continuation_type", continuation.Type), String("source", source))
	if s.stopForHostedToolApproval(ctx, run, effective, reservation, continuation) || s.dispatchInitialRunContinuation(ctx, run, root, effective, reservation, continuation) {
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	s.generationStreams.register(runCtx, run.RunID, run.Actor, cancel)
	defer cancel()
	lifecycle := newRunSegmentLifecycle(s, runCtx, run.RunID, reservation)
	defer lifecycle.abort()
	if err := s.validateDurableRunToolResult(runCtx, run, continuation); err != nil {
		lifecycle.fail(run, effective, continuation.StepID, err)
		return
	}
	if continuation.Type == runContinuationExecuteApprovedTool {
		if err := s.executeApprovedRunTool(runCtx, run, effective, checkpoint, continuation); err != nil {
			lifecycle.fail(run, effective, continuation.StepID, err)
			return
		}
	}
	lifecycle.transfer()
	if effective.Strategy == TextRunStrategyDirect {
		s.executeDirectStrategy(runCtx, run, root, effective, reservation, nil, runUsage{})
		return
	}
	s.executePlan(runCtx, run, root, effective, reservation, nil, runUsage{})
}

func (s *Engine) stopForHostedToolApproval(ctx context.Context, run model.Run, effective effectiveTextRunConfig, reservation *UsageBalanceReservation, continuation runContinuation) bool {
	waiting, err := s.ensureHostedToolSetApproval(ctx, run, effective, continuation)
	if err != nil {
		_ = s.ReleaseRunUsageReservation(context.WithoutCancel(ctx), reservation, "Hosted Tool 授权创建失败退回预扣")
		s.failTextRun(context.WithoutCancel(ctx), run, continuation.StepID, err)
		return true
	}
	if waiting {
		_ = s.ReleaseRunUsageReservation(context.WithoutCancel(ctx), reservation, "等待 Hosted Tool 授权退回预扣")
	}
	return waiting
}

func (s *Engine) dispatchInitialRunContinuation(ctx context.Context, run model.Run, root model.Step, effective effectiveTextRunConfig, reservation *UsageBalanceReservation, continuation runContinuation) bool {
	switch continuation.Type {
	case runContinuationStartDirect:
		s.withRunGenerationLease(ctx, run, func(runCtx context.Context) {
			s.executeDirectStrategy(runCtx, run, root, effective, reservation, nil, runUsage{})
		})
		return true
	case runContinuationStartPlanning, runContinuationReplan:
		s.executePlanning(ctx, run, root, effective, reservation, continuation.NextRevision, continuation.Feedback)
		return true
	case runContinuationRenewInteraction:
		_ = s.ReleaseRunUsageReservation(context.WithoutCancel(ctx), reservation, "运行交互续期被错误调度退回预扣")
		s.failTextRun(context.WithoutCancel(ctx), run, continuation.StepID, ErrRunSnapshotIncompatible)
		s.FinishRunNotifications(run.RunID)
		return true
	case runContinuationAwaitHandoffJoin:
		_ = s.ReleaseRunUsageReservation(context.WithoutCancel(ctx), reservation, "Handoff Join 等待 checkpoint 被错误调度退回预扣")
		s.failTextRun(context.WithoutCancel(ctx), run, continuation.StepID, ErrRunSnapshotIncompatible)
		s.FinishRunNotifications(run.RunID)
		return true
	default:
		return false
	}
}

func (s *Engine) withRunGenerationLease(ctx context.Context, run model.Run, execute func(context.Context)) {
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	s.generationStreams.register(runCtx, run.RunID, run.Actor, cancel)
	defer cancel()
	execute(runCtx)
}

func (s *Engine) validateDurableRunToolResult(ctx context.Context, run model.Run, continuation runContinuation) error {
	expected := continuation.DurableToolResult
	if expected == nil {
		return nil
	}
	result, err := s.repo.GetRunToolResult(ctx, run.Actor, run.RunID, expected.ToolCallID)
	if err != nil {
		return err
	}
	if result == nil || result.EventType != expected.EventType || result.StepID != continuation.StepID {
		return ErrRunSnapshotIncompatible
	}
	return nil
}

func (s *Engine) executeApprovedRunTool(ctx context.Context, run model.Run, effective effectiveTextRunConfig, checkpoint model.Checkpoint, continuation runContinuation) error {
	request := continuation.FrozenToolCall
	tools, err := s.resolveRunTools(ctx, run.Actor, effective)
	if err != nil {
		return err
	}
	tool, ok := tools[request.ToolName]
	if !ok || !resolvedToolMatchesFrozen(tool, request) {
		return ErrRunSnapshotIncompatible
	}
	existing, resultErr := s.repo.GetRunToolResult(ctx, run.Actor, run.RunID, request.ToolCallID)
	if resultErr == nil && existing != nil {
		if !committedToolResultMatchesFrozen(existing, request) {
			return ErrRunSnapshotIncompatible
		}
		s.logger.Info("run_continuation_tool_result_reused", String("run_id", run.RunID), String("checkpoint_id", checkpoint.CheckpointID), String("tool_call_id", request.ToolCallID))
		return nil
	}
	if !errors.Is(resultErr, ErrNotFound) {
		return resultErr
	}
	if err = s.ensureRunCallBudgetWithReserve(ctx, run, effective, false, 0); err != nil {
		return err
	}
	_, _, err = s.executeFrozenRunTool(ctx, run, continuation.StepID, effective, tool, ToolCall{ToolCallID: request.ToolCallID, ToolName: request.ToolName, ArgumentsJSON: string(request.Arguments)})
	return err
}

func resolvedToolMatchesFrozen(tool ResolvedTool, request *runFrozenToolCall) bool {
	return tool.ToolKey == request.ToolKey && tool.OriginalName == request.OriginalName
}

func committedToolResultMatchesFrozen(event *model.Event, request *runFrozenToolCall) bool {
	terminal := event.EventType == valueToolCompleted8D0A12FD || event.EventType == valueToolFailedFB145984
	return event.ToolName == request.ToolName && terminal
}

func (s *Engine) ResumeTextRun(ctx context.Context, input ResumeTextRunInput) (*model.Checkpoint, bool, error) {
	if validResumeTextRunInput(input) {
		run, runErr := s.repo.GetRun(ctx, input.Actor, input.RunID)
		if runErr != nil {
			return nil, false, runErr
		}
		if run.RuntimeKind == model.RuntimeKindWorkflow {
			return s.resumeWorkflowRun(ctx, input, *run)
		}
	}
	prepared, err := s.prepareTextRunResume(ctx, input)
	if err != nil {
		return nil, false, err
	}
	if prepared.continuation.Type == runContinuationRenewInteraction {
		return s.renewExpiredRunInteraction(ctx, input, prepared.run, prepared.effective, prepared.checkpoint, prepared.continuation, prepared.fingerprint)
	}
	_, resumeStepIDs, err := s.prepareExplicitResumeSteps(ctx, prepared.run, prepared.reused)
	if err != nil {
		return nil, false, err
	}
	var reservation *UsageBalanceReservation
	if !prepared.reused {
		reservation, _, err = s.ReserveRunUsageBalance(ctx, RunBillingInput{Actor: prepared.run.Actor, Thread: prepared.run.Thread, PlatformModelName: prepared.effective.PlatformModelName, ClientRunID: prepared.continuation.SegmentKey})
		if err != nil {
			return nil, false, err
		}
	}
	return s.applyExplicitTextRunResume(ctx, input, prepared, resumeStepIDs, reservation)
}

func (s *Engine) prepareTextRunResume(ctx context.Context, input ResumeTextRunInput) (preparedTextRunResume, error) {
	if !validResumeTextRunInput(input) {
		return preparedTextRunResume{}, ErrInvalidInput
	}
	run, err := s.repo.GetRun(ctx, input.Actor, input.RunID)
	if err != nil {
		return preparedTextRunResume{}, err
	}
	effective, err := s.loadResumeTextRunRuntime(ctx, *run)
	if err != nil {
		return preparedTextRunResume{}, ErrRunSnapshotIncompatible
	}
	checkpoint, reused, err := s.selectRunResumeCheckpoint(ctx, input)
	if err != nil {
		return preparedTextRunResume{}, err
	}
	continuation, err := decodeRunContinuation(checkpoint)
	if err != nil {
		return preparedTextRunResume{}, ErrRunSnapshotIncompatible
	}
	if err = validateExplicitResumeContinuation(continuation); err != nil {
		return preparedTextRunResume{}, err
	}
	requestedID := strings.TrimSpace(input.CheckpointID)
	if requestedID != "" && checkpoint.CheckpointID != requestedID {
		if reused {
			return preparedTextRunResume{}, ErrRunResumeIDConflict
		}
		return preparedTextRunResume{}, ErrRunResumeConflict
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(run.RunID+"\x00"+checkpoint.CheckpointID)))
	return preparedTextRunResume{run: *run, effective: effective, checkpoint: checkpoint, continuation: continuation, reused: reused, fingerprint: fingerprint}, nil
}

func validResumeTextRunInput(input ResumeTextRunInput) bool {
	return validActorRef(input.Actor) && strings.TrimSpace(input.RunID) != "" && strings.TrimSpace(input.ClientResumeID) != "" && len(input.ClientResumeID) <= 64
}

func (s *Engine) loadResumeTextRunRuntime(ctx context.Context, run model.Run) (effectiveTextRunConfig, error) {
	var effective effectiveTextRunConfig
	if json.Unmarshal([]byte(run.RunConfigSnapshotJSON), &effective) != nil || effective.SemanticVersion != RuntimeSnapshotVersion {
		return effectiveTextRunConfig{}, ErrRunSnapshotIncompatible
	}
	if _, err := s.loadTextRunContextMessages(ctx, run); err != nil {
		return effectiveTextRunConfig{}, ErrRunSnapshotIncompatible
	}
	if _, err := s.resolveRunTools(ctx, run.Actor, effective); err != nil {
		return effectiveTextRunConfig{}, ErrRunSnapshotIncompatible
	}
	return effective, nil
}

func (s *Engine) selectRunResumeCheckpoint(ctx context.Context, input ResumeTextRunInput) (model.Checkpoint, bool, error) {
	checkpoints, err := s.repo.ListRunCheckpoints(ctx, input.Actor, input.RunID)
	if err != nil {
		return model.Checkpoint{}, false, err
	}
	for _, checkpoint := range checkpoints {
		if checkpoint.ResumeRequestID == input.ClientResumeID {
			return checkpoint, true, nil
		}
	}
	for _, checkpoint := range checkpoints {
		if checkpoint.Status == model.CheckpointReady {
			return checkpoint, false, nil
		}
	}
	return model.Checkpoint{}, false, ErrRunResumeConflict
}

func (s *Engine) prepareExplicitResumeSteps(ctx context.Context, run model.Run, reused bool) (model.Step, []string, error) {
	if reused {
		return model.Step{}, nil, nil
	}
	root, err := s.runRootStep(ctx, run.RunID)
	if err != nil {
		return model.Step{}, nil, ErrRunResumeConflict
	}
	steps, err := s.repo.ListRunSteps(ctx, run.RunID)
	if err != nil {
		return model.Step{}, nil, err
	}
	resumeStepIDs := suspendedResumeStepIDs(steps, run.CurrentStepID, root.StepID)
	if len(resumeStepIDs) == 0 {
		return model.Step{}, nil, ErrRunResumeConflict
	}
	return root, resumeStepIDs, nil
}

func suspendedResumeStepIDs(steps []model.Step, currentStepID, rootStepID string) []string {
	result := make([]string, 0, 2)
	seen := map[string]struct{}{}
	for _, step := range steps {
		if step.Status != model.RunStatusSuspended || step.StepID != currentStepID && step.StepID != rootStepID {
			continue
		}
		if _, exists := seen[step.StepID]; !exists {
			seen[step.StepID] = struct{}{}
			result = append(result, step.StepID)
		}
	}
	return result
}

func (s *Engine) applyExplicitTextRunResume(ctx context.Context, input ResumeTextRunInput, prepared preparedTextRunResume, resumeStepIDs []string, reservation *UsageBalanceReservation) (*model.Checkpoint, bool, error) {
	run, continuation, selectedCheckpoint := prepared.run, prepared.continuation, prepared.checkpoint
	nextStatus := continuation.TargetStatus
	successor := newRunContinuationCheckpoint(run, selectedCheckpoint.StepID, "resume_execution", continuation)
	successor.CheckpointID = deterministicRunCheckpointID(run.RunID, selectedCheckpoint.CheckpointID, input.ClientResumeID, "resume_execution")
	successor.ParentCheckpointID = selectedCheckpoint.CheckpointID
	events := runExplicitResumeEvents(run, selectedCheckpoint, *successor, nextStatus, resumeStepIDs, continuation.Type)
	var checkpoint, continuationCheckpoint *model.Checkpoint
	var saved []model.Event
	var applied bool
	work := func(txCtx context.Context) error {
		var resumeErr error
		checkpoint, continuationCheckpoint, saved, applied, resumeErr = s.repo.ResumeRun(txCtx, input.Actor, input.RunID, selectedCheckpoint.CheckpointID, input.ClientResumeID, prepared.fingerprint, nextStatus, successor, events)
		if resumeErr != nil || !applied {
			return resumeErr
		}
		if continuationCheckpoint == nil {
			return ErrRunSnapshotIncompatible
		}
		run.Status, run.PendingInteractionID = nextStatus, ""
		resumeErr = s.createContinuationJob(txCtx, run, *continuationCheckpoint, "explicit_resume", reservation)
		return resumeErr
	}
	var err error
	if s.unitOfWork == nil {
		err = ErrHostProjectionUnavailable
	} else {
		err = s.unitOfWork.Within(ctx, work)
	}
	if err != nil {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "运行显式恢复失败退回预扣")
		return nil, false, err
	}
	if !applied {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "运行显式恢复幂等复用退回预扣")
		return checkpoint, true, nil
	}
	s.publishRunEvents(run.RunID, saved)
	s.wakeContinuationJobs()
	return checkpoint, false, nil
}

func (s *Engine) renewExpiredRunInteraction(ctx context.Context, input ResumeTextRunInput, run model.Run, effective effectiveTextRunConfig, selected model.Checkpoint, continuation runContinuation, fingerprint string) (*model.Checkpoint, bool, error) {
	frozen := continuation.FrozenInteraction
	if frozen == nil {
		return nil, false, ErrRunSnapshotIncompatible
	}
	expired, err := s.repo.GetRunInteraction(ctx, input.Actor, input.RunID, frozen.InteractionID)
	if err != nil || !expiredInteractionMatchesFrozen(expired, frozen) {
		return nil, false, ErrRunSnapshotIncompatible
	}
	renewed, successor, events, err := buildRenewedRunInteraction(input, run, effective, selected, frozen)
	if err != nil {
		return nil, false, err
	}
	checkpoint, _, _, saved, applied, err := s.repo.RenewExpiredRunInteraction(ctx, input.Actor, input.RunID, frozen.InteractionID, selected.CheckpointID, input.ClientResumeID, fingerprint, renewed, successor, events)
	if err != nil {
		return nil, false, err
	}
	if applied {
		s.publishRunEvents(run.RunID, saved)
	}
	return checkpoint, !applied, nil
}

func expiredInteractionMatchesFrozen(expired *model.Interaction, frozen *runFrozenInteraction) bool {
	return expired.Status == model.InteractionExpired && expired.Type == frozen.Type && expired.StepID == frozen.StepID && expired.ToolCallID == frozen.ToolCallID && canonicalRunJSON(json.RawMessage(expired.RequestPayloadJSON)) == canonicalRunJSON(frozen.Request) && canonicalRunJSON(json.RawMessage(expired.ResponseSchemaJSON)) == canonicalRunJSON(frozen.ResponseSchema)
}

func buildRenewedRunInteraction(input ResumeTextRunInput, run model.Run, effective effectiveTextRunConfig, selected model.Checkpoint, frozen *runFrozenInteraction) (*model.Interaction, *model.Checkpoint, []model.Event, error) {
	now := time.Now()
	expiresAt := now.Add(time.Duration(effective.InteractionTTLHours) * time.Hour)
	renewedID := deterministicRunInteractionID(run.RunID, frozen.InteractionID, input.ClientResumeID, "renewed")
	renewed := &model.Interaction{InteractionID: renewedID, RunID: run.RunID, StepID: frozen.StepID, ToolCallID: frozen.ToolCallID, Type: frozen.Type, Status: model.InteractionPending, RequestPayloadJSON: string(frozen.Request), ResponseSchemaJSON: string(frozen.ResponseSchema), RequestedAt: now, ExpiresAt: &expiresAt}
	successor, err := newRunInteractionCheckpoint(run, renewed, "interaction_renewed")
	if err != nil {
		return nil, nil, nil, err
	}
	successor.CheckpointID = deterministicRunCheckpointID(run.RunID, selected.CheckpointID, input.ClientResumeID, "interaction_renewed")
	successor.ParentCheckpointID = selected.CheckpointID
	var request interface{}
	if err = json.Unmarshal(frozen.Request, &request); err != nil {
		return nil, nil, nil, ErrRunSnapshotIncompatible
	}
	events := []model.Event{
		newRunEvent(run, "interaction.created", frozen.StepID, "Expired interaction reopened", map[string]interface{}{valueInteractionIDA8491B1B: renewed.InteractionID, "renewedFromInteractionID": frozen.InteractionID, valueType5EE8C955: frozen.Type, valueRequest91B6AFF3: request, "expiresAt": expiresAt}, nil),
		newRunEvent(run, "checkpoint.created", frozen.StepID, "Renewed interaction checkpoint", map[string]interface{}{valueCheckpointID9CD08C70: successor.CheckpointID, valueKindE5B2EFB3: successor.Kind, valueContinuationTypeDCB4DE9C: runContinuationRenewInteraction}, nil),
		newRunEvent(run, "step.waiting_input", frozen.StepID, "Waiting for renewed interaction", map[string]interface{}{valueInteractionIDA8491B1B: renewed.InteractionID}, nil),
	}
	resumed := newRunEvent(run, "run.resumed", frozen.StepID, "Expired interaction reopened", map[string]interface{}{valueCheckpointID9CD08C70: selected.CheckpointID, "executionCheckpointID": successor.CheckpointID, valueInteractionIDA8491B1B: renewed.InteractionID, valueStatus327C4193: model.RunStatusWaitingInput}, nil)
	resumed.Status = model.RunStatusWaitingInput
	events = append(events, resumed)
	return renewed, successor, events, nil
}

func deterministicRunInteractionID(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "interaction_" + fmt.Sprintf("%x", digest[:16])
}

func runExplicitResumeEvents(run model.Run, checkpoint, successor model.Checkpoint, nextStatus string, stepIDs []string, continuationType string) []model.Event {
	events := []model.Event{newRunEvent(run, "checkpoint.created", successor.StepID, "Resume execution checkpoint", map[string]interface{}{valueCheckpointID9CD08C70: successor.CheckpointID, valueKindE5B2EFB3: successor.Kind, valueContinuationTypeDCB4DE9C: continuationType}, nil)}
	for _, stepID := range stepIDs {
		events = append(events, newRunEvent(run, valueStepResumedF8C2AD47, stepID, "Step resumed", map[string]interface{}{valueCheckpointID9CD08C70: checkpoint.CheckpointID}, nil))
	}
	resumed := newRunEvent(run, "run.resumed", successor.StepID, "Text run resumed", map[string]interface{}{valueCheckpointID9CD08C70: checkpoint.CheckpointID, "executionCheckpointID": successor.CheckpointID}, nil)
	resumed.Status = nextStatus
	events = append(events, resumed)
	return events
}

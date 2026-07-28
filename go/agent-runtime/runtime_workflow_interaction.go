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

func (s *Engine) resolveWorkflowRunInteraction(ctx context.Context, initialRun model.Run, input ResolveRunInteractionInput) (*model.Interaction, error) {
	responseValue, responseJSON, fingerprint, err := normalizeWorkflowInteractionResponse(input.Response)
	if err != nil {
		return nil, ErrRunInteractionResponseInvalid
	}
	for attempt := 0; attempt < 5; attempt++ {
		run, loadErr := s.workflowRunForAttempt(ctx, initialRun, input.Actor, input.RunID, attempt)
		if loadErr != nil {
			return nil, loadErr
		}
		resolved, retry, resolveErr := s.resolveWorkflowInteractionAttempt(
			ctx,
			run,
			input,
			responseValue,
			responseJSON,
			fingerprint,
		)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if retry {
			continue
		}
		return resolved, nil
	}
	return nil, ErrWorkflowVersionConflict
}

func (s *Engine) resolveWorkflowInteractionAttempt(
	ctx context.Context,
	run model.Run,
	input ResolveRunInteractionInput,
	responseValue interface{},
	responseJSON string,
	fingerprint string,
) (*model.Interaction, bool, error) {
	execution, interaction, err := s.loadWorkflowInteractionResolution(ctx, input, run.RunID)
	if err != nil {
		return nil, false, err
	}
	if resolved, terminal, gateErr := workflowInteractionResolutionGate(run, interaction, input, fingerprint, s.now()); terminal {
		return resolved, false, gateErr
	}
	if err = validateWorkflowJSON(json.RawMessage(interaction.ResponseSchemaJSON), responseValue); err != nil {
		return nil, false, ErrRunInteractionResponseInvalid
	}
	transition, resolved := s.buildWorkflowInteractionResolutionTransition(
		ctx,
		run,
		*execution,
		*interaction,
		input,
		responseJSON,
		fingerprint,
	)
	_, saved, applied, err := s.repo.ApplyWorkflowTransition(ctx, input.Actor, run.RunID, transition)
	if errors.Is(err, ErrWorkflowVersionConflict) || err == nil && !applied {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	s.publishRunEvents(run.RunID, saved)
	s.wakeContinuationJobs()
	return &resolved, false, nil
}

func (s *Engine) loadWorkflowInteractionResolution(
	ctx context.Context,
	input ResolveRunInteractionInput,
	runID string,
) (*model.WorkflowExecution, *model.Interaction, error) {
	execution, err := s.repo.GetWorkflowExecution(ctx, input.Actor, runID)
	if err != nil {
		return nil, nil, err
	}
	interaction, err := s.repo.GetRunInteraction(ctx, input.Actor, runID, input.InteractionID)
	if err != nil {
		return nil, nil, err
	}
	return execution, interaction, nil
}

func workflowInteractionResolutionGate(
	run model.Run,
	interaction *model.Interaction,
	input ResolveRunInteractionInput,
	fingerprint string,
	now time.Time,
) (*model.Interaction, bool, error) {
	if run.EndedAt != nil || run.Status == model.RunStatusSuspended {
		return nil, true, ErrRunInteractionConflict
	}
	if interaction.Status == model.InteractionResolved {
		resolved, _, err := replayWorkflowInteractionResolution(interaction, input, fingerprint)
		return resolved, true, err
	}
	if !workflowInteractionCanResolve(*interaction, now) {
		return nil, true, ErrRunInteractionConflict
	}
	return nil, false, nil
}

func replayWorkflowInteractionResolution(
	interaction *model.Interaction,
	input ResolveRunInteractionInput,
	fingerprint string,
) (*model.Interaction, bool, error) {
	if interaction.ResolveRequestID == input.ClientResolveID && interaction.ResumeFingerprint == fingerprint {
		return interaction, false, nil
	}
	return nil, false, ErrRunInteractionConflict
}

func workflowInteractionCanResolve(interaction model.Interaction, now time.Time) bool {
	return interaction.Status == model.InteractionPending &&
		(interaction.ExpiresAt == nil || interaction.ExpiresAt.After(now))
}

func (s *Engine) buildWorkflowInteractionResolutionTransition(
	ctx context.Context,
	run model.Run,
	execution model.WorkflowExecution,
	interaction model.Interaction,
	input ResolveRunInteractionInput,
	responseJSON string,
	fingerprint string,
) (model.WorkflowTransition, model.Interaction) {
	now := s.now()
	resolved := interaction
	resolved.Status, resolved.ResponseJSON = model.InteractionResolved, responseJSON
	resolved.ResolveRequestID, resolved.ResumeFingerprint = input.ClientResolveID, fingerprint
	resolved.ResolvedAt, resolved.ResolvedBy, resolved.UpdatedAt = &now, input.Actor, now
	nextRun := run
	nextRun.Status, nextRun.StatusReason = model.RunStatusRunning, "Workflow interaction resolved"
	nextRun.PendingInteractionID = s.nextPendingWorkflowInteraction(ctx, input.Actor, run.RunID, input.InteractionID)
	nextExecution := execution
	nextExecution.Version++
	nextExecution.Status = model.WorkflowExecutionRunning
	continuation := runContinuation{
		SemanticVersion: RuntimeSnapshotVersion,
		SegmentKey:      fmt.Sprintf("%s:workflow:%d", run.RunID, nextExecution.Version),
		Type:            runContinuationWorkflowExecute,
		TargetStatus:    model.RunStatusRunning,
		StepID:          interaction.StepID,
	}
	checkpoint := newRunContinuationCheckpoint(run, interaction.StepID, "workflow_interaction_resolved", continuation)
	checkpoint.CheckpointID = deterministicRunCheckpointID(run.RunID, interaction.InteractionID, input.ClientResolveID, "workflow")
	job := s.newWorkflowContinuationJob(ctx, run, *checkpoint, "workflow_interaction", now)
	events := []model.Event{
		newRunEvent(run, "interaction.resolved", interaction.StepID, "Workflow interaction resolved", map[string]interface{}{workflowPayloadInteractionID: interaction.InteractionID, workflowPayloadType: interaction.Type}, nil),
		newRunEvent(run, "workflow.wait.resolved", interaction.StepID, "Workflow wait resolved", map[string]interface{}{workflowPayloadInteractionID: interaction.InteractionID}, nil),
		newRunEvent(run, "checkpoint.created", interaction.StepID, "Workflow interaction continuation", map[string]interface{}{workflowPayloadCheckpointID: checkpoint.CheckpointID}, nil),
	}
	return model.WorkflowTransition{
		ExpectedVersion: execution.Version,
		Execution:       nextExecution,
		Run:             nextRun,
		Interactions:    []model.Interaction{resolved},
		Checkpoints:     []model.Checkpoint{*checkpoint},
		ContinuationJobs: []model.ContinuationJob{
			*job,
		},
		Events: events,
	}, resolved
}

func (s *Engine) expireWorkflowInteractionIfNeeded(ctx context.Context, item model.ExpiredInteraction) ([]model.Event, bool, bool, error) {
	run, err := s.repo.GetRun(ctx, item.Actor, item.RunID)
	if err != nil {
		return nil, false, false, err
	}
	if run.RuntimeKind != model.RuntimeKindWorkflow {
		return nil, false, false, nil
	}
	events, applied, err := s.expireWorkflowRunInteraction(ctx, *run, item.InteractionID)
	return events, applied, true, err
}

func (s *Engine) expireWorkflowRunInteraction(ctx context.Context, initialRun model.Run, interactionID string) ([]model.Event, bool, error) {
	for attempt := 0; attempt < 5; attempt++ {
		run, err := s.workflowRunForAttempt(ctx, initialRun, initialRun.Actor, initialRun.RunID, attempt)
		if err != nil {
			return nil, false, err
		}
		events, applied, retry, expireErr := s.expireWorkflowInteractionAttempt(ctx, run, interactionID)
		if expireErr != nil {
			return nil, false, expireErr
		}
		if retry {
			continue
		}
		return events, applied, nil
	}
	return nil, false, ErrWorkflowVersionConflict
}

func (s *Engine) expireWorkflowInteractionAttempt(
	ctx context.Context,
	run model.Run,
	interactionID string,
) ([]model.Event, bool, bool, error) {
	interaction, err := s.repo.GetRunInteraction(ctx, run.Actor, run.RunID, interactionID)
	if err != nil {
		return nil, false, false, err
	}
	if interaction.Status == model.InteractionExpired {
		return nil, false, false, nil
	}
	if interaction.Status != model.InteractionPending {
		return nil, false, false, ErrRunInteractionConflict
	}
	if run.EndedAt != nil {
		events, applied, expireErr := s.repo.ExpireRunInteraction(ctx, interactionID)
		return events, applied, false, expireErr
	}
	execution, err := s.repo.GetWorkflowExecution(ctx, run.Actor, run.RunID)
	if err != nil {
		return nil, false, false, err
	}
	transition := s.buildWorkflowInteractionExpiryTransition(ctx, run, *execution, *interaction)
	_, saved, applied, err := s.repo.ApplyWorkflowTransition(ctx, run.Actor, run.RunID, transition)
	if errors.Is(err, ErrWorkflowVersionConflict) || err == nil && !applied {
		return nil, false, true, nil
	}
	if err != nil {
		return nil, false, false, err
	}
	s.wakeContinuationJobs()
	return saved, true, false, nil
}

func (s *Engine) buildWorkflowInteractionExpiryTransition(
	ctx context.Context,
	run model.Run,
	execution model.WorkflowExecution,
	interaction model.Interaction,
) model.WorkflowTransition {
	now := s.now()
	expired := interaction
	expired.Status, expired.ResolvedAt, expired.UpdatedAt = model.InteractionExpired, &now, now
	nextRun := run
	nextRun.Status, nextRun.StatusReason = model.RunStatusRunning, "Workflow interaction expired"
	nextRun.PendingInteractionID = s.nextPendingWorkflowInteraction(ctx, run.Actor, run.RunID, interaction.InteractionID)
	nextExecution := execution
	nextExecution.Version++
	nextExecution.Status = model.WorkflowExecutionRunning
	continuation := runContinuation{
		SemanticVersion: RuntimeSnapshotVersion,
		SegmentKey:      fmt.Sprintf("%s:workflow:%d", run.RunID, nextExecution.Version),
		Type:            runContinuationWorkflowExecute,
		TargetStatus:    model.RunStatusRunning,
		StepID:          interaction.StepID,
	}
	checkpoint := newRunContinuationCheckpoint(run, interaction.StepID, "workflow_interaction_expired", continuation)
	checkpoint.CheckpointID = deterministicRunCheckpointID(run.RunID, interaction.InteractionID, "expired", "workflow")
	job := s.newWorkflowContinuationJob(ctx, run, *checkpoint, "workflow_interaction_expired", now)
	events := []model.Event{
		newRunEvent(run, "interaction.expired", interaction.StepID, "Workflow interaction expired", map[string]interface{}{workflowPayloadInteractionID: interaction.InteractionID, workflowPayloadType: interaction.Type}, nil),
		newRunEvent(run, "workflow.wait.expired", interaction.StepID, "Workflow wait expired", map[string]interface{}{workflowPayloadInteractionID: interaction.InteractionID}, nil),
		newRunEvent(run, "checkpoint.created", interaction.StepID, "Workflow interaction expiry continuation", map[string]interface{}{workflowPayloadCheckpointID: checkpoint.CheckpointID}, nil),
	}
	return model.WorkflowTransition{
		ExpectedVersion: execution.Version,
		Execution:       nextExecution,
		Run:             nextRun,
		Interactions:    []model.Interaction{expired},
		Checkpoints:     []model.Checkpoint{*checkpoint},
		ContinuationJobs: []model.ContinuationJob{
			*job,
		},
		Events: events,
	}
}

func (s *Engine) workflowRunForAttempt(
	ctx context.Context,
	initial model.Run,
	actor model.ActorRef,
	runID string,
	attempt int,
) (model.Run, error) {
	if attempt == 0 {
		return initial, nil
	}
	loaded, err := s.repo.GetRun(ctx, actor, runID)
	if err != nil {
		return model.Run{}, err
	}
	return *loaded, nil
}

func normalizeWorkflowInteractionResponse(input interface{}) (interface{}, string, string, error) {
	raw, err := canonicalWorkflowJSON(input)
	if err != nil {
		return nil, "", "", err
	}
	value, err := decodeWorkflowJSON(raw)
	if err != nil {
		return nil, "", "", err
	}
	fingerprint, err := hashWorkflowValue(value)
	if err != nil {
		return nil, "", "", err
	}
	return value, string(raw), fingerprint, nil
}

func (s *Engine) nextPendingWorkflowInteraction(ctx context.Context, actor model.ActorRef, runID, excluding string) string {
	items, err := s.repo.ListRunInteractions(ctx, actor, runID)
	if err != nil {
		return ""
	}
	ids := make([]string, 0)
	for _, item := range items {
		if item.InteractionID != excluding && item.Status == model.InteractionPending {
			ids = append(ids, item.InteractionID)
		}
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return ""
	}
	return strings.TrimSpace(ids[0])
}

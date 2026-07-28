package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func (s *Engine) resumeWorkflowRun(ctx context.Context, input ResumeTextRunInput, initial model.Run) (*model.Checkpoint, bool, error) {
	for attempt := 0; attempt < 5; attempt++ {
		run, err := s.workflowRunForAttempt(ctx, initial, input.Actor, input.RunID, attempt)
		if err != nil {
			return nil, false, err
		}
		checkpoint, reused, retry, resumeErr := s.resumeWorkflowRunAttempt(ctx, input, run)
		if resumeErr != nil {
			return nil, false, resumeErr
		}
		if retry {
			continue
		}
		return checkpoint, reused, nil
	}
	return nil, false, ErrWorkflowVersionConflict
}

func (s *Engine) resumeWorkflowRunAttempt(
	ctx context.Context,
	input ResumeTextRunInput,
	run model.Run,
) (*model.Checkpoint, bool, bool, error) {
	prepared, err := s.prepareWorkflowResumeAttempt(ctx, input, run)
	if err != nil {
		return nil, false, false, err
	}
	if prepared.reused {
		return prepared.replay, true, false, nil
	}
	_, events, applied, err := s.repo.ApplyWorkflowTransition(ctx, input.Actor, input.RunID, prepared.resume.transition)
	if errors.Is(err, ErrWorkflowVersionConflict) || err == nil && !applied {
		return nil, false, true, nil
	}
	if err != nil {
		return nil, false, false, err
	}
	s.publishRunEvents(run.RunID, events)
	s.wakeContinuationJobs()
	return &prepared.resume.successor, false, false, nil
}

type workflowResumeAttemptPreparation struct {
	resume workflowResumePreparation
	replay *model.Checkpoint
	reused bool
}

func (s *Engine) prepareWorkflowResumeAttempt(
	ctx context.Context,
	input ResumeTextRunInput,
	run model.Run,
) (workflowResumeAttemptPreparation, error) {
	checkpoints, err := s.repo.ListRunCheckpoints(ctx, input.Actor, input.RunID)
	if err != nil {
		return workflowResumeAttemptPreparation{}, err
	}
	if replay, reused, replayErr := replayWorkflowResume(checkpoints, input); reused || replayErr != nil {
		return workflowResumeAttemptPreparation{replay: replay, reused: reused}, replayErr
	}
	if run.Status != model.RunStatusSuspended || run.EndedAt != nil {
		return workflowResumeAttemptPreparation{}, ErrRunResumeConflict
	}
	current, err := selectWorkflowResumeCheckpoint(checkpoints, input.CheckpointID)
	if err != nil {
		return workflowResumeAttemptPreparation{}, err
	}
	prepared, err := s.prepareWorkflowResume(ctx, input, run, current)
	if err != nil {
		return workflowResumeAttemptPreparation{}, err
	}
	return workflowResumeAttemptPreparation{resume: prepared}, nil
}

type workflowResumePreparation struct {
	transition model.WorkflowTransition
	successor  model.Checkpoint
}

func (s *Engine) prepareWorkflowResume(
	ctx context.Context,
	input ResumeTextRunInput,
	run model.Run,
	current model.Checkpoint,
) (workflowResumePreparation, error) {
	execution, err := s.repo.GetWorkflowExecution(ctx, input.Actor, input.RunID)
	if err != nil {
		return workflowResumePreparation{}, err
	}
	state, budget, err := decodeWorkflowExecutionState(*execution)
	if err != nil {
		return workflowResumePreparation{}, err
	}
	step, err := resetFailedWorkflowCompensation(&state, input.RunID, s.now())
	if err != nil {
		return workflowResumePreparation{}, err
	}
	steps, err := s.repo.ListRunSteps(ctx, input.RunID)
	if err != nil {
		return workflowResumePreparation{}, err
	}
	step, err = workflowCompensationResumeStep(steps, step)
	if err != nil {
		return workflowResumePreparation{}, err
	}
	stateJSON, varsJSON, waitsJSON, compensationJSON, budgetJSON, err := encodeWorkflowExecutionState(state, budget)
	if err != nil {
		return workflowResumePreparation{}, err
	}
	fingerprint, err := workflowResumeFingerprint(input, current.CheckpointID)
	if err != nil {
		return workflowResumePreparation{}, err
	}
	nextExecution := workflowResumeExecution(
		*execution,
		stateJSON,
		varsJSON,
		waitsJSON,
		compensationJSON,
		budgetJSON,
	)
	nextRun := workflowResumeRun(run)
	current.Status, current.ResumeRequestID, current.ResumeFingerprint, current.UpdatedAt = model.CheckpointConsumed, input.ClientResumeID, fingerprint, s.now()
	successor, job := s.workflowResumeContinuation(ctx, input, run, nextRun, nextExecution, current, step)
	transition := workflowResumeTransition(input, run, *execution, nextExecution, nextRun, current, successor, *job, step)
	return workflowResumePreparation{transition: transition, successor: successor}, nil
}

func workflowResumeFingerprint(input ResumeTextRunInput, checkpointID string) (string, error) {
	return hashWorkflowValue(struct{ RunID, ClientResumeID, CheckpointID string }{
		input.RunID,
		input.ClientResumeID,
		checkpointID,
	})
}

func workflowResumeExecution(
	execution model.WorkflowExecution,
	stateJSON string,
	varsJSON string,
	waitsJSON string,
	compensationJSON string,
	budgetJSON string,
) model.WorkflowExecution {
	execution.Version++
	execution.Status = model.WorkflowExecutionCompensating
	execution.StateJSON, execution.VarsJSON, execution.WaitsJSON = stateJSON, varsJSON, waitsJSON
	execution.CompensationJSON, execution.BudgetJSON = compensationJSON, budgetJSON
	execution.ErrorCode, execution.ErrorMessage, execution.EndedAt = "", "", nil
	return execution
}

func workflowResumeRun(run model.Run) model.Run {
	run.Status, run.StatusReason = model.RunStatusCompensating, "Retrying failed workflow compensation"
	run.ErrorCode, run.ErrorMessage, run.EndedAt = "", "", nil
	return run
}

func (s *Engine) workflowResumeContinuation(
	ctx context.Context,
	input ResumeTextRunInput,
	run model.Run,
	nextRun model.Run,
	nextExecution model.WorkflowExecution,
	current model.Checkpoint,
	step model.Step,
) (model.Checkpoint, *model.ContinuationJob) {
	continuation := runContinuation{
		SemanticVersion: RuntimeSnapshotVersion,
		SegmentKey:      fmt.Sprintf("%s:workflow:%d", run.RunID, nextExecution.Version),
		Type:            runContinuationWorkflowExecute,
		TargetStatus:    model.RunStatusCompensating,
		StepID:          step.StepID,
	}
	successor := newRunContinuationCheckpoint(run, step.StepID, "workflow_compensation_resume", continuation)
	successor.CheckpointID = deterministicRunCheckpointID(run.RunID, "workflow_compensation_resume", input.ClientResumeID)
	successor.ParentCheckpointID = current.CheckpointID
	job := s.newWorkflowContinuationJob(ctx, nextRun, *successor, "workflow_compensation_resume", s.now())
	return *successor, job
}

func workflowResumeTransition(
	input ResumeTextRunInput,
	run model.Run,
	execution model.WorkflowExecution,
	nextExecution model.WorkflowExecution,
	nextRun model.Run,
	current model.Checkpoint,
	successor model.Checkpoint,
	job model.ContinuationJob,
	step model.Step,
) model.WorkflowTransition {
	return model.WorkflowTransition{
		ExpectedVersion: execution.Version,
		Execution:       nextExecution,
		Run:             nextRun,
		Steps:           []model.Step{step},
		Checkpoints:     []model.Checkpoint{current, successor},
		ContinuationJobs: []model.ContinuationJob{
			job,
		},
		Events: []model.Event{
			newRunEvent(run, "workflow.compensation.resumed", step.StepID, "Retrying failed workflow compensation", map[string]interface{}{"clientResumeID": input.ClientResumeID}, nil),
			newRunEvent(run, "step.resumed", step.StepID, step.NodeID, map[string]interface{}{workflowPayloadAttempt: step.Attempt}, nil),
			newRunEvent(run, "run.compensating", step.StepID, "Retrying failed workflow compensation", map[string]interface{}{workflowPayloadRuntimeKind: model.RuntimeKindWorkflow}, nil),
			newRunEvent(run, "checkpoint.created", step.StepID, "Workflow compensation resume checkpoint", map[string]interface{}{workflowPayloadCheckpointID: successor.CheckpointID}, nil),
		},
	}
}

func replayWorkflowResume(checkpoints []model.Checkpoint, input ResumeTextRunInput) (*model.Checkpoint, bool, error) {
	for _, checkpoint := range checkpoints {
		if checkpoint.ResumeRequestID != input.ClientResumeID {
			continue
		}
		fingerprint, err := hashWorkflowValue(struct{ RunID, ClientResumeID, CheckpointID string }{input.RunID, input.ClientResumeID, checkpoint.CheckpointID})
		if err != nil {
			return nil, false, err
		}
		if checkpoint.ResumeFingerprint != fingerprint {
			return nil, false, ErrRunResumeIDConflict
		}
		for index := range checkpoints {
			if checkpoints[index].ParentCheckpointID == checkpoint.CheckpointID && checkpoints[index].Kind == "workflow_compensation_resume" {
				item := checkpoints[index]
				return &item, true, nil
			}
		}
		return &checkpoint, true, nil
	}
	return nil, false, nil
}

func workflowCompensationResumeStep(steps []model.Step, reset model.Step) (model.Step, error) {
	for _, step := range steps {
		if step.StepID != reset.StepID {
			continue
		}
		step.Status, step.Attempt = model.WorkflowStepStatusRunning, reset.Attempt
		step.WaitingKind, step.WaitingID, step.ChildRunID = "", "", ""
		step.OutputJSON, step.ErrorJSON, step.ResultSummary = "", "", ""
		step.EndedAt = nil
		return step, nil
	}
	return model.Step{}, ErrRunResumeConflict
}

func selectWorkflowResumeCheckpoint(checkpoints []model.Checkpoint, checkpointID string) (model.Checkpoint, error) {
	checkpointID = strings.TrimSpace(checkpointID)
	for index := len(checkpoints) - 1; index >= 0; index-- {
		item := checkpoints[index]
		if checkpointID != "" && item.CheckpointID != checkpointID {
			continue
		}
		if item.Status == model.CheckpointReady {
			return item, nil
		}
		if checkpointID != "" {
			return model.Checkpoint{}, ErrRunResumeConflict
		}
	}
	return model.Checkpoint{}, ErrRunResumeConflict
}

func resetFailedWorkflowCompensation(state *workflowRuntimeState, runID string, now time.Time) (model.Step, error) {
	for index := len(state.Compensations) - 1; index >= 0; index-- {
		compensation := state.Compensations[index]
		if compensation.Status != model.WorkflowCompensationFailed {
			continue
		}
		compensation.Status, compensation.Error = model.WorkflowCompensationPending, ""
		state.Compensations[index] = compensation
		path := "compensation/" + compensation.ActivationKey + "/" + compensation.Undo.ID
		activation, ok := state.Activations[path]
		if !ok {
			return model.Step{}, ErrRunResumeConflict
		}
		activation.Status, activation.ErrorCode, activation.ErrorMessage = model.WorkflowStepStatusRunning, "", ""
		activation.WaitID, activation.InteractionID, activation.ChildRunID, activation.WakeAt = "", "", "", nil
		activation.ReservedLLM, activation.ReservedTools, activation.ReservedChildren = 0, 0, 0
		activation.Attempt++
		state.Activations[path] = activation
		return model.Step{
			StepID: activation.StepID, RunID: runID, NodeID: activation.NodeID, ActivationPath: activation.Path,
			LaneID: activation.ScopeKey, Attempt: activation.Attempt, Kind: compensation.Undo.Type,
			Title: compensation.Undo.ID, Status: model.WorkflowStepStatusRunning, StartedAt: now,
		}, nil
	}
	return model.Step{}, ErrRunResumeConflict
}

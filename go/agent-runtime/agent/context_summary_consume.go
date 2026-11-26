package agent

import (
	"context"
	"fmt"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func (runner *Runner) consumeContextSummary(ctx context.Context, snapshot kernel.Snapshot, state runState) (kernel.Snapshot, error) {
	adapter := runner.contextSummary
	if adapter == nil {
		return snapshot, ErrInvalidRequest
	}
	last, ok := activeModelInvocation(state)
	if !ok || last.Status != ModelInvocationCompleted || last.Purpose != ModelInvocationContextSummary || !validContextSummaryInvocation(*last) {
		return snapshot, ErrInvalidRequest
	}
	if runner.rootCallbacks != nil {
		runner.rootCallbacks.summaryPhaseEntered.Store(true)
	}
	invocation := cloneModelInvocation(*last)
	binding := invocation.ContextSummary
	read, err := adapter.ports.ReadSummary(ctx, *cloneContextSummaryBinding(binding))
	if err != nil {
		return snapshot, err
	}
	read.SourceCheckpoint = cloneSummaryCheckpoint(read.SourceCheckpoint)
	read.Cursor = cloneSummaryCursor(read.Cursor)
	if !callbackscope.Operation(ctx, adapter.bounded, "context.summary.read", read) || read.Root != binding.Root ||
		!summarySourceMatches(binding.Source, read.SourceCheckpoint) || read.SourceCheckpoint.ScopeID != binding.Cursor.Turn.SessionID ||
		read.Cursor.ExpectedHeadCheckpointID != binding.Cursor.ExpectedHeadCheckpointID ||
		!runtimecontext.ValidCheckpoint(read.Cursor.Checkpoint) || read.Cursor.Turn.ContextCheckpointID != read.Cursor.Checkpoint.ID ||
		!sameBoundedValue(read.Cursor.Turn.ContextRef, summaryRef(read.Cursor.Checkpoint)) {
		return snapshot, ErrInvalidRequest
	}
	working, parseErr := WorkingStateFromSummaryInvocation(invocation)
	var target *runtimecontext.Checkpoint
	var artifacts []runtimecontext.Artifact
	if parseErr == nil {
		candidate, candidateErr := adapter.bounded.manager.CompactPortable(runtimecontext.PortableCompactionRequest{
			Previous: read.SourceCheckpoint, RunID: snapshot.Run.ID, Messages: runtimecontext.Materialize(read.SourceCheckpoint.Window),
			Policy: binding.ContextPolicy, OmitExtract: true,
		})
		if candidateErr != nil || candidate.RemovedMessages != binding.RemovedMessages || !summarySameArtifacts(binding.Artifacts, candidate.Artifacts) {
			return snapshot, ErrInvalidRequest
		}
		candidate.Window.WorkingState = working
		next, rolloverErr := adapter.bounded.manager.Rollover(ctx, runtimecontext.RolloverRequest{
			Previous: read.SourceCheckpoint, Window: candidate.Window, Artifacts: candidate.Artifacts,
			Reason: "working_state_summary", ModelWindowFingerprint: binding.ModelWindowFingerprint,
		})
		if rolloverErr != nil {
			return snapshot, rolloverErr
		}
		trial := model.CloneRequest(binding.BusinessRequest)
		trial.Messages = runtimecontext.Materialize(next.Window)
		assessment, assessErr := binding.BusinessAccounting.AssessModelRequest(summaryBusinessAssessmentRequest(trial), binding.ModelWindow, binding.ContextPolicy)
		if assessErr != nil {
			return snapshot, assessErr
		}
		if runtimecontext.WithinHardBudget(assessment, binding.ContextPolicy) && assessment.SerializedBytes < binding.OriginalAssessment.SerializedBytes &&
			assessment.HardTokenEstimate < binding.OriginalAssessment.HardTokenEstimate {
			target, artifacts = &next, candidate.Artifacts
		}
	}
	cursor, alreadyApplied, err := reconcileSummaryCursor(*binding, read, target)
	if err != nil {
		return snapshot, err
	}
	// Compute the same native usage mutation before any additional Host write.
	// An exhausted budget consumes the known bill but never starts business work.
	consumed, consumedOK := consumeLastModelInvocation(&state, runner.clock.Now())
	if !consumedOK {
		return snapshot, ErrInvalidRequest
	}
	dimension, usageErr := chargeConsumedModelUsage(&state, consumed)
	if usageErr != nil {
		return runner.fail(ctx, snapshot, state, "agent.usage_invalid", usageErr)
	}
	if dimension != "" {
		return runner.fail(ctx, snapshot, state, "agent."+string(dimension)+"_budget", fmt.Errorf("%w: %s", ErrBudgetExceeded, dimension))
	}
	if target != nil {
		if err = runner.runtime.RequireExecution(ctx, snapshot.Run.ID); err != nil {
			return snapshot, err
		}
		for _, artifact := range artifacts {
			saved, putErr := adapter.bounded.ports.PutArtifact(ctx, runtimecontext.CloneArtifacts([]runtimecontext.Artifact{artifact})[0])
			if putErr != nil {
				return snapshot, putErr
			}
			if !sameBoundedValue(saved, artifact) || !callbackscope.Operation(ctx, adapter.bounded, "context.summary.artifact:"+artifact.ID, artifact) {
				return snapshot, ErrInvalidRequest
			}
		}
		if !alreadyApplied {
			if err = runner.runtime.RequireExecution(ctx, snapshot.Run.ID); err != nil {
				return snapshot, err
			}
			receipt, commitErr := adapter.ports.CommitSummary(ctx, cloneSummaryCursor(cursor), runtimecontext.CloneCheckpoint(*target))
			if commitErr != nil {
				return snapshot, commitErr
			}
			expected := cursor.Turn
			expected.ContextCheckpointID, expected.ContextRef = target.ID, summaryRef(*target)
			expected.Revision++
			expected.UpdatedAt = cursor.UpdatedAt
			if !sameBoundedValue(receipt.Turn, expected) || !sameBoundedValue(receipt.Checkpoint, *target) ||
				receipt.ExpectedTurnCheckpointID != cursor.ExpectedTurnCheckpointID || receipt.ExpectedHeadCheckpointID != cursor.ExpectedHeadCheckpointID ||
				!callbackscope.Operation(ctx, adapter.bounded, "context.summary.commit:"+target.ID, receipt) {
				return snapshot, ErrInvalidRequest
			}
		}
		if err = runner.runtime.RequireExecution(ctx, snapshot.Run.ID); err != nil {
			return snapshot, err
		}
		bound, bindErr := adapter.ports.BindSummary(ctx, cloneSummaryCursor(cursor), runtimecontext.CloneCheckpoint(*target))
		if bindErr != nil {
			return snapshot, bindErr
		}
		if bound.TurnID != cursor.Turn.ID || bound.Access != "owner" || !sameBoundedValue(bound.Previous, cursor.Checkpoint) ||
			!sameBoundedValue(bound.Current, *target) || !callbackscope.Operation(ctx, adapter.bounded, "context.summary.bind:"+target.ID, bound) {
			return snapshot, ErrInvalidRequest
		}
		state.ModelInvocations[len(state.ModelInvocations)-1].SummaryTargetHash = workingStateFingerprint(working)
	}
	encoded, err := encodeState(state)
	if err != nil {
		return snapshot, err
	}
	mutation := kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{Type: EventContextSummaryConsumed, Message: invocation.ID, Wakeup: true}},
	}
	next, err := runner.commitNativeTransition(ctx, snapshot, mutation)
	if err != nil && next.Run.ID == "" {
		return snapshot, err
	}
	return next, err
}

func reconcileSummaryCursor(binding ContextSummaryBinding, read ContextSummaryRead, target *runtimecontext.Checkpoint) (SummaryCursor, bool, error) {
	cursor := cloneSummaryCursor(read.Cursor)
	if cursor.Turn.ID != binding.Cursor.Turn.ID || cursor.Turn.SessionID != binding.Cursor.Turn.SessionID ||
		cursor.Turn.ConfigSnapshotID != binding.Cursor.Turn.ConfigSnapshotID || cursor.ExpectedTurnCheckpointID != cursor.Checkpoint.ID {
		return SummaryCursor{}, false, ErrInvalidRequest
	}
	if cursor.Checkpoint.ID == binding.Source.CheckpointID {
		if !sameBoundedValue(cursor.Checkpoint, read.SourceCheckpoint) || !summaryTurnProjection(binding.Cursor.Turn, cursor.Turn) {
			return SummaryCursor{}, false, ErrInvalidRequest
		}
		cursor.ExpectedHeadCheckpointID = binding.Cursor.ExpectedHeadCheckpointID
		cursor.UpdatedAt = binding.Cursor.UpdatedAt
		if cursor.UpdatedAt.Before(cursor.Turn.UpdatedAt) {
			cursor.UpdatedAt = cursor.Turn.UpdatedAt
		}
		return cursor, false, nil
	}
	if target == nil || !sameBoundedValue(cursor.Checkpoint, *target) {
		return SummaryCursor{}, false, ErrInvalidRequest
	}
	expected := binding.Cursor.Turn
	expected.ContextCheckpointID, expected.ContextRef = target.ID, summaryRef(*target)
	expected.Revision++
	expected.UpdatedAt = binding.Cursor.UpdatedAt
	// The source path clamps its write time to the original Turn. Rebuild the
	// same value after an applied commit, including older frozen recipes whose
	// Host clock preceded that Turn's timestamp.
	if expected.UpdatedAt.Before(binding.Cursor.Turn.UpdatedAt) {
		expected.UpdatedAt = binding.Cursor.Turn.UpdatedAt
	}
	if summaryTurnProjection(expected, cursor.Turn) {
		return cursor, true, nil
	}
	return SummaryCursor{}, false, ErrInvalidRequest
}

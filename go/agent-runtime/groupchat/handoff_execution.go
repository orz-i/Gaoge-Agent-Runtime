package groupchat

import (
	"context"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

func (runner *Runner) executeHandoff(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
) (kernel.Snapshot, error) {
	if runner.visibleHandoffs == nil || runner.relations == nil {
		return runner.fail(ctx, snapshot, state, "groupchat.handoff_unavailable", ErrInvalidRequest)
	}
	for snapshot.Run.Status == kernel.RunStatusRunning {
		if state.NextSpeakerIndex >= len(state.Speakers) {
			state.TerminationReason = TerminationHandoffCompleted
			return runner.complete(ctx, snapshot, state)
		}
		next, nextState, err := runner.executeCurrentSpeaker(ctx, snapshot, state)
		if err != nil {
			return next, err
		}
		snapshot, state = next, nextState
		completed := state.Speakers[state.NextSpeakerIndex-1]
		signal, found, err := runner.visibleHandoffs.ResolveVisibleHandoff(
			ctx, completed.Delegation.ChildRunID,
		)
		if err != nil {
			return runner.fail(ctx, snapshot, state, "groupchat.handoff_signal_failed", err)
		}
		if !found {
			state.TerminationReason = TerminationHandoffCompleted
			return runner.complete(ctx, snapshot, state)
		}
		signal = normalizedVisibleHandoffSignal(signal)
		if !validVisibleHandoffTarget(state, completed.Turn.SpeakerID, signal.TargetParticipantID) {
			return runner.fail(ctx, snapshot, state, "groupchat.handoff_target_invalid", ErrInvalidRequest)
		}
		if len(state.Speakers) >= state.MaxUtterances {
			state.TerminationReason = TerminationMaxUtterances
			return runner.complete(ctx, snapshot, state)
		}
		config, ok := speakerConfigForID(state.SpeakerConfigs, signal.TargetParticipantID)
		if !ok {
			return runner.fail(ctx, snapshot, state, "groupchat.handoff_target_missing", ErrInvalidRequest)
		}
		speaker, err := runner.newSpeakerExecution(config, signal.TargetParticipantID, len(state.Speakers))
		if err != nil {
			return runner.fail(ctx, snapshot, state, "groupchat.handoff_materialize_failed", err)
		}
		state.Speakers = append(state.Speakers, speaker)
		persisted, err := runner.persistRunning(ctx, snapshot, state)
		if err != nil {
			return persisted, err
		}
		snapshot = persisted
		state, err = decodeState(snapshot.State)
		if err != nil {
			return runner.fail(ctx, snapshot, executionState{}, "groupchat.state_invalid", err)
		}
	}
	return snapshot, nil
}

// ValidateVisibleHandoff validates a tool request without mutating the parent
// Group Chat Run. The durable signal itself is persisted by the hosting Harness.
func (runner *Runner) ValidateVisibleHandoff(
	ctx context.Context,
	childRunID string,
	targetParticipantID string,
) error {
	if runner == nil || runner.runtime == nil || runner.relations == nil {
		return ErrInvalidRequest
	}
	childRunID = strings.TrimSpace(childRunID)
	targetParticipantID = strings.TrimSpace(targetParticipantID)
	if childRunID == "" || targetParticipantID == "" {
		return ErrInvalidRequest
	}
	relation, err := runner.relations.GetByChild(ctx, childRunID)
	if err != nil {
		if errors.Is(err, runrelation.ErrNotFound) {
			return ErrInvalidRequest
		}
		return err
	}
	if relation.Kind != runrelation.KindGroupChatSpeaker {
		return ErrInvalidRequest
	}
	parent, err := runner.runtime.Load(ctx, relation.ParentRunID)
	if err != nil {
		return err
	}
	if parent.Run.Kind != RunKind || parent.Run.Status != kernel.RunStatusRunning {
		return ErrInvalidRequest
	}
	state, err := decodeState(parent.State)
	if err != nil {
		return err
	}
	if state.SpeakerPolicy != SpeakerHandoff ||
		state.NextSpeakerIndex < 0 ||
		state.NextSpeakerIndex >= len(state.Speakers) {
		return ErrInvalidRequest
	}
	current := state.Speakers[state.NextSpeakerIndex]
	if current.Delegation.ChildRunID != childRunID {
		return ErrInvalidRequest
	}
	if !validVisibleHandoffTarget(state, current.Turn.SpeakerID, targetParticipantID) {
		return ErrInvalidRequest
	}
	return nil
}

func validVisibleHandoffTarget(
	state executionState,
	currentSpeakerID string,
	targetParticipantID string,
) bool {
	targetParticipantID = strings.TrimSpace(targetParticipantID)
	currentSpeakerID = strings.TrimSpace(currentSpeakerID)
	if targetParticipantID == "" || targetParticipantID == currentSpeakerID {
		return false
	}
	for _, candidateID := range state.CandidateSpeakerIDs {
		if strings.TrimSpace(candidateID) == targetParticipantID {
			return true
		}
	}
	return false
}

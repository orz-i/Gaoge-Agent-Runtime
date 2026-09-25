package groupchat

import (
	"context"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func (runner *Runner) executeSelector(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
) (kernel.Snapshot, error) {
	if runner.selector == nil {
		return runner.fail(ctx, snapshot, state, "groupchat.selector_unavailable", ErrInvalidRequest)
	}
	for snapshot.Run.Status == kernel.RunStatusRunning {
		if state.NextSpeakerIndex < len(state.Speakers) {
			next, nextState, err := runner.executeCurrentSpeaker(ctx, snapshot, state)
			if err != nil {
				return next, err
			}
			snapshot, state = next, nextState
			persisted, persistErr := runner.persistRunning(ctx, snapshot, state)
			if persistErr != nil {
				return persisted, persistErr
			}
			snapshot = persisted
			state, err = decodeState(snapshot.State)
			if err != nil {
				return runner.fail(ctx, snapshot, executionState{}, "groupchat.state_invalid", err)
			}
			continue
		}
		if len(state.Speakers) >= state.MaxUtterances {
			state.TerminationReason = TerminationMaxUtterances
			return runner.complete(ctx, snapshot, state)
		}
		if state.SelectorInvocation == nil || state.SelectorInvocation.Status == SelectorInvocationConsumed {
			next, nextState, err := runner.prepareSelectorInvocation(ctx, snapshot, state)
			if err != nil {
				return next, err
			}
			snapshot, state = next, nextState
			continue
		}
		next, err := runner.advanceSelectorInvocation(ctx, snapshot, state)
		if err != nil {
			return next, err
		}
		snapshot = next
		if snapshot.Run.Status != kernel.RunStatusRunning {
			return snapshot, nil
		}
		state, err = decodeState(snapshot.State)
		if err != nil {
			return runner.fail(ctx, snapshot, executionState{}, "groupchat.state_invalid", err)
		}
	}
	return snapshot, nil
}

func (runner *Runner) prepareSelectorInvocation(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
) (kernel.Snapshot, executionState, error) {
	request, ok := selectorRequestForState(snapshot.Run.ID, state)
	if !ok {
		state.TerminationReason = TerminationNoEligibleSpeaker
		completed, err := runner.complete(ctx, snapshot, state)
		return completed, state, err
	}
	invocation, err := newSelectorInvocation(request, len(state.Speakers), runner.runtime.Now())
	if err != nil {
		failed, failErr := runner.fail(ctx, snapshot, state, "groupchat.selector_invalid", err)
		return failed, state, failErr
	}
	state.SelectorInvocation = &invocation
	encoded, err := encodeState(state)
	if err != nil {
		return snapshot, state, err
	}
	updated, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning,
		State:  encoded,
		Events: []kernel.EventDraft{{
			Type: "groupchat.selector.created", Message: invocation.ID, Wakeup: true,
		}},
	})
	if err != nil {
		return snapshot, state, err
	}
	return updated, state, nil
}

func (runner *Runner) advanceSelectorInvocation(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
) (kernel.Snapshot, error) {
	invocation := cloneSelectorInvocation(state.SelectorInvocation)
	if invocation == nil {
		return runner.fail(ctx, snapshot, state, "groupchat.selector_invalid", ErrInvalidRequest)
	}
	if invocation.Status == SelectorInvocationPending {
		claimed, nextState, err := runner.claimSelectorInvocation(ctx, snapshot, state, invocation)
		if err != nil {
			return claimed, err
		}
		snapshot, state = claimed, nextState
		invocation = cloneSelectorInvocation(state.SelectorInvocation)
		response, selectErr := runner.selector.Select(ctx, cloneSelectorRequest(invocation.Request))
		if selectErr != nil {
			return runner.handleSelectorFailure(ctx, snapshot, state, selectErr)
		}
		if !validSelectorResponse(response, invocation.Request) {
			return runner.fail(ctx, snapshot, state, "groupchat.selector_invalid_response", ErrSelectorFailure)
		}
		completed, err := runner.completeSelectorInvocation(ctx, snapshot, state, response)
		if err != nil {
			return completed, err
		}
		snapshot = completed
		state, err = decodeState(snapshot.State)
		if err != nil {
			return runner.fail(ctx, snapshot, executionState{}, "groupchat.state_invalid", err)
		}
		invocation = cloneSelectorInvocation(state.SelectorInvocation)
	}
	if invocation == nil || invocation.Status != SelectorInvocationCompleted {
		return snapshot, ErrSelectorPending
	}
	return runner.consumeSelectorInvocation(ctx, snapshot, state)
}

func (runner *Runner) claimSelectorInvocation(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	invocation *SelectorInvocation,
) (kernel.Snapshot, executionState, error) {
	now := runner.runtime.Now().UTC()
	if invocation.ExecutionLeaseUntil != nil && now.Before(invocation.ExecutionLeaseUntil.UTC()) {
		return snapshot, state, ErrSelectorInvocationBusy
	}
	leaseUntil := now.Add(selectorInvocationLeaseDuration)
	invocation.ExecutionAttempt++
	invocation.ExecutionLeaseUntil = &leaseUntil
	state.SelectorInvocation = cloneSelectorInvocation(invocation)
	encoded, err := encodeState(state)
	if err != nil {
		return snapshot, state, err
	}
	claimed, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded,
		Events: []kernel.EventDraft{{
			Type: "groupchat.selector.claimed", Message: invocation.ID, Wakeup: true, WakeupAt: &leaseUntil,
		}},
	})
	return claimed, state, err
}

func (runner *Runner) completeSelectorInvocation(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	response SelectorResponse,
) (kernel.Snapshot, error) {
	invocation := cloneSelectorInvocation(state.SelectorInvocation)
	if invocation == nil {
		return snapshot, ErrInvalidRequest
	}
	now := runner.runtime.Now().UTC()
	invocation.Status = SelectorInvocationCompleted
	invocation.Response = response
	invocation.Response.SpeakerID = strings.TrimSpace(invocation.Response.SpeakerID)
	invocation.Response.ResponseID = strings.TrimSpace(invocation.Response.ResponseID)
	invocation.ExecutionLeaseUntil = nil
	invocation.CompletedAt = &now
	state.SelectorInvocation = invocation
	encoded, err := encodeState(state)
	if err != nil {
		return snapshot, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded,
		Events: []kernel.EventDraft{{
			Type: "groupchat.selector.completed", Message: invocation.ID, Wakeup: true,
		}},
	})
}

func (runner *Runner) consumeSelectorInvocation(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
) (kernel.Snapshot, error) {
	invocation := cloneSelectorInvocation(state.SelectorInvocation)
	if invocation == nil || invocation.Status != SelectorInvocationCompleted {
		return snapshot, ErrInvalidRequest
	}
	now := runner.runtime.Now().UTC()
	invocation.Status = SelectorInvocationConsumed
	invocation.ConsumedAt = &now
	state.SelectorInvocation = invocation
	if invocation.Response.Complete {
		state.TerminationReason = TerminationSelectorCompleted
		return runner.complete(ctx, snapshot, state)
	}
	config, ok := speakerConfigForID(state.SpeakerConfigs, invocation.Response.SpeakerID)
	if !ok {
		return runner.fail(ctx, snapshot, state, "groupchat.selector_speaker_missing", ErrSelectorFailure)
	}
	speaker, err := runner.newSpeakerExecution(config, invocation.Response.SpeakerID, len(state.Speakers))
	if err != nil {
		return runner.fail(ctx, snapshot, state, "groupchat.selector_speaker_invalid", err)
	}
	state.Speakers = append(state.Speakers, speaker)
	encoded, err := encodeState(state)
	if err != nil {
		return snapshot, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded,
		Events: []kernel.EventDraft{{
			Type: "groupchat.selector.consumed", Message: invocation.Response.SpeakerID, Wakeup: true,
		}},
	})
}

func (runner *Runner) handleSelectorFailure(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	cause error,
) (kernel.Snapshot, error) {
	var retryable interface{ Retryable() bool }
	if errors.As(cause, &retryable) && retryable.Retryable() {
		invocation := cloneSelectorInvocation(state.SelectorInvocation)
		if invocation == nil {
			return snapshot, ErrSelectorPending
		}
		invocation.ExecutionLeaseUntil = nil
		state.SelectorInvocation = invocation
		encoded, err := encodeState(state)
		if err != nil {
			return snapshot, err
		}
		released, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: encoded,
			Events: []kernel.EventDraft{{
				Type: "groupchat.selector.retry", Message: invocation.ID, Wakeup: true,
				WakeupAt: groupChatWakeupAt(runner.runtime.Now()),
			}},
		})
		return released, errors.Join(ErrSelectorPending, err)
	}
	return runner.fail(ctx, snapshot, state, "groupchat.selector_failed", errors.Join(ErrSelectorFailure, cause))
}

func selectorRequestForState(runID string, state executionState) (SelectorRequest, bool) {
	candidates := selectorCandidates(state)
	if len(candidates) == 0 {
		return SelectorRequest{}, false
	}
	return SelectorRequest{
		RunID: strings.TrimSpace(runID), Goal: state.Goal,
		Model: state.SelectorModel, ModelOptions: append([]byte(nil), state.SelectorModelOptions...),
		Candidates: candidates, History: selectorHistory(state),
		CanComplete: len(state.Speakers) > 0,
	}, true
}

func selectorCandidates(state executionState) []SelectorCandidate {
	configs := speakerConfigByParticipant(state.SpeakerConfigs)
	participants := participantByID(state.Participants)
	lastSpeakerID := ""
	if len(state.Speakers) > 0 {
		lastSpeakerID = state.Speakers[len(state.Speakers)-1].Turn.SpeakerID
	}
	result := make([]SelectorCandidate, 0, len(state.CandidateSpeakerIDs))
	for _, participantID := range state.CandidateSpeakerIDs {
		if len(state.CandidateSpeakerIDs) > 1 && participantID == lastSpeakerID {
			continue
		}
		config, found := configs[participantID]
		if !found {
			continue
		}
		participant := participants[participantID]
		result = append(result, SelectorCandidate{
			ID: participantID, Name: config.RoleName, Description: participant.Description,
		})
	}
	return result
}

func selectorHistory(state executionState) []SelectorHistoryItem {
	result := make([]SelectorHistoryItem, 0, len(state.Speakers))
	for _, speaker := range state.Speakers {
		if speaker.Turn.Status != SpeakerTurnCompleted {
			continue
		}
		content := delegatedResultContent(speaker.Turn.Result)
		if content == "" {
			continue
		}
		result = append(result, SelectorHistoryItem{
			SpeakerID: speaker.Turn.SpeakerID, SpeakerName: speaker.Turn.RoleName, Content: content,
		})
	}
	return result
}

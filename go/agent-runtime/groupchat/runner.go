package groupchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

const (
	CapabilityRunner      kernel.Capability = "groupchat.runner"
	autonomousWakeupDelay                   = time.Second
)

// Delegator is the narrow Handoff capability consumed by Group Chat.
type Delegator interface {
	StartOrLoad(context.Context, kernel.Snapshot, handoff.Delegation) (handoff.Delegation, error)
}

// Dependencies are the only requirements of the Group Chat feature.
type Dependencies struct {
	Runtime         *kernel.Runtime
	Handoffs        Delegator
	Selector        Selector
	Relations       runrelation.Recorder
	MaxParticipants int
	MaxUtterances   int
}

// Runner owns directed speaker sequencing and durable child ownership.
type Runner struct {
	runtime         *kernel.Runtime
	handoffs        Delegator
	selector        Selector
	relations       runrelation.Recorder
	maxParticipants int
	maxUtterances   int
}

type speakerExecution struct {
	Turn       SpeakerTurn        `json:"turn"`
	Delegation handoff.Delegation `json:"delegation"`
}

type executionState struct {
	Goal                 string              `json:"goal"`
	SpeakerPolicy        SpeakerPolicy       `json:"speakerPolicy"`
	Participants         []Participant       `json:"participants"`
	SpeakerConfigs       []SpeakerConfig     `json:"speakerConfigs"`
	DirectedSpeakerIDs   []string            `json:"directedSpeakerIDs,omitempty"`
	CandidateSpeakerIDs  []string            `json:"candidateSpeakerIDs,omitempty"`
	SelectorModel        string              `json:"selectorModel,omitempty"`
	SelectorModelOptions json.RawMessage     `json:"selectorModelOptions,omitempty"`
	SelectorInvocation   *SelectorInvocation `json:"selectorInvocation,omitempty"`
	MaxUtterances        int                 `json:"maxUtterances"`
	Speakers             []speakerExecution  `json:"speakers"`
	NextSpeakerIndex     int                 `json:"nextSpeakerIndex"`
	TerminationReason    TerminationReason   `json:"terminationReason,omitempty"`
}

func groupChatWakeupAt(now time.Time) *time.Time {
	value := now.UTC().Add(autonomousWakeupDelay)
	return &value
}

// NewRunner creates an independent Group Chat feature.
func NewRunner(dependencies Dependencies) (*Runner, error) {
	if dependencies.Runtime == nil || dependencies.Handoffs == nil {
		return nil, ErrInvalidRequest
	}
	if dependencies.MaxParticipants <= 0 || dependencies.MaxParticipants > MaxParticipantCount {
		dependencies.MaxParticipants = MaxParticipantCount
	}
	if dependencies.MaxUtterances <= 0 || dependencies.MaxUtterances > MaxUtteranceCount {
		dependencies.MaxUtterances = MaxUtteranceCount
	}
	return &Runner{
		runtime: dependencies.Runtime, handoffs: dependencies.Handoffs, selector: dependencies.Selector, relations: dependencies.Relations,
		maxParticipants: dependencies.MaxParticipants, maxUtterances: dependencies.MaxUtterances,
	}, nil
}

// Descriptor declares the explicit Group Chat capability graph.
func (runner *Runner) Descriptor() kernel.FeatureDescriptor {
	requires := []kernel.Capability{kernel.CapabilityRuntime, handoff.CapabilityCoordinator}
	if runner != nil && runner.relations != nil {
		requires = append(requires, runrelation.CapabilityRelations)
	}
	return kernel.FeatureDescriptor{
		Name: "groupchat", Requires: requires, Provides: []kernel.Capability{CapabilityRunner},
	}
}

// StartRun creates one explicit directed Group Chat Run.
func (runner *Runner) StartRun(ctx context.Context, request StartRequest) (kernel.Snapshot, error) {
	request = normalizeStartRequest(request)
	if ValidateStartRequest(request) != nil || !validSpeakerConfigs(request, runner.maxParticipants, runner.maxUtterances) {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	state, err := runner.materializeState(request)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	snapshot, err := runner.runtime.Create(ctx, kernel.CreateRequest{
		ID: request.ID, Kind: RunKind, Actor: request.Actor, Thread: request.Thread,
		RequestID: request.RequestID, Goal: request.Goal, State: encoded,
		Events: []kernel.EventDraft{{
			Type: "groupchat.started", Message: "Group Chat materialized", Wakeup: true,
			WakeupAt: groupChatWakeupAt(runner.runtime.Now()),
		}},
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.execute(ctx, snapshot)
}

// Resume continues one non-terminal Group Chat Run without recreating speaker children.
func (runner *Runner) Resume(ctx context.Context, runID string, expectedRevision uint64) (kernel.Snapshot, error) {
	snapshot, err := runner.runtime.Load(ctx, strings.TrimSpace(runID))
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if snapshot.Run.Revision != expectedRevision {
		return kernel.Snapshot{}, kernel.ErrConflict
	}
	if snapshot.Run.Status != kernel.RunStatusRunning {
		return snapshot, ErrGroupChatTerminal
	}
	return runner.execute(ctx, snapshot)
}

// ViewState decodes an isolated public Group Chat view from Kernel opaque state.
func ViewState(snapshot kernel.Snapshot) (View, error) {
	state, err := decodeState(snapshot.State)
	if err != nil {
		return View{}, err
	}
	turns := make([]SpeakerTurn, 0, len(state.Speakers))
	for _, speaker := range state.Speakers {
		turns = append(turns, cloneSpeakerTurn(speaker.Turn))
	}
	return View{
		SpeakerPolicy:       state.SpeakerPolicy,
		Participants:        cloneParticipants(state.Participants),
		DirectedSpeakerIDs:  append([]string(nil), state.DirectedSpeakerIDs...),
		CandidateSpeakerIDs: append([]string(nil), state.CandidateSpeakerIDs...),
		SelectorInvocation:  cloneSelectorInvocation(state.SelectorInvocation),
		SpeakerTurns:        turns,
		NextSpeakerIndex:    state.NextSpeakerIndex,
		TerminationReason:   state.TerminationReason,
	}, nil
}

func (runner *Runner) materializeState(request StartRequest) (executionState, error) {
	state := executionState{
		Goal:                 strings.TrimSpace(request.Goal),
		SpeakerPolicy:        request.SpeakerPolicy,
		Participants:         cloneParticipants(request.Participants),
		SpeakerConfigs:       cloneSpeakerConfigs(request.SpeakerConfigs),
		DirectedSpeakerIDs:   append([]string(nil), request.DirectedSpeakerIDs...),
		CandidateSpeakerIDs:  append([]string(nil), request.CandidateSpeakerIDs...),
		SelectorModel:        strings.TrimSpace(request.SelectorModel),
		SelectorModelOptions: append(json.RawMessage(nil), request.SelectorModelOptions...),
		MaxUtterances:        request.MaxUtterances,
		Speakers:             []speakerExecution{},
	}
	if request.SpeakerPolicy != SpeakerDirected {
		return state, nil
	}
	configByParticipant := speakerConfigByParticipant(request.SpeakerConfigs)
	for ordinal, participantID := range request.DirectedSpeakerIDs {
		speaker, err := runner.newSpeakerExecution(configByParticipant[participantID], participantID, ordinal)
		if err != nil {
			return executionState{}, err
		}
		state.Speakers = append(state.Speakers, speaker)
	}
	return state, nil
}

func (runner *Runner) newSpeakerExecution(
	config SpeakerConfig,
	participantID string,
	ordinal int,
) (speakerExecution, error) {
	delegationID, err := runner.runtime.NewID("ghd")
	if err != nil {
		return speakerExecution{}, err
	}
	childRunID, err := runner.runtime.NewID("run")
	if err != nil {
		return speakerExecution{}, err
	}
	delegation := handoff.Delegation{
		ID: delegationID, MemberID: participantID,
		RoleID: config.RoleID, RoleRevision: config.RoleRevision, RoleName: config.RoleName,
		Instructions: config.Instructions, Limits: config.Limits,
		ChildRunID: childRunID, Model: config.Model,
		ModelOptions: append(json.RawMessage(nil), config.ModelOptions...),
		ToolKeys:     append([]string(nil), config.ToolKeys...), Status: handoff.StatusQueued,
	}
	return speakerExecution{
		Turn: SpeakerTurn{
			Ordinal: ordinal, SpeakerID: participantID,
			RoleID: config.RoleID, RoleRevision: config.RoleRevision, RoleName: config.RoleName,
			ChildRunID: childRunID, Status: SpeakerTurnQueued,
		},
		Delegation: delegation,
	}, nil
}

func (runner *Runner) execute(ctx context.Context, snapshot kernel.Snapshot) (kernel.Snapshot, error) {
	state, err := decodeState(snapshot.State)
	if err != nil {
		return runner.fail(ctx, snapshot, executionState{}, "groupchat.state_invalid", err)
	}
	switch state.SpeakerPolicy {
	case SpeakerDirected:
		return runner.executeDirected(ctx, snapshot, state)
	case SpeakerSelector:
		return runner.executeSelector(ctx, snapshot, state)
	default:
		return runner.fail(ctx, snapshot, state, "groupchat.policy_invalid", ErrInvalidRequest)
	}
}

func (runner *Runner) executeDirected(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
) (kernel.Snapshot, error) {
	for state.NextSpeakerIndex < len(state.Speakers) {
		next, nextState, err := runner.executeCurrentSpeaker(ctx, snapshot, state)
		if err != nil {
			return next, err
		}
		snapshot, state = next, nextState
	}
	state.TerminationReason = TerminationCompleted
	return runner.complete(ctx, snapshot, state)
}

func (runner *Runner) executeCurrentSpeaker(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
) (kernel.Snapshot, executionState, error) {
	index := state.NextSpeakerIndex
	current := &state.Speakers[index]
	if err := runner.ensureSpeakerRelation(ctx, snapshot, *current); err != nil {
		failed, failErr := runner.fail(ctx, snapshot, state, "groupchat.relation_failed", err)
		return failed, state, failErr
	}
	current.Delegation.Goal = speakerGoal(state, index)
	delegation, delegateErr := runner.handoffs.StartOrLoad(ctx, snapshot, current.Delegation)
	if delegation.ID != "" {
		current.Delegation = delegation
		current.Turn = projectSpeakerTurn(current.Turn, delegation)
	}
	switch {
	case errors.Is(delegateErr, handoff.ErrChildPending):
		pending, persistErr := runner.persistRunning(ctx, snapshot, state)
		return pending, state, errors.Join(ErrSpeakerPending, persistErr)
	case errors.Is(delegateErr, handoff.ErrChildFailed):
		failed, failErr := runner.fail(ctx, snapshot, state, "groupchat.speaker_failed", ErrGroupChatFailed)
		return failed, state, failErr
	case delegateErr != nil:
		failed, failErr := runner.fail(ctx, snapshot, state, "groupchat.speaker_failed", delegateErr)
		return failed, state, failErr
	case delegation.Status == handoff.StatusCompleted:
		state.NextSpeakerIndex++
		return snapshot, state, nil
	default:
		pending, persistErr := runner.persistRunning(ctx, snapshot, state)
		return pending, state, errors.Join(ErrSpeakerPending, persistErr)
	}
}

func (runner *Runner) ensureSpeakerRelation(
	ctx context.Context,
	parent kernel.Snapshot,
	speaker speakerExecution,
) error {
	if runner.relations == nil {
		return nil
	}
	_, err := runner.relations.Ensure(ctx, runrelation.Draft{
		ParentRunID: parent.Run.ID,
		ChildRunID:  speaker.Delegation.ChildRunID,
		Kind:        runrelation.KindGroupChatSpeaker,
		OwnerNodeID: speaker.Delegation.ID,
	})
	return err
}

func (runner *Runner) persistRunning(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
) (kernel.Snapshot, error) {
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded,
		Events: []kernel.EventDraft{{Type: "groupchat.progressed", Message: "Group Chat progressed"}},
	})
}

func (runner *Runner) complete(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
) (kernel.Snapshot, error) {
	encodedState, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	result, err := json.Marshal(publicResult(state))
	if err != nil {
		return kernel.Snapshot{}, errors.Join(ErrInvalidRequest, err)
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: encodedState,
		Result: &kernel.Result{ContentType: "application/json", Content: result},
		Events: []kernel.EventDraft{{Type: "groupchat.completed", Message: "Group Chat completed"}},
	})
}

func (runner *Runner) fail(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	code string,
	cause error,
) (kernel.Snapshot, error) {
	state.TerminationReason = TerminationFailed
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, errors.Join(cause, err)
	}
	failed, transitionErr := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusFailed, State: encoded,
		ErrorCode: strings.TrimSpace(code), ErrorDetail: errorText(cause),
		Events: []kernel.EventDraft{{Type: "groupchat.failed", Message: strings.TrimSpace(code)}},
	})
	return failed, errors.Join(cause, transitionErr)
}

func publicResult(state executionState) Result {
	result := Result{Kind: ResultKind, Utterances: make([]Utterance, 0, len(state.Speakers))}
	for _, speaker := range state.Speakers {
		if speaker.Turn.Status != SpeakerTurnCompleted {
			continue
		}
		result.Utterances = append(result.Utterances, Utterance{
			Ordinal:      speaker.Turn.Ordinal,
			SpeakerID:    speaker.Turn.SpeakerID,
			RoleID:       speaker.Turn.RoleID,
			RoleRevision: speaker.Turn.RoleRevision,
			RoleName:     speaker.Turn.RoleName,
			RunID:        speaker.Turn.ChildRunID,
			Status:       speaker.Turn.Status,
			Content:      delegatedResultContent(speaker.Turn.Result),
			ErrorCode:    speaker.Turn.ErrorCode,
		})
	}
	return result
}

func speakerGoal(state executionState, index int) string {
	var builder strings.Builder
	builder.WriteString("Original user request:\n")
	builder.WriteString(state.Goal)
	if index > 0 {
		builder.WriteString("\n\nVisible group conversation so far:\n")
		for previous := 0; previous < index; previous++ {
			turn := state.Speakers[previous].Turn
			content := delegatedResultContent(turn.Result)
			if content == "" {
				continue
			}
			name := strings.TrimSpace(turn.RoleName)
			if name == "" {
				name = strings.TrimSpace(turn.SpeakerID)
			}
			builder.WriteString("\n[Speaker: ")
			builder.WriteString(name)
			builder.WriteString("]\n")
			builder.WriteString(content)
			builder.WriteString("\n")
		}
	}
	current := state.Speakers[index].Turn
	name := strings.TrimSpace(current.RoleName)
	if name == "" {
		name = strings.TrimSpace(current.SpeakerID)
	}
	builder.WriteString("\n\nYou are the next visible speaker: ")
	builder.WriteString(name)
	builder.WriteString(". Respond with only the message you want to contribute to the shared conversation.")
	return builder.String()
}

func delegatedResultContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var envelope struct {
		Content string `json:"content"`
	}
	if json.Unmarshal(raw, &envelope) == nil && strings.TrimSpace(envelope.Content) != "" {
		return strings.TrimSpace(envelope.Content)
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}
	return ""
}

func projectSpeakerTurn(turn SpeakerTurn, delegation handoff.Delegation) SpeakerTurn {
	turn.ChildRunID = delegation.ChildRunID
	turn.RoleID = delegation.RoleID
	turn.RoleRevision = delegation.RoleRevision
	turn.RoleName = delegation.RoleName
	turn.Result = append(json.RawMessage(nil), delegation.Result...)
	turn.ErrorCode = strings.TrimSpace(delegation.ErrorCode)
	switch delegation.Status {
	case handoff.StatusQueued:
		turn.Status = SpeakerTurnQueued
	case handoff.StatusRunning:
		turn.Status = SpeakerTurnRunning
	case handoff.StatusCompleted:
		turn.Status = SpeakerTurnCompleted
	case handoff.StatusCancelled:
		turn.Status = SpeakerTurnCancelled
	case handoff.StatusFailed:
		turn.Status = SpeakerTurnFailed
	}
	return turn
}

func validSpeakerConfigs(request StartRequest, maxParticipants, maxUtterances int) bool {
	if len(request.Participants) > maxParticipants || request.MaxUtterances > maxUtterances {
		return false
	}
	configs := speakerConfigByParticipant(request.SpeakerConfigs)
	if len(configs) != len(request.SpeakerConfigs) {
		return false
	}
	for _, config := range request.SpeakerConfigs {
		if strings.TrimSpace(config.ParticipantID) == "" || strings.TrimSpace(config.RoleID) == "" ||
			config.RoleRevision == 0 || strings.TrimSpace(config.RoleName) == "" {
			return false
		}
	}
	ids := request.DirectedSpeakerIDs
	if request.SpeakerPolicy == SpeakerSelector {
		ids = request.CandidateSpeakerIDs
	}
	for _, id := range ids {
		if _, found := configs[id]; !found {
			return false
		}
	}
	return true
}

func normalizeStartRequest(request StartRequest) StartRequest {
	request.ID = strings.TrimSpace(request.ID)
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.Goal = strings.TrimSpace(request.Goal)
	for index := range request.Participants {
		request.Participants[index].ID = strings.TrimSpace(request.Participants[index].ID)
		request.Participants[index].Description = strings.TrimSpace(request.Participants[index].Description)
	}
	for index := range request.SpeakerConfigs {
		request.SpeakerConfigs[index].ParticipantID = strings.TrimSpace(request.SpeakerConfigs[index].ParticipantID)
		request.SpeakerConfigs[index].RoleID = strings.TrimSpace(request.SpeakerConfigs[index].RoleID)
		request.SpeakerConfigs[index].RoleName = strings.TrimSpace(request.SpeakerConfigs[index].RoleName)
		request.SpeakerConfigs[index].Instructions = strings.TrimSpace(request.SpeakerConfigs[index].Instructions)
		request.SpeakerConfigs[index].Model = strings.TrimSpace(request.SpeakerConfigs[index].Model)
		request.SpeakerConfigs[index].ToolKeys = normalizedStrings(request.SpeakerConfigs[index].ToolKeys)
	}
	for index := range request.DirectedSpeakerIDs {
		request.DirectedSpeakerIDs[index] = strings.TrimSpace(request.DirectedSpeakerIDs[index])
	}
	for index := range request.CandidateSpeakerIDs {
		request.CandidateSpeakerIDs[index] = strings.TrimSpace(request.CandidateSpeakerIDs[index])
	}
	request.SelectorModel = strings.TrimSpace(request.SelectorModel)
	request.SelectorModelOptions = append(json.RawMessage(nil), request.SelectorModelOptions...)
	return request
}

func encodeState(state executionState) (json.RawMessage, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	return encoded, nil
}

func decodeState(encoded json.RawMessage) (executionState, error) {
	var state executionState
	if err := json.Unmarshal(encoded, &state); err != nil {
		return executionState{}, errors.Join(ErrInvalidRequest, err)
	}
	if state.SelectorInvocation != nil && !validSelectorInvocation(state.SelectorInvocation) {
		return executionState{}, ErrInvalidRequest
	}
	return state, nil
}

func cloneParticipants(values []Participant) []Participant {
	return append([]Participant(nil), values...)
}

func cloneSpeakerConfigs(values []SpeakerConfig) []SpeakerConfig {
	result := append([]SpeakerConfig(nil), values...)
	for index := range result {
		result[index] = cloneSpeakerConfig(result[index])
	}
	return result
}

func cloneSpeakerConfig(value SpeakerConfig) SpeakerConfig {
	value.ModelOptions = append(json.RawMessage(nil), value.ModelOptions...)
	value.ToolKeys = append([]string(nil), value.ToolKeys...)
	return value
}

func cloneSpeakerTurn(value SpeakerTurn) SpeakerTurn {
	value.Result = append(json.RawMessage(nil), value.Result...)
	return value
}

func speakerConfigByParticipant(values []SpeakerConfig) map[string]SpeakerConfig {
	result := make(map[string]SpeakerConfig, len(values))
	for _, value := range values {
		id := strings.TrimSpace(value.ParticipantID)
		if id == "" {
			continue
		}
		if _, duplicate := result[id]; duplicate {
			return map[string]SpeakerConfig{}
		}
		result[id] = cloneSpeakerConfig(value)
	}
	return result
}

func speakerConfigForID(values []SpeakerConfig, participantID string) (SpeakerConfig, bool) {
	config, ok := speakerConfigByParticipant(values)[strings.TrimSpace(participantID)]
	return config, ok
}

func participantByID(values []Participant) map[string]Participant {
	result := make(map[string]Participant, len(values))
	for _, value := range values {
		result[strings.TrimSpace(value.ID)] = value
	}
	return result
}

func normalizedStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(err))
}

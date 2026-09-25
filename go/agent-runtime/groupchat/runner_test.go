package groupchat_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/groupchat"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

func TestDirectedGroupChatExecutesVisibleSpeakersInOrder(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{
		Store: memory.NewStore(), Clock: groupChatClock{}, IDs: &groupChatIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	delegator := &recordingDelegator{}
	relations, err := runrelation.New(memory.NewRunRelationStore(), groupChatClock{})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := groupchat.NewRunner(groupchat.Dependencies{
		Runtime: runtime, Handoffs: delegator, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := runner.StartRun(t.Context(), directedRequest())
	if err != nil {
		t.Fatalf("start group chat: %v", err)
	}
	if snapshot.Run.Status != kernel.RunStatusCompleted || snapshot.Result == nil {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if len(delegator.goals) != 2 {
		t.Fatalf("goals = %#v", delegator.goals)
	}
	if !strings.Contains(delegator.goals[1], "[Speaker: Researcher]") ||
		!strings.Contains(delegator.goals[1], "answer-researcher") {
		t.Fatalf("second speaker did not receive visible transcript: %q", delegator.goals[1])
	}

	var result groupchat.Result
	if err = json.Unmarshal(snapshot.Result.Content, &result); err != nil {
		t.Fatal(err)
	}
	if result.Kind != groupchat.ResultKind || len(result.Utterances) != 2 {
		t.Fatalf("result = %#v", result)
	}
	if result.Utterances[0].SpeakerID != "participant-researcher" ||
		result.Utterances[1].SpeakerID != "participant-reviewer" {
		t.Fatalf("utterances = %#v", result.Utterances)
	}
	items, err := relations.ListChildren(t.Context(), snapshot.Run.ID)
	if err != nil || len(items) != 2 {
		t.Fatalf("relations=%#v err=%v", items, err)
	}
	for _, item := range items {
		if item.Kind != runrelation.KindGroupChatSpeaker {
			t.Fatalf("relation kind = %q", item.Kind)
		}
	}
}

func TestHandoffGroupChatFollowsExplicitVisibleSignalThenStops(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{
		Store: memory.NewStore(), Clock: groupChatClock{}, IDs: &groupChatIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), groupChatClock{})
	if err != nil {
		t.Fatal(err)
	}
	signals := &recordingVisibleHandoffs{
		signals: []groupchat.VisibleHandoffSignal{{TargetParticipantID: "participant-reviewer"}, {}},
		found:   []bool{true, false},
	}
	delegator := &recordingDelegator{}
	runner, err := groupchat.NewRunner(groupchat.Dependencies{
		Runtime: runtime, Handoffs: delegator, VisibleHandoffs: signals, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.StartRun(t.Context(), handoffRequest())
	if err != nil {
		t.Fatalf("start handoff group chat: %v", err)
	}
	if snapshot.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("status = %q", snapshot.Run.Status)
	}
	view, err := groupchat.ViewState(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if view.SpeakerPolicy != groupchat.SpeakerHandoff ||
		view.TerminationReason != groupchat.TerminationHandoffCompleted ||
		len(view.SpeakerTurns) != 2 ||
		view.SpeakerTurns[0].SpeakerID != "participant-researcher" ||
		view.SpeakerTurns[1].SpeakerID != "participant-reviewer" {
		t.Fatalf("handoff view = %#v", view)
	}
	if len(signals.childRunIDs) != 2 || len(delegator.goals) != 2 {
		t.Fatalf("signals=%#v goals=%#v", signals.childRunIDs, delegator.goals)
	}
	if !strings.Contains(delegator.goals[0], "visible handoff tool") ||
		!strings.Contains(delegator.goals[0], "participant-reviewer: Reviewer") {
		t.Fatalf("handoff instructions missing: %q", delegator.goals[0])
	}
}

func TestHandoffAuthorityOnlyAcceptsCurrentSpeakerCandidate(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{
		Store: memory.NewStore(), Clock: groupChatClock{}, IDs: &groupChatIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), groupChatClock{})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := groupchat.NewRunner(groupchat.Dependencies{
		Runtime: runtime, Handoffs: pendingDelegator{}, VisibleHandoffs: &recordingVisibleHandoffs{}, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.StartRun(t.Context(), handoffRequest())
	if !errors.Is(err, groupchat.ErrSpeakerPending) {
		t.Fatalf("expected speaker pending, got %v", err)
	}
	view, viewErr := groupchat.ViewState(snapshot)
	if viewErr != nil || len(view.SpeakerTurns) != 1 {
		t.Fatalf("view=%#v err=%v", view, viewErr)
	}
	childRunID := view.SpeakerTurns[0].ChildRunID
	if err = runner.ValidateVisibleHandoff(t.Context(), childRunID, "participant-reviewer"); err != nil {
		t.Fatalf("valid handoff rejected: %v", err)
	}
	if err = runner.ValidateVisibleHandoff(t.Context(), childRunID, "participant-researcher"); !errors.Is(err, groupchat.ErrInvalidRequest) {
		t.Fatalf("self handoff error = %v", err)
	}
	if err = runner.ValidateVisibleHandoff(t.Context(), childRunID, "participant-writer"); !errors.Is(err, groupchat.ErrInvalidRequest) {
		t.Fatalf("unknown handoff error = %v", err)
	}
}

func handoffRequest() groupchat.StartRequest {
	request := directedRequest()
	request.ID = "groupchat-handoff-1"
	request.SpeakerPolicy = groupchat.SpeakerHandoff
	request.DirectedSpeakerIDs = nil
	request.CandidateSpeakerIDs = []string{"participant-researcher", "participant-reviewer"}
	request.MaxUtterances = 4
	return request
}

type recordingVisibleHandoffs struct {
	signals     []groupchat.VisibleHandoffSignal
	found       []bool
	childRunIDs []string
}

func (resolver *recordingVisibleHandoffs) ResolveVisibleHandoff(
	_ context.Context,
	childRunID string,
) (groupchat.VisibleHandoffSignal, bool, error) {
	resolver.childRunIDs = append(resolver.childRunIDs, childRunID)
	if len(resolver.signals) == 0 || len(resolver.found) == 0 {
		return groupchat.VisibleHandoffSignal{}, false, nil
	}
	signal := resolver.signals[0]
	found := resolver.found[0]
	resolver.signals = resolver.signals[1:]
	resolver.found = resolver.found[1:]
	return signal, found, nil
}

type pendingDelegator struct{}

func (pendingDelegator) StartOrLoad(
	_ context.Context,
	_ kernel.Snapshot,
	delegation handoff.Delegation,
) (handoff.Delegation, error) {
	delegation.Status = handoff.StatusRunning
	return delegation, handoff.ErrChildPending
}

func TestSelectorGroupChatChoosesBoundedSpeakersAndCompletes(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{
		Store: memory.NewStore(), Clock: groupChatClock{}, IDs: &groupChatIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	delegator := &recordingDelegator{}
	selector := &recordingSelector{responses: []groupchat.SelectorResponse{
		{SpeakerID: "participant-researcher", ResponseID: "selector-1"},
		{SpeakerID: "participant-reviewer", ResponseID: "selector-2"},
		{Complete: true, ResponseID: "selector-3"},
	}}
	runner, err := groupchat.NewRunner(groupchat.Dependencies{
		Runtime: runtime, Handoffs: delegator, Selector: selector,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.StartRun(t.Context(), selectorRequest())
	if err != nil {
		t.Fatalf("start selector group chat: %v", err)
	}
	if snapshot.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("status = %q", snapshot.Run.Status)
	}
	if len(selector.requests) != 3 {
		t.Fatalf("selector requests = %#v", selector.requests)
	}
	if got := selectorCandidateIDs(selector.requests[1]); len(got) != 1 || got[0] != "participant-reviewer" {
		t.Fatalf("second selector candidates = %#v", got)
	}
	if !selector.requests[2].CanComplete || len(selector.requests[2].History) != 2 {
		t.Fatalf("final selector request = %#v", selector.requests[2])
	}
	var result groupchat.Result
	if err = json.Unmarshal(snapshot.Result.Content, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Utterances) != 2 ||
		result.Utterances[0].SpeakerID != "participant-researcher" ||
		result.Utterances[1].SpeakerID != "participant-reviewer" {
		t.Fatalf("selector result = %#v", result)
	}
	view, err := groupchat.ViewState(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if view.SpeakerPolicy != groupchat.SpeakerSelector ||
		view.TerminationReason != groupchat.TerminationSelectorCompleted ||
		view.SelectorInvocation == nil ||
		view.SelectorInvocation.Status != groupchat.SelectorInvocationConsumed {
		t.Fatalf("selector view = %#v", view)
	}
}

func TestSelectorGroupChatStopsAtHardUtteranceLimit(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{
		Store: memory.NewStore(), Clock: groupChatClock{}, IDs: &groupChatIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	selector := &recordingSelector{responses: []groupchat.SelectorResponse{
		{SpeakerID: "participant-researcher"},
		{SpeakerID: "participant-reviewer"},
		{SpeakerID: "participant-researcher"},
	}}
	request := selectorRequest()
	request.MaxUtterances = 2
	runner, err := groupchat.NewRunner(groupchat.Dependencies{
		Runtime: runtime, Handoffs: &recordingDelegator{}, Selector: selector,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	view, err := groupchat.ViewState(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if view.TerminationReason != groupchat.TerminationMaxUtterances || len(view.SpeakerTurns) != 2 ||
		len(selector.requests) != 2 {
		t.Fatalf("limited selector view=%#v requests=%d", view, len(selector.requests))
	}
}

func TestSelectorRetryReusesDurableInvocationIdentity(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{
		Store: memory.NewStore(), Clock: groupChatClock{}, IDs: &groupChatIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	selector := &recordingSelector{
		failures: []error{retryableSelectorError{}},
		responses: []groupchat.SelectorResponse{
			{SpeakerID: "participant-researcher"},
			{Complete: true},
		},
	}
	runner, err := groupchat.NewRunner(groupchat.Dependencies{
		Runtime: runtime, Handoffs: &recordingDelegator{}, Selector: selector,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.StartRun(t.Context(), selectorRequest())
	if !errors.Is(err, groupchat.ErrSelectorPending) {
		t.Fatalf("expected selector pending, got %v", err)
	}
	if len(selector.requests) != 1 {
		t.Fatalf("selector requests = %d", len(selector.requests))
	}
	firstInvocationID := selector.requests[0].InvocationID
	snapshot, err = runner.Resume(t.Context(), snapshot.Run.ID, snapshot.Run.Revision)
	if err != nil {
		t.Fatalf("resume selector: %v", err)
	}
	if snapshot.Run.Status != kernel.RunStatusCompleted || len(selector.requests) != 3 {
		t.Fatalf("resumed selector status=%q requests=%d", snapshot.Run.Status, len(selector.requests))
	}
	if selector.requests[1].InvocationID != firstInvocationID {
		t.Fatalf("selector invocation changed across retry: %q != %q", selector.requests[1].InvocationID, firstInvocationID)
	}
}

func selectorRequest() groupchat.StartRequest {
	request := directedRequest()
	request.ID = "groupchat-selector-1"
	request.SpeakerPolicy = groupchat.SpeakerSelector
	request.DirectedSpeakerIDs = nil
	request.CandidateSpeakerIDs = []string{"participant-researcher", "participant-reviewer"}
	request.SelectorModel = "selector-model"
	request.MaxUtterances = 4
	return request
}

func selectorCandidateIDs(request groupchat.SelectorRequest) []string {
	result := make([]string, 0, len(request.Candidates))
	for _, candidate := range request.Candidates {
		result = append(result, candidate.ID)
	}
	return result
}

type recordingSelector struct {
	requests  []groupchat.SelectorRequest
	responses []groupchat.SelectorResponse
	failures  []error
}

func (selector *recordingSelector) Select(
	_ context.Context,
	request groupchat.SelectorRequest,
) (groupchat.SelectorResponse, error) {
	selector.requests = append(selector.requests, request)
	if len(selector.failures) > 0 {
		err := selector.failures[0]
		selector.failures = selector.failures[1:]
		return groupchat.SelectorResponse{}, err
	}
	if len(selector.responses) == 0 {
		return groupchat.SelectorResponse{}, fmt.Errorf("no selector response")
	}
	response := selector.responses[0]
	selector.responses = selector.responses[1:]
	return response, nil
}

type retryableSelectorError struct{}

func (retryableSelectorError) Error() string   { return "selector temporarily unavailable" }
func (retryableSelectorError) Retryable() bool { return true }

func directedRequest() groupchat.StartRequest {
	return groupchat.StartRequest{
		ID:            "groupchat-1",
		Actor:         kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread:        kernel.ThreadRef{Kind: "conversation", ID: "conversation-1"},
		RequestID:     "request-1",
		Goal:          "Compare the evidence.",
		SpeakerPolicy: groupchat.SpeakerDirected,
		Participants: []groupchat.Participant{
			{ID: "participant-researcher", Description: "Find evidence."},
			{ID: "participant-reviewer", Description: "Review evidence."},
		},
		SpeakerConfigs: []groupchat.SpeakerConfig{
			{ParticipantID: "participant-researcher", AuthorKind: "agent_role", AuthorID: "17", AuthorRevision: "3", AuthorName: "Researcher", MemberID: "participant-researcher"},
			{ParticipantID: "participant-reviewer", AuthorKind: "agent_role", AuthorID: "23", AuthorRevision: "5", AuthorName: "Reviewer", MemberID: "participant-reviewer"},
		},
		DirectedSpeakerIDs: []string{"participant-researcher", "participant-reviewer"},
		MaxUtterances:      2,
	}
}

type recordingDelegator struct {
	goals []string
}

func (delegator *recordingDelegator) StartOrLoad(
	_ context.Context,
	_ kernel.Snapshot,
	delegation handoff.Delegation,
) (handoff.Delegation, error) {
	delegator.goals = append(delegator.goals, delegation.Goal)
	delegation.Status = handoff.StatusCompleted
	delegation.Result = json.RawMessage(fmt.Sprintf(`{"content":"answer-%s"}`, strings.TrimPrefix(delegation.MemberID, "participant-")))
	return delegation, nil
}

type groupChatClock struct{}

func (groupChatClock) Now() time.Time {
	return time.Date(2026, time.September, 24, 0, 0, 0, 0, time.UTC)
}

type groupChatIDs struct {
	next int
}

func (ids *groupChatIDs) NewID(prefix string) (string, error) {
	ids.next++
	return fmt.Sprintf("%s-%d", prefix, ids.next), nil
}

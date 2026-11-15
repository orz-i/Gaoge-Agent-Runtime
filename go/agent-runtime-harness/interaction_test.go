package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestResolveInteractionValidatesBeforeDurableResolution(t *testing.T) {
	t.Parallel()
	fixture := newInteractionResumeFixture(t)
	fixture.handler.failures = 0
	fixture.handler.validationFailures = 1
	interaction := requestHandledInteraction(t, fixture)
	response := json.RawMessage(`{"candidateID":"candidate-1"}`)
	_, err := fixture.runner.ResolveInteraction(t.Context(), fixture.turnID, interaction.ID, harness.ResolveInteractionRequest{Response: response})
	if !errors.Is(err, errInteractionValidationFixture) {
		t.Fatalf("interaction preflight error = %v", err)
	}
	paused, err := fixture.runner.Load(t.Context(), fixture.turnID)
	if err != nil {
		t.Fatal(err)
	}
	got := interactionByKey(t, paused, "handled-choice")
	if got.Status != harness.InteractionWaiting || len(got.Response) != 0 || paused.Turn.Status != harness.TurnWaitingInput || fixture.handler.calls != 0 || fixture.handler.validationCalls != 1 {
		t.Fatalf("invalid response consumed interaction: interaction=%#v turn=%#v handler=%#v", got, paused.Turn, fixture.handler)
	}
}
func TestResolveInteractionHandlesApplicationResponseBeforeResumingOwner(t *testing.T) {
	fixture := newInteractionResumeFixture(t)
	interaction := requestHandledInteraction(t, fixture)
	response := json.RawMessage(`{"candidateID":"candidate-2"}`)
	assertHandlerFailureKeepsOwnerWaiting(t, fixture, interaction, response)
	resolved := resolveHandledInteraction(t, fixture, interaction, response)
	assertResolvedInteraction(t, resolved, response)
	assertInteractionContinuationCalls(t, fixture, interaction)
	assertHandledInteractionReplayIsSideEffectFree(t, fixture, interaction, response)
}

func assertWaitingInteraction(t *testing.T, snapshot harness.Snapshot) harness.Interaction {
	t.Helper()
	if snapshot.Turn.Status != harness.TurnWaitingInput || len(snapshot.Interactions) != 1 {
		t.Fatalf("waiting interaction snapshot = %#v", snapshot)
	}
	interaction := snapshot.Interactions[0]
	if interaction.Status != harness.InteractionWaiting || interaction.InvocationID != featureParentInvocationID(snapshot.Turn.ID) || len(interaction.Response) != 0 {
		t.Fatalf("waiting interaction = %#v", interaction)
	}
	invocation := invocationByID(t, snapshot, featureParentInvocationID(snapshot.Turn.ID))
	if invocation.Status != harness.InvocationWaitingInput {
		t.Fatalf("waiting invocation = %#v", invocation)
	}
	if countInteractionItems(snapshot.Items, harness.ItemWaiting) != 1 {
		t.Fatalf("waiting interaction items = %#v", snapshot.Items)
	}
	return interaction
}
func assertResolvedInteraction(t *testing.T, snapshot harness.Snapshot, response json.RawMessage) {
	t.Helper()
	if snapshot.Turn.Status != harness.TurnRunning || len(snapshot.Interactions) != 1 {
		t.Fatalf("resolved interaction snapshot = %#v", snapshot)
	}
	interaction := snapshot.Interactions[0]
	if interaction.Status != harness.InteractionResolved || string(interaction.Response) != string(response) {
		t.Fatalf("resolved interaction = %#v", interaction)
	}
	invocation := invocationByID(t, snapshot, featureParentInvocationID(snapshot.Turn.ID))
	if invocation.Status != harness.InvocationRunning {
		t.Fatalf("resolved invocation = %#v", invocation)
	}
	if countInteractionItems(snapshot.Items, harness.ItemCompleted) != 1 {
		t.Fatalf("completed interaction items = %#v", snapshot.Items)
	}
}
func invocationByID(t *testing.T, snapshot harness.Snapshot, id string) harness.Invocation {
	t.Helper()
	for _, invocation := range snapshot.Invocations {
		if invocation.ID == id {
			return invocation
		}
	}
	t.Fatalf("missing invocation %s: %#v", id, snapshot.Invocations)
	return harness.Invocation{}
}
func interactionByKey(t *testing.T, snapshot harness.Snapshot, key string) harness.Interaction {
	t.Helper()
	for _, interaction := range snapshot.Interactions {
		if interaction.Key == key {
			return interaction
		}
	}
	t.Fatalf("missing interaction %s: %#v", key, snapshot.Interactions)
	return harness.Interaction{}
}
func countInteractionItems(items []harness.Item, status harness.ItemStatus) int {
	count := 0
	for _, item := range items {
		if item.Kind == harness.ItemInteraction && item.Status == status {
			count++
		}
	}
	return count
}

type recordingInteractionResponseHandler struct {
	calls              int
	interactionID      string
	actorID            string
	failures           int
	validationCalls    int
	validationFailures int
}
type interactionResumeFixture struct {
	runner  *harness.Runner
	turnID  string
	parent  string
	handler *recordingInteractionResponseHandler
	agent   *recordingInteractionAgent
}

func newInteractionResumeFixture(t *testing.T) interactionResumeFixture {
	t.Helper()
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	handler := &recordingInteractionResponseHandler{failures: 1}
	agentFeature := &recordingInteractionAgent{loadingFeatureAgent: loadingFeatureAgent{runtime: runtime}}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: agentFeature, Store: store, Clock: featureInvocationClock{}, Interactions: handler})
	if err != nil {
		t.Fatal(err)
	}
	now := featureInvocationClock{}.Now()
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "interaction-resume-thread"}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "interaction-resume-turn"}
	sessionID, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := harness.TurnID(sessionID, hostTurn)
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
	thread := kernel.ThreadRef{Kind: testThreadKind, ID: hostThread.ID}
	seedFeatureInvocationEnvelope(t, store, sessionID, turnID, hostThread, hostTurn, actor, now)
	_, parentItemID := seedFeatureInvocationParent(t, runtime, store, turnID, actor, thread, now)
	return interactionResumeFixture{runner: runner, turnID: turnID, parent: parentItemID, handler: handler, agent: agentFeature}
}
func requestHandledInteraction(t *testing.T, fixture interactionResumeFixture) harness.Interaction {
	t.Helper()
	waiting, err := fixture.runner.RequestInteraction(t.Context(), fixture.turnID, harness.RequestInteraction{InvocationID: featureParentInvocationID(fixture.turnID), ParentItemID: fixture.parent, ApplicationRef: &harness.HostRef{Kind: "story", ID: "story_1"}, ArtifactRefs: []harness.HostRef{{Kind: "story_candidate_portfolio", ID: "portfolio_1"}}, Key: "handled-choice", Kind: harness.InteractionChoice, Schema: json.RawMessage(`{"type":"object"}`)})
	if err != nil {
		t.Fatal(err)
	}
	return assertWaitingInteraction(t, waiting)
}
func assertHandlerFailureKeepsOwnerWaiting(t *testing.T, fixture interactionResumeFixture, interaction harness.Interaction, response json.RawMessage) {
	t.Helper()
	_, err := fixture.runner.ResolveInteraction(t.Context(), fixture.turnID, interaction.ID, harness.ResolveInteractionRequest{Response: response})
	if !errors.Is(err, errInteractionHandlerFixture) {
		t.Fatalf("first application handler error = %v", err)
	}
	paused, err := fixture.runner.Load(t.Context(), fixture.turnID)
	if err != nil || paused.Turn.Status != harness.TurnWaitingInput || invocationByID(t, paused, featureParentInvocationID(fixture.turnID)).Status != harness.InvocationWaitingInput || interactionByKey(t, paused, "handled-choice").Status != harness.InteractionResolved || fixture.agent.resumeCalls != 0 {
		t.Fatalf("proven no-effect failure failed to release claim safely: turn=%s invocation=%s calls=%d err=%v", paused.Turn.Status, invocationByID(t, paused, featureParentInvocationID(fixture.turnID)).Status, fixture.agent.resumeCalls, err)
	}
}
func resolveHandledInteraction(t *testing.T, fixture interactionResumeFixture, interaction harness.Interaction, response json.RawMessage) harness.Snapshot {
	t.Helper()
	resolved, err := fixture.runner.ResolveInteraction(t.Context(), fixture.turnID, interaction.ID, harness.ResolveInteractionRequest{Response: response})
	if err != nil {
		t.Fatalf("resolve handled interaction: %v", err)
	}
	return resolved
}
func assertInteractionContinuationCalls(t *testing.T, fixture interactionResumeFixture, interaction harness.Interaction) {
	t.Helper()
	if fixture.handler.calls != 2 || fixture.agent.resumeCalls != 1 || fixture.handler.interactionID != interaction.ID {
		t.Fatalf("interaction continuation calls: handler=%d resume=%d interaction=%q", fixture.handler.calls, fixture.agent.resumeCalls, fixture.handler.interactionID)
	}
}
func assertHandledInteractionReplayIsSideEffectFree(t *testing.T, fixture interactionResumeFixture, interaction harness.Interaction, response json.RawMessage) {
	t.Helper()
	if _, err := fixture.runner.ResolveInteraction(t.Context(), fixture.turnID, interaction.ID, harness.ResolveInteractionRequest{Response: response}); err != nil {
		t.Fatalf("replay handled interaction: %v", err)
	}
	if fixture.handler.calls != 2 || fixture.agent.resumeCalls != 1 {
		t.Fatalf("resolved replay repeated side effects: handler=%d resume=%d", fixture.handler.calls, fixture.agent.resumeCalls)
	}
}

var errInteractionHandlerFixture = errors.New("interaction handler fixture")
var errInteractionValidationFixture = errors.New("interaction validation fixture")

func (handler *recordingInteractionResponseHandler) ValidateInteractionResponse(_ context.Context, response harness.InteractionResponseContext) error {
	handler.validationCalls++
	if response.Session.Actor.ActorID == "" || response.Interaction.ApplicationRef == nil || len(response.Interaction.ArtifactRefs) != 1 {
		return errInteractionValidationFixture
	}
	if handler.validationFailures > 0 {
		handler.validationFailures--
		return errInteractionValidationFixture
	}
	return nil
}
func (handler *recordingInteractionResponseHandler) HandleInteractionResponse(_ context.Context, response harness.InteractionResponseContext) (harness.InteractionResponseResult, error) {
	handler.calls++
	handler.interactionID = response.Interaction.ID
	handler.actorID = response.Session.Actor.ActorID
	if handler.failures > 0 {
		handler.failures--
		return harness.InteractionResponseResult{}, errors.Join(harness.ErrInteractionNoEffect, errInteractionHandlerFixture)
	}
	return harness.InteractionResponseResult{}, nil
}

type recordingInteractionAgent struct {
	loadingFeatureAgent
	resumeCalls int
}

func (feature *recordingInteractionAgent) Resume(ctx context.Context, runID string, expectedRevision uint64) (kernel.Snapshot, error) {
	feature.resumeCalls++
	return feature.loadingFeatureAgent.Resume(ctx, runID, expectedRevision)
}

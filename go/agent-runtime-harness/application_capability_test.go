package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge/sdk/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/stretchr/testify/require"
)

var errApplicationCapabilityFixture = errors.New("application capability fixture")

const (
	testApplicationCapabilityKey   = "story.author_decision"
	testApplicationVersion         = "v1"
	testApplicationStoryRefKind    = "story"
	testApplicationPortfolioKind   = "story_candidate_portfolio"
	testApplicationReviewRefKind   = "story_review"
	testApplicationDecisionRefKind = "story_author_decision"
	testApplicationDecisionID      = "decision_1"
)

func TestApplicationCapabilityCompletesWithoutRuntimeRun(t *testing.T) {
	t.Parallel()
	runtime := newFeatureInvocationRuntime(t)
	executor := &applicationCapabilityExecutorFixture{outputRefs: []harness.HostRef{{Kind: testApplicationReviewRefKind, ID: "review_1"}}}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: harness.NewMemoryStore(),
		Clock: featureInvocationClock{}, Applications: executor,
	})
	require.NoError(t, err)
	started, err := runner.StartApplicationTurn(t.Context(), harness.ApplicationTurnRequest{
		StartRequest: harness.StartRequest{
			HostThread: harness.HostRef{Kind: testThreadKind, ID: "application-thread"},
			HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "application-turn"},
			Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
			Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "application-thread"},
			RequestID:  "application-request", Goal: "complete application capability",
		},
		CapabilityKey: testApplicationCapabilityKey, DefinitionVersion: testApplicationVersion,
		Input: json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	invocation, ok := harness.TopLevelInvocation(started)
	require.True(t, ok)
	require.Equal(t, harness.TurnCompleted, started.Turn.Status)
	require.Equal(t, harness.ExecutionApplication, invocation.ExecutionClass)
	require.Equal(t, harness.InvocationCompleted, invocation.Status)
	require.Equal(t, testApplicationVersion, invocation.DefinitionVersion)
	require.Equal(t, []harness.HostRef{{Kind: testApplicationReviewRefKind, ID: "review_1"}}, invocation.OutputRefs)
	require.Equal(t, 1, executor.calls)
	_, loadErr := runtime.Load(t.Context(), invocation.ExecutionRefID)
	require.ErrorIs(t, loadErr, kernel.ErrNotFound)
	refreshed, err := runner.Refresh(t.Context(), started.Turn.ID)
	require.NoError(t, err)
	require.Equal(t, harness.TurnCompleted, refreshed.Turn.Status)
	require.Equal(t, 1, executor.calls)
}

func TestApplicationCapabilityRetryReusesInvocationWithoutRuntimeRun(t *testing.T) {
	t.Parallel()
	runtime := newFeatureInvocationRuntime(t)
	executor := &applicationCapabilityExecutorFixture{
		failures: 1, outputRefs: []harness.HostRef{{Kind: testApplicationReviewRefKind, ID: "review_retry"}},
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: harness.NewMemoryStore(),
		Clock: featureInvocationClock{}, Applications: executor,
	})
	require.NoError(t, err)
	failed, err := runner.StartApplicationTurn(t.Context(), harness.ApplicationTurnRequest{
		StartRequest: harness.StartRequest{
			HostThread: harness.HostRef{Kind: testThreadKind, ID: "application-retry-thread"},
			HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "application-retry-turn"},
			Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
			Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "application-retry-thread"},
			RequestID:  "application-retry-request", Goal: "retry application capability",
		},
		CapabilityKey: testApplicationCapabilityKey, DefinitionVersion: testApplicationVersion, Input: json.RawMessage(`{}`),
	})
	require.ErrorIs(t, err, errApplicationCapabilityFixture)
	first, ok := harness.TopLevelInvocation(failed)
	require.True(t, ok)
	require.Equal(t, harness.TurnFailed, failed.Turn.Status)
	require.Equal(t, harness.InvocationFailed, first.Status)
	require.Equal(t, 1, first.Attempt)
	retried, err := runner.RetryInvocation(t.Context(), first.ID)
	require.NoError(t, err)
	second, ok := harness.TopLevelInvocation(retried)
	require.True(t, ok)
	require.Equal(t, harness.TurnCompleted, retried.Turn.Status)
	require.Equal(t, harness.InvocationCompleted, second.Status)
	require.Equal(t, first.ID, second.ID)
	require.Equal(t, 2, second.Attempt)
	require.NotEqual(t, first.ExecutionRefID, second.ExecutionRefID)
	require.Equal(t, 2, executor.calls)
	_, loadErr := runtime.Load(t.Context(), second.ExecutionRefID)
	require.ErrorIs(t, loadErr, kernel.ErrNotFound)
}

func TestApplicationCapabilityCancelOwnsWaitingLifecycle(t *testing.T) {
	t.Parallel()
	runtime := newFeatureInvocationRuntime(t)
	executor := &applicationCapabilityExecutorFixture{interaction: &harness.ApplicationInteractionRequest{
		ApplicationRef: &harness.HostRef{Kind: testApplicationStoryRefKind, ID: "story_cancel"},
		ArtifactRefs:   []harness.HostRef{{Kind: testApplicationPortfolioKind, ID: "portfolio_cancel"}},
		Key:            testApplicationCapabilityKey, Kind: harness.InteractionChoice, Schema: json.RawMessage(`{"type":"object"}`),
	}}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: harness.NewMemoryStore(),
		Clock: featureInvocationClock{}, Applications: executor, Interactions: applicationInteractionHandlerFixture{},
	})
	require.NoError(t, err)
	waiting, err := runner.StartApplicationTurn(t.Context(), harness.ApplicationTurnRequest{
		StartRequest: harness.StartRequest{
			HostThread: harness.HostRef{Kind: testThreadKind, ID: "application-cancel-thread"},
			HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "application-cancel-turn"},
			Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
			Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "application-cancel-thread"},
			RequestID:  "application-cancel-request", Goal: "cancel application capability",
		},
		CapabilityKey: testApplicationCapabilityKey, DefinitionVersion: testApplicationVersion, Input: json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	require.Equal(t, harness.TurnWaitingInput, waiting.Turn.Status)
	require.Len(t, waiting.Interactions, 1)
	cancelled, err := runner.Cancel(t.Context(), waiting.Turn.ID, "author cancelled")
	require.NoError(t, err)
	invocation, ok := harness.TopLevelInvocation(cancelled)
	require.True(t, ok)
	require.Equal(t, harness.TurnCancelled, cancelled.Turn.Status)
	require.Equal(t, harness.InvocationCancelled, invocation.Status)
	_, err = runner.ResolveInteraction(t.Context(), cancelled.Turn.ID, waiting.Interactions[0].ID, harness.ResolveInteractionRequest{
		Response: json.RawMessage(`{"outcome":"select","selectedCandidateIDs":[]}`),
	})
	require.ErrorIs(t, err, harness.ErrConflict)
}

func TestApplicationCapabilityInteractionCompletesTopLevelTurn(t *testing.T) {
	t.Parallel()
	runtime := newFeatureInvocationRuntime(t)
	executor := &applicationCapabilityExecutorFixture{interaction: &harness.ApplicationInteractionRequest{
		ApplicationRef: &harness.HostRef{Kind: testApplicationStoryRefKind, ID: "story_top_level"},
		ArtifactRefs:   []harness.HostRef{{Kind: testApplicationPortfolioKind, ID: "portfolio_top_level"}},
		Key:            testApplicationCapabilityKey, Kind: harness.InteractionChoice, Schema: json.RawMessage(`{"type":"object"}`),
	}}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: harness.NewMemoryStore(),
		Clock: featureInvocationClock{}, Applications: executor, Interactions: applicationInteractionHandlerFixture{},
	})
	require.NoError(t, err)
	waiting, err := runner.StartApplicationTurn(t.Context(), harness.ApplicationTurnRequest{
		StartRequest: harness.StartRequest{
			HostThread: harness.HostRef{Kind: testThreadKind, ID: "application-top-level-thread"},
			HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "application-top-level-turn"},
			Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
			Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "application-top-level-thread"},
			RequestID:  "application-top-level-request", Goal: "decide application capability",
		},
		CapabilityKey: testApplicationCapabilityKey, DefinitionVersion: testApplicationVersion, Input: json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	require.Equal(t, harness.TurnWaitingInput, waiting.Turn.Status)
	require.Len(t, waiting.Interactions, 1)

	resolved, err := runner.ResolveInteraction(t.Context(), waiting.Turn.ID, waiting.Interactions[0].ID, harness.ResolveInteractionRequest{
		Response: json.RawMessage(`{"outcome":"select","selectedCandidateIDs":["candidate_1"]}`),
	})
	require.NoError(t, err)
	invocation, ok := harness.TopLevelInvocation(resolved)
	require.True(t, ok)
	require.Equal(t, harness.TurnCompleted, resolved.Turn.Status)
	require.Equal(t, harness.InvocationCompleted, invocation.Status)
	require.Equal(t, []harness.HostRef{{Kind: testApplicationDecisionRefKind, ID: testApplicationDecisionID}}, invocation.OutputRefs)
	_, loadErr := runtime.Load(t.Context(), invocation.ExecutionRefID)
	require.ErrorIs(t, loadErr, kernel.ErrNotFound)
}

func TestApplicationInteractionCompletesChildAndResumesRootAgent(t *testing.T) {
	t.Parallel()
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	resumer := &applicationResumeAgent{loadingFeatureAgent: loadingFeatureAgent{runtime: runtime}}
	executor := &applicationCapabilityExecutorFixture{interaction: &harness.ApplicationInteractionRequest{
		ApplicationRef: &harness.HostRef{Kind: testApplicationStoryRefKind, ID: "story_1"},
		ArtifactRefs:   []harness.HostRef{{Kind: testApplicationPortfolioKind, ID: "portfolio_1"}},
		Key:            testApplicationCapabilityKey, Kind: harness.InteractionChoice,
		Schema: json.RawMessage(`{"type":"object"}`),
	}}
	handler := applicationInteractionHandlerFixture{}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: resumer, Store: store, Clock: featureInvocationClock{},
		Applications: executor, Interactions: handler,
	})
	require.NoError(t, err)
	now := featureInvocationClock{}.Now()
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "application-child-thread"}
	sessionID, err := harness.SessionID(hostThread)
	require.NoError(t, err)
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "application-child-turn"}
	turnID, err := harness.TurnID(sessionID, hostTurn)
	require.NoError(t, err)
	actor := kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
	thread := kernel.ThreadRef{Kind: testThreadKind, ID: hostThread.ID}
	seedFeatureInvocationEnvelope(t, store, sessionID, turnID, hostThread, hostTurn, actor, now)
	_, parentItemID := seedFeatureInvocationParent(t, runtime, store, turnID, actor, thread, now)

	waiting, err := runner.StartApplicationInvocation(t.Context(), turnID, harness.ApplicationInvocationRequest{
		ParentItemID: parentItemID, RequestID: "application-child-request", Goal: "ask the author",
		CapabilityKey: testApplicationCapabilityKey, DefinitionVersion: testApplicationVersion, Input: json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	require.Equal(t, harness.TurnWaitingInput, waiting.Turn.Status)
	require.Len(t, waiting.Interactions, 1)
	interaction := waiting.Interactions[0]
	resolved, err := runner.ResolveInteraction(t.Context(), turnID, interaction.ID, harness.ResolveInteractionRequest{
		Response: json.RawMessage(`{"outcome":"select","selectedCandidateIDs":["candidate_1"]}`),
	})
	require.NoError(t, err)
	child := applicationInvocation(t, resolved.Invocations)
	require.Equal(t, harness.TurnRunning, resolved.Turn.Status)
	require.Equal(t, harness.InvocationCompleted, child.Status)
	require.Equal(t, []harness.HostRef{{Kind: testApplicationDecisionRefKind, ID: testApplicationDecisionID}}, child.OutputRefs)
	require.Equal(t, 1, resumer.resumeCalls)
	_, loadErr := runtime.Load(t.Context(), child.ExecutionRefID)
	require.ErrorIs(t, loadErr, kernel.ErrNotFound)
}

func applicationInvocation(t *testing.T, values []harness.Invocation) harness.Invocation {
	t.Helper()
	for _, value := range values {
		if value.ExecutionClass == harness.ExecutionApplication {
			return value
		}
	}
	t.Fatalf("missing application invocation: %#v", values)
	return harness.Invocation{}
}

type applicationCapabilityExecutorFixture struct {
	calls       int
	failures    int
	outputRefs  []harness.HostRef
	interaction *harness.ApplicationInteractionRequest
}

func (executor *applicationCapabilityExecutorFixture) ExecuteApplicationCapability(
	_ context.Context,
	request harness.ApplicationCapabilityRequest,
) (harness.ApplicationCapabilityResult, error) {
	executor.calls++
	if request.Invocation.ExecutionClass != harness.ExecutionApplication || request.Session.Actor.ActorID == "" {
		return harness.ApplicationCapabilityResult{}, harness.ErrInvalidRequest
	}
	if executor.failures > 0 {
		executor.failures--
		return harness.ApplicationCapabilityResult{}, errApplicationCapabilityFixture
	}
	return harness.ApplicationCapabilityResult{
		OutputRefs: append([]harness.HostRef(nil), executor.outputRefs...), Interaction: executor.interaction,
	}, nil
}

type applicationInteractionHandlerFixture struct{}

func (applicationInteractionHandlerFixture) HandleInteractionResponse(
	context.Context,
	harness.InteractionResponseContext,
) (harness.InteractionResponseResult, error) {
	return harness.InteractionResponseResult{OutputRefs: []harness.HostRef{{Kind: testApplicationDecisionRefKind, ID: testApplicationDecisionID}}}, nil
}

type applicationResumeAgent struct {
	loadingFeatureAgent
	resumeCalls int
}

func (agent *applicationResumeAgent) Resume(
	ctx context.Context,
	runID string,
	expectedRevision uint64,
) (kernel.Snapshot, error) {
	agent.resumeCalls++
	return agent.loadingFeatureAgent.Resume(ctx, runID, expectedRevision)
}

var _ harness.ApplicationCapabilityExecutor = (*applicationCapabilityExecutorFixture)(nil)
var _ harness.InteractionResponseHandler = applicationInteractionHandlerFixture{}

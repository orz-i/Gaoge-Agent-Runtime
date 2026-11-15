package harness_test

import (
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestRetryChildApplicationUnderLiveAgentDoesNotRequireApplicationKernelRun(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	executor := &applicationCapabilityExecutorFixture{failures: 1, outputRefs: []harness.HostRef{{Kind: testApplicationReviewRefKind, ID: "retry-child-proof"}}}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store, Clock: featureInvocationClock{}, Applications: executor})
	if err != nil {
		t.Fatal(err)
	}
	now := featureInvocationClock{}.Now()
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "app-child-retry-thread"}
	sessionID, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "app-child-retry-turn"}
	turnID, err := harness.TurnID(sessionID, hostTurn)
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
	thread := kernel.ThreadRef{Kind: testThreadKind, ID: "app-child-retry-thread"}
	seedFeatureInvocationEnvelope(t, store, sessionID, turnID, hostThread, hostTurn, actor, now)
	_, parentItemID := seedFeatureInvocationParent(t, runtime, store, turnID, actor, thread, now)
	failed, err := runner.StartApplicationInvocation(t.Context(), turnID, harness.ApplicationInvocationRequest{ParentItemID: parentItemID, RequestID: "application-retry-child", Goal: "retry child application", CapabilityKey: testApplicationCapabilityKey, DefinitionVersion: testApplicationVersion, Input: json.RawMessage(`{}`)})
	if !errors.Is(err, errApplicationCapabilityFixture) {
		t.Fatalf("first application attempt was not proven failed: %v", err)
	}
	first := applicationInvocation(t, failed.Invocations)
	if first.Status != harness.InvocationFailed || first.Attempt != 1 {
		t.Fatalf("expected failed child Application, got %+v", first)
	}
	retried, err := runner.RetryInvocation(t.Context(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	after := applicationInvocation(t, retried.Invocations)
	if after.Status != harness.InvocationCompleted || after.Attempt != 2 || after.ExecutionRefID == first.ExecutionRefID || executor.calls != 2 {
		t.Fatalf("live child Application retry regressed: %+v calls=%d", after, executor.calls)
	}
}

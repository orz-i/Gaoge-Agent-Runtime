package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type auditPostRetryCASStore struct {
	harness.Store
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *auditPostRetryCASStore) RetryInvocation(
	ctx context.Context, id string, revision uint64, nextRef string, now time.Time,
) (harness.Invocation, error) {
	retried, err := s.Store.RetryInvocation(ctx, id, revision, nextRef, now)
	if err == nil {
		s.once.Do(func() { close(s.entered) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return retried, ctx.Err()
		}
	}
	return retried, err
}

// The root Run can terminate after a child retry's Harness CAS commits but
// before a Host-only Application callback claims its external effect.
// Application execution has no Kernel Run and does not enter the native
// withPhysicalInvocationDispatch boundary: this race needs its own guard.
func TestRetryChildApplicationRejectsParentTerminatedAfterAttemptCAS(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	backing := harness.NewMemoryStore()
	block := &auditPostRetryCASStore{
		Store: backing, entered: make(chan struct{}), release: make(chan struct{}),
	}
	var once sync.Once
	release := func() { once.Do(func() { close(block.release) }) }
	t.Cleanup(release)
	executor := &applicationCapabilityExecutorFixture{
		failures:   1,
		outputRefs: []harness.HostRef{{Kind: testApplicationReviewRefKind, ID: "post-cas-must-not-execute"}},
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Store: block, Clock: featureInvocationClock{}, Applications: executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := featureInvocationClock{}.Now()
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "app-post-retry-cas-thread"}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "app-post-retry-cas-turn"}
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
	seedFeatureInvocationEnvelope(t, backing, sessionID, turnID, hostThread, hostTurn, actor, now)
	parentRunID, parentItemID := seedFeatureInvocationParent(t, runtime, backing, turnID, actor, thread, now)

	before, firstErr := runner.StartApplicationInvocation(t.Context(), turnID, harness.ApplicationInvocationRequest{
		ParentItemID: parentItemID, RequestID: "post-retry-cas-app",
		Goal:          "must stay within live parent",
		CapabilityKey: testApplicationCapabilityKey, DefinitionVersion: testApplicationVersion,
		Input: json.RawMessage(`{}`),
	})
	if !errors.Is(firstErr, errApplicationCapabilityFixture) || executor.calls != 1 {
		t.Fatalf("setup did not produce one definitively failed effect: calls=%d err=%v", executor.calls, firstErr)
	}
	failed := applicationInvocation(t, before.Invocations)
	if failed.Status != harness.InvocationFailed || failed.Attempt != 1 {
		t.Fatalf("unexpected pre-retry status %+v", failed)
	}

	outcomes := make(chan error, 1)
	go func() {
		_, retryErr := runner.RetryInvocation(context.Background(), failed.ID)
		outcomes <- retryErr
	}()
	select {
	case <-block.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("retry never committed and paused after Attempt CAS")
	}
	accepted, err := backing.GetInvocation(t.Context(), failed.ID)
	if err != nil || accepted.Status != harness.InvocationAccepted || accepted.Attempt != 2 {
		t.Fatalf("test did not pause after committed Retry CAS: %+v err=%v", accepted, err)
	}
	parent, err := runtime.Load(t.Context(), parentRunID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Cancel(t.Context(), parentRunID, parent.Run.Revision, "parent terminated after child CAS")
	if err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case retryErr := <-outcomes:
		if !errors.Is(retryErr, harness.ErrConflict) {
			t.Fatalf("retired parent did not fence post-CAS Application retry: %v", retryErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retry still blocked after parent termination")
	}
	if executor.calls != 1 {
		t.Fatalf("external Application callback executed after parent terminated: calls=%d", executor.calls)
	}
	after, err := backing.GetInvocation(t.Context(), failed.ID)
	if err != nil || after.ExecutionRefID != accepted.ExecutionRefID ||
		after.Attempt != 2 || after.Status != harness.InvocationAccepted {
		t.Fatalf("unproven child was mutated or silently retried: %+v err=%v", after, err)
	}
}

// The Application Accepted->Running transition is a durable Host effect claim.
// A parent may terminate after the pre-claim ancestor read. Hold precisely
// after the claim commits to verify that the final callback admission still
// rejects the now-dead parent and records a definitive no-effect outcome.
type auditPostApplicationClaimStore struct {
	harness.Store
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *auditPostApplicationClaimStore) UpdateInvocation(
	ctx context.Context, value harness.Invocation, expected uint64,
) (harness.Invocation, error) {
	updated, err := s.Store.UpdateInvocation(ctx, value, expected)
	if err == nil && value.Status == harness.InvocationRunning {
		s.once.Do(func() { close(s.entered) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return updated, ctx.Err()
		}
	}
	return updated, err
}

func TestRetryChildApplicationRejectsParentTerminatedAfterEffectClaim(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	backing := harness.NewMemoryStore()
	executor := &applicationCapabilityExecutorFixture{
		failures:   1,
		outputRefs: []harness.HostRef{{Kind: testApplicationReviewRefKind, ID: "post-claim-must-not-execute"}},
	}
	makeRunner := func(store harness.Store) *harness.Runner {
		t.Helper()
		runner, err := harness.NewRunner(harness.Dependencies{
			Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
			Store: store, Clock: featureInvocationClock{}, Applications: executor,
		})
		if err != nil {
			t.Fatal(err)
		}
		return runner
	}
	now := featureInvocationClock{}.Now()
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "app-post-claim-thread"}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "app-post-claim-turn"}
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
	seedFeatureInvocationEnvelope(t, backing, sessionID, turnID, hostThread, hostTurn, actor, now)
	parentRunID, parentItemID := seedFeatureInvocationParent(t, runtime, backing, turnID, actor, thread, now)

	before, firstErr := makeRunner(backing).StartApplicationInvocation(t.Context(), turnID, harness.ApplicationInvocationRequest{
		ParentItemID: parentItemID, RequestID: "post-app-claim-child",
		Goal:          "must not invoke when parent expires after claim",
		CapabilityKey: testApplicationCapabilityKey, DefinitionVersion: testApplicationVersion,
		Input: json.RawMessage(`{}`),
	})
	if !errors.Is(firstErr, errApplicationCapabilityFixture) || executor.calls != 1 {
		t.Fatalf("setup did not produce one failed no-effect Application: calls=%d err=%v", executor.calls, firstErr)
	}
	failed := applicationInvocation(t, before.Invocations)
	gate := &auditPostApplicationClaimStore{
		Store: backing, entered: make(chan struct{}), release: make(chan struct{}),
	}
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	t.Cleanup(release)

	retryRunner := makeRunner(gate)
	outcomes := make(chan error, 1)
	go func() {
		_, retryErr := retryRunner.RetryInvocation(context.Background(), failed.ID)
		outcomes <- retryErr
	}()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Application retry never committed its effect claim")
	}
	claimed, err := backing.GetInvocation(t.Context(), failed.ID)
	if err != nil || claimed.Status != harness.InvocationRunning || claimed.Attempt != 2 {
		t.Fatalf("test did not pause after committed App effect claim: %+v err=%v", claimed, err)
	}
	parent, err := runtime.Load(t.Context(), parentRunID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Cancel(t.Context(), parentRunID, parent.Run.Revision, "parent terminated after child effect claim")
	if err != nil {
		t.Fatal(err)
	}
	release()

	select {
	case retryErr := <-outcomes:
		if !errors.Is(retryErr, harness.ErrConflict) {
			t.Fatalf("parent termination after effect claim was not fenced: %v", retryErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Application post-claim retry hung after parent termination")
	}
	if executor.calls != 1 {
		t.Fatalf("Host effect executed after parent terminated post claim: calls=%d", executor.calls)
	}
	current, err := backing.GetInvocation(t.Context(), failed.ID)
	if err != nil || current.Attempt != 2 || current.ExecutionRefID != claimed.ExecutionRefID ||
		current.Status != harness.InvocationFailed {
		t.Fatalf("pre-effect rejection failed to persist known no-effect result: %+v err=%v", current, err)
	}
}

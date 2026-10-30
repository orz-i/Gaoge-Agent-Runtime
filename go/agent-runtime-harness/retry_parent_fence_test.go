package harness_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

// A child whose Kernel parent has already terminated may have a still-running
// product Turn. Retry must check the parent *before* allocating a new durable
// child attempt, not leave it permanently stuck Accepted when the start
// preflight later discovers that no parent execution can own it.
func TestRetryChildAfterParentTerminalDoesNotRotate(t *testing.T) {
	runner, turnID, parentItemID, _, parentRunID, store, runtime := newFeatureInvocationHarness(t)
	started, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: parentItemID, RequestID: "terminated-parent-child",
		Goal: "retry after parent", Mode: team.ExecutionSequential,
		Members: []team.Member{{ID: retryTeamMemberID, Goal: retryTeamMemberGoal}},
	})
	if err != nil {
		t.Fatal(err)
	}
	failed := markInvocationFailed(t, store, teamChildInvocation(t, started))
	parent, err := runtime.Load(t.Context(), parentRunID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Cancel(t.Context(), parentRunID, parent.Run.Revision, "parent ended")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.GetTurn(t.Context(), turnID)
	if err != nil || turn.Status != harness.TurnRunning {
		t.Fatalf("test requires an active Turn with terminal Kernel parent: turn=%+v err=%v", turn, err)
	}
	_, err = runner.RetryInvocation(t.Context(), failed.ID)
	if !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("retired parent unexpectedly authorized a retry: %v", err)
	}
	after, err := store.GetInvocation(t.Context(), failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != harness.InvocationFailed || after.Attempt != failed.Attempt ||
		after.Revision != failed.Revision || after.ExecutionRefID != failed.ExecutionRefID {
		t.Fatalf("rejected retry allocated an unreachable Accepted attempt: before=%+v after=%+v", failed, after)
	}
}

// A descendant may be attached to a completed child Invocation item while
// the root Agent Run is still running. Root-only parent checks miss this
// terminal owner and rotate an unreachable retry attempt before the physical
// dispatch grant finally discovers the terminated middle ancestor.
func TestRetryNestedChildAfterOwnerInvocationCompletedDoesNotRotate(t *testing.T) {
	runner, turnID, rootItemID, _, _, store, _ := newFeatureInvocationHarness(t)
	parentSnapshot, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: rootItemID, RequestID: "finished-owner",
		Goal: "finish parent", Mode: team.ExecutionSequential,
		Members: []team.Member{{ID: retryTeamMemberID, Goal: retryTeamMemberGoal}},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := teamChildInvocation(t, parentSnapshot)
	if parent.Status != harness.InvocationCompleted {
		t.Fatalf("fixture parent did not finish: %+v", parent)
	}
	var ownerItem harness.Item
	for _, item := range parentSnapshot.Items {
		if item.Kind == harness.ItemInvocation && item.InvocationID == parent.ID &&
			item.Status == harness.ItemStarted {
			ownerItem = item
			break
		}
	}
	if ownerItem.ID == "" {
		t.Fatalf("missing parent invocation item: %+v", parentSnapshot.Items)
	}
	payload := json.RawMessage(`{"goal":"retry grandchild when parent is finished"}`)
	digest := sha256.Sum256(payload)
	now := featureInvocationClock{}.Now()
	invID := "hiv_grandchild_terminal_owner"
	failed := harness.Invocation{
		ID: invID, TurnID: turnID, ParentItemID: ownerItem.ID,
		CapabilityKey: harness.CapabilityWorkflow, DefinitionVersion: harness.RuntimeCapabilityVersion,
		ExecutionClass: harness.ExecutionWorkflow, Input: payload, InputHash: hex.EncodeToString(digest[:]),
		ExecutionRefID: harness.CapabilityExecutionRefID(invID),
		Status:         harness.InvocationFailed, Attempt: 1, Revision: 1, ErrorCode: "fixture.failed",
		OutputRefs: []harness.HostRef{}, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err = store.CreateInvocation(t.Context(), failed); err != nil {
		t.Fatal(err)
	}
	_, err = runner.RetryInvocation(t.Context(), invID)
	if !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("completed intermediate Invocation authorized grandchild retry: %v", err)
	}
	after, err := store.GetInvocation(t.Context(), invID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Attempt != 1 || after.Status != harness.InvocationFailed ||
		after.Revision != 1 || after.ExecutionRefID != failed.ExecutionRefID {
		t.Fatalf("terminal parent rotated grandchild into orphan Accepted attempt: %+v", after)
	}
}

// Application capabilities do not have a Kernel Run. Do not accidentally
// require one when protecting retry ancestry: a child Application of a live
// Agent remains retryable through its original Host-selected executor.
func TestRetryChildApplicationUnderLiveAgentDoesNotRequireApplicationKernelRun(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	executor := &applicationCapabilityExecutorFixture{
		failures: 1, outputRefs: []harness.HostRef{{Kind: testApplicationReviewRefKind, ID: "retry-child-proof"}},
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Store: store, Clock: featureInvocationClock{}, Applications: executor,
	})
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
	failed, err := runner.StartApplicationInvocation(t.Context(), turnID, harness.ApplicationInvocationRequest{
		ParentItemID: parentItemID, RequestID: "application-retry-child",
		Goal:          "retry child application",
		CapabilityKey: testApplicationCapabilityKey, DefinitionVersion: testApplicationVersion,
		Input: json.RawMessage(`{}`),
	})
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
	if after.Status != harness.InvocationCompleted || after.Attempt != 2 ||
		after.ExecutionRefID == first.ExecutionRefID || executor.calls != 2 {
		t.Fatalf("live child Application retry regressed: %+v calls=%d", after, executor.calls)
	}
}

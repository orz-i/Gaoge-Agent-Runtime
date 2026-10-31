package harness_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

// A Tool fact from Attempt 1 is immutable. Its owner Invocation ID stays
// stable across root Retry, but its RunID must not grant a new child in
// Attempt 2. This exercises the public child entry, including physical Start.
func TestPR82OldToolItemCannotAdmitNewChildAfterRootRetry(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	now := featureInvocationClock{}.Now()
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "stale-tool-parent-thread"}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "stale-tool-parent-turn"}
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

	const oldRunID = "stale-tool-attempt-one"
	first, err := runtime.Create(t.Context(), kernel.CreateRequest{
		ID: oldRunID, Kind: agent.RunKind, Actor: actor, Thread: thread,
		RequestID: "old", Goal: "old", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	rootInput, err := json.Marshal(struct {
		Goal   string           `json:"goal"`
		Actor  kernel.ActorRef  `json:"actor"`
		Thread kernel.ThreadRef `json:"thread"`
	}{Goal: "root", Actor: actor, Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(rootInput)
	root := harness.Invocation{
		ID: "stale-tool-root", TurnID: turnID, CapabilityKey: harness.CapabilityAgent,
		DefinitionVersion: harness.RuntimeCapabilityVersion, ExecutionClass: harness.ExecutionAgent,
		Input: rootInput, InputHash: hex.EncodeToString(hash[:]), ExecutionRefID: oldRunID,
		Status: harness.InvocationRunning, Attempt: 1, Revision: 1, OutputRefs: []harness.HostRef{},
		CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.CreateInvocation(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	const oldToolID = "stale-root-tool-item"
	if _, _, err := store.AppendItem(t.Context(), harness.Item{
		ID: oldToolID, TurnID: turnID, Kind: harness.ItemTool, Status: harness.ItemCompleted,
		InvocationID: root.ID, RunID: oldRunID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Cancel(t.Context(), oldRunID, first.Run.Revision, "old attempt done"); err != nil {
		t.Fatal(err)
	}
	root.Status = harness.InvocationFailed
	failed, err := store.UpdateInvocation(t.Context(), root, root.Revision)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.GetTurn(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	turn.Status = harness.TurnFailed
	if _, err := store.UpdateTurn(t.Context(), turn, turn.Revision); err != nil {
		t.Fatal(err)
	}
	retried, err := store.RetryInvocation(t.Context(), root.ID, failed.Revision, "stale-tool-attempt-two", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Create(t.Context(), kernel.CreateRequest{
		ID: retried.ExecutionRefID, Kind: agent.RunKind, Actor: actor, Thread: thread,
		RequestID: "new", Goal: "new", State: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	retried.Status = harness.InvocationRunning
	if _, err := store.UpdateInvocation(t.Context(), retried, retried.Revision); err != nil {
		t.Fatal(err)
	}
	teams := &auditCountedTeamStart{completedTeamFeature: completedTeamFeature{runtime: runtime}}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store,
		Clock: featureInvocationClock{}, Teams: teams, Relations: newFeatureInvocationRelations(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: oldToolID, RequestID: "late-old-attempt-tool-call",
		Goal: "must not dispatch", Mode: team.ExecutionSequential,
		Members: []team.Member{{ID: "member", Goal: "task"}},
	})
	if !errors.Is(err, harness.ErrConflict) || teams.calls.Load() != 0 {
		t.Fatalf("old Tool Item authorized new Attempt: err=%v physical starts=%d", err, teams.calls.Load())
	}
}

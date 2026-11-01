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

// Full public StartTeamInvocation path: the child returns Running and its
// physical Start grant is already settled. Root Retry must still refuse to
// rotate the Attempt while that native child continues executing.
func TestPR82RootRetryCannotOverlapLiveNativeChild(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	now := featureInvocationClock{}.Now()
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "pr82-live-native-thread"}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "pr82-live-native-turn"}
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
	rootRun, err := runtime.Create(t.Context(), kernel.CreateRequest{
		ID: "pr82-native-root-first", Kind: agent.RunKind, Actor: actor, Thread: thread,
		RequestID: "root-first", Goal: "delegate", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(struct {
		Goal   string           `json:"goal"`
		Actor  kernel.ActorRef  `json:"actor"`
		Thread kernel.ThreadRef `json:"thread"`
	}{Goal: "delegate", Actor: actor, Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(input)
	root := harness.Invocation{
		ID: "pr82-native-root", TurnID: turnID, CapabilityKey: harness.CapabilityAgent,
		DefinitionVersion: harness.RuntimeCapabilityVersion, ExecutionClass: harness.ExecutionAgent,
		Input: input, InputHash: hex.EncodeToString(hash[:]), ExecutionRefID: rootRun.Run.ID,
		Status: harness.InvocationRunning, Attempt: 1, Revision: 1, OutputRefs: []harness.HostRef{},
		CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.CreateInvocation(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	const parentID = "pr82-current-root-tool"
	if _, _, err := store.AppendItem(t.Context(), harness.Item{
		ID: parentID, TurnID: turnID, Kind: harness.ItemTool, Status: harness.ItemCompleted,
		InvocationID: root.ID, RunID: root.ExecutionRefID,
	}); err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store,
		Clock: featureInvocationClock{}, Teams: auditRunningTeamFeature{runtime: runtime},
		Relations: newFeatureInvocationRelations(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: parentID, RequestID: "live-child", Goal: "continues asynchronously",
		Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "task"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	childID, err := harness.InvocationID(turnID, parentID, harness.CapabilityTeam, "live-child")
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.GetInvocation(t.Context(), childID)
	if err != nil || child.Status != harness.InvocationRunning {
		t.Fatalf("native child is not running: %+v %v", child, err)
	}
	childRun, err := runtime.Load(t.Context(), child.ExecutionRefID)
	if err != nil || childRun.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("child Kernel execution was not running: %+v %v", childRun.Run, err)
	}
	items, err := store.ListItems(t.Context(), turnID, 0, 500)
	if err != nil || harness.PendingExecutionDispatch(items, "") {
		t.Fatalf("child's physical Start grant was not settled: %v", err)
	}
	if _, err := runtime.Cancel(t.Context(), root.ExecutionRefID, rootRun.Run.Revision, "first root ended"); err != nil {
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
	if _, err := store.RetryInvocation(t.Context(), root.ID, failed.Revision, "pr82-native-root-second", now); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("root retry overlapped still-running native child: %v", err)
	}
	unchanged, err := runtime.Load(t.Context(), child.ExecutionRefID)
	if err != nil || unchanged.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("child changed after blocked retry: %+v %v", unchanged.Run, err)
	}
}

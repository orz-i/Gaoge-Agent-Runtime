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
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// Permanent regression from independent merge audit: one
// legitimate tenant-B physical root is indexed under tenant-A RunRelation.
// Ownership of a local Run is not ownership by this requested Turn.
func TestIndependentCrossTurnCapabilityRelationCannotExposeOtherTenantRoot(t *testing.T) {
	runner, turnA, _, relations, runA, store, runtime := newFeatureInvocationHarness(t)
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "cross-turn-tenant-B-thread"}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "cross-turn-tenant-B-turn"}
	sessionB, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	turnB, err := harness.TurnID(sessionB, hostTurn)
	if err != nil {
		t.Fatal(err)
	}
	actorB := kernel.ActorRef{TenantID: "tenant-B", ActorID: "B-user"}
	threadB := kernel.ThreadRef{Kind: testThreadKind, ID: hostThread.ID}
	seedFeatureInvocationEnvelope(t, store, sessionB, turnB, hostThread, hostTurn, actorB, featureInvocationClock{}.Now())
	requestID := "tenant-B-original-request"
	invocationID, err := harness.InvocationID(turnB, "", harness.CapabilityAgent, requestID)
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(struct {
		Goal   string           `json:"goal"`
		Actor  kernel.ActorRef  `json:"actor"`
		Thread kernel.ThreadRef `json:"thread"`
	}{Goal: "tenant-B-private-task", Actor: actorB, Thread: threadB})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(input)
	foreignID := "tenant-B-physical-root-run"
	_, fresh, err := store.CreateInvocation(t.Context(), harness.Invocation{
		ID: invocationID, TurnID: turnB, CapabilityKey: harness.CapabilityAgent,
		DefinitionVersion: harness.RuntimeCapabilityVersion, ExecutionClass: harness.ExecutionAgent,
		Input: input, InputHash: hex.EncodeToString(digest[:]), ExecutionRefID: foreignID,
		Status: harness.InvocationRunning, Attempt: 1, Revision: 1,
		CreatedAt: featureInvocationClock{}.Now(), UpdatedAt: featureInvocationClock{}.Now(),
	})
	if err != nil || !fresh {
		t.Fatalf("native-B Invocation: fresh=%v err=%v", fresh, err)
	}
	created, err := runtime.Create(t.Context(), kernel.CreateRequest{
		ID: foreignID, Kind: agent.RunKind, Actor: actorB, Thread: threadB,
		RequestID: requestID, Goal: "tenant-B-private-task", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Apply(t.Context(), created.Run.ID, created.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: created.State,
		Result: &kernel.Result{ContentType: "text/plain", Content: json.RawMessage(`"TENANT-B-PRIVATE-RESULT"`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The relation is the route claimed under A; the physical Run exists under
	// a genuine, separately frozen B root. It must NOT authorize B's output on A.
	_, err = relations.Ensure(t.Context(), runrelation.Draft{
		ParentRunID: runA, ChildRunID: foreignID,
		Kind: runrelation.KindCapability, OwnerNodeID: invocationID,
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := runner.Load(t.Context(), turnA)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range view.Subtasks {
		if task.RunID == foreignID && (task.Goal == "tenant-B-private-task" ||
			string(task.Result) == `"TENANT-B-PRIVATE-RESULT"` || task.Status == "completed") {
			t.Fatalf("CROSS-TURN PHYSICAL DISCLOSURE: turnA=%s turnB=%s task=%+v", turnA, turnB, task)
		}
	}
}

func TestIndependentCrossTurnCapabilityRelationCannotCancelOtherTenantRoot(t *testing.T) {
	runner, turnA, _, relations, runA, store, runtime := newFeatureInvocationHarness(t)
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "cross-turn-tenant-B-thread"}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "cross-turn-tenant-B-turn"}
	sessionB, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	turnB, err := harness.TurnID(sessionB, hostTurn)
	if err != nil {
		t.Fatal(err)
	}
	actorB := kernel.ActorRef{TenantID: "tenant-B", ActorID: "B-user"}
	threadB := kernel.ThreadRef{Kind: testThreadKind, ID: hostThread.ID}
	seedFeatureInvocationEnvelope(t, store, sessionB, turnB, hostThread, hostTurn, actorB, featureInvocationClock{}.Now())
	requestID := "tenant-B-original-request"
	invocationID, err := harness.InvocationID(turnB, "", harness.CapabilityAgent, requestID)
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(struct {
		Goal   string           `json:"goal"`
		Actor  kernel.ActorRef  `json:"actor"`
		Thread kernel.ThreadRef `json:"thread"`
	}{Goal: "tenant-B-private-task", Actor: actorB, Thread: threadB})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(input)
	foreignID := "tenant-B-physical-root-run"
	_, fresh, err := store.CreateInvocation(t.Context(), harness.Invocation{
		ID: invocationID, TurnID: turnB, CapabilityKey: harness.CapabilityAgent,
		DefinitionVersion: harness.RuntimeCapabilityVersion, ExecutionClass: harness.ExecutionAgent,
		Input: input, InputHash: hex.EncodeToString(digest[:]), ExecutionRefID: foreignID,
		Status: harness.InvocationRunning, Attempt: 1, Revision: 1,
		CreatedAt: featureInvocationClock{}.Now(), UpdatedAt: featureInvocationClock{}.Now(),
	})
	if err != nil || !fresh {
		t.Fatalf("native-B Invocation: fresh=%v err=%v", fresh, err)
	}
	created, err := runtime.Create(t.Context(), kernel.CreateRequest{
		ID: foreignID, Kind: agent.RunKind, Actor: actorB, Thread: threadB,
		RequestID: requestID, Goal: "tenant-B-private-task", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = created // Tenant B retains a Running physical root before cancellation.
	// The relation is the route claimed under A; the physical Run exists under
	// a genuine, separately frozen B root. It must NOT authorize B's output on A.
	_, err = relations.Ensure(t.Context(), runrelation.Draft{
		ParentRunID: runA, ChildRunID: foreignID,
		Kind: runrelation.KindCapability, OwnerNodeID: invocationID,
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := runner.Load(t.Context(), turnA)
	if err != nil {
		t.Fatal(err)
	}
	taskID := ""
	for _, task := range view.Subtasks {
		if task.RunID == foreignID {
			taskID = task.ID
		}
	}
	if taskID == "" {
		t.Fatalf("foreign B subtask not visible in A Turn")
	}
	_, cancelErr := runner.CancelSubtask(t.Context(), turnA, taskID, "cancel apparent A subtask")
	actual, loadErr := runtime.Load(t.Context(), foreignID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if actual.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("CROSS-TURN FOREIGN PHYSICAL CANCEL: ownerB=%s turnA=%s foreignStatus=%s cancelErr=%v", turnB, turnA, actual.Run.Status, cancelErr)
	}
	if !errors.Is(cancelErr, harness.ErrConflict) && !errors.Is(cancelErr, harness.ErrRuntimeCancellationUnverified) {
		t.Fatalf("cross-turn cancellation not denied: %v", cancelErr)
	}
	items, err := store.ListItems(t.Context(), turnA, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if harness.ActiveCancellationFence(items) {
		t.Fatal("foreign relation claimed an A cancellation intent")
	}
}

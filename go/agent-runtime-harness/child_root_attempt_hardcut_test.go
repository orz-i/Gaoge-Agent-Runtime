package harness_test

import (
	"context"
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

// Simulate a Store ACK lost after an Accepted child has committed but before
// its Item fact, relation, durable dispatch grant or physical Kernel Run.
type interruptedChildCreateStore struct {
	harness.Store
	interrupted bool
}

func (store *interruptedChildCreateStore) CreateInvocation(ctx context.Context, value harness.Invocation) (harness.Invocation, bool, error) {
	saved, fresh, err := store.Store.CreateInvocation(ctx, value)
	if err == nil && fresh && value.ParentItemID != "" && !store.interrupted {
		store.interrupted = true
		return harness.Invocation{}, false, errors.New("injected lost accepted-child ACK")
	}
	return saved, fresh, err
}

// A later root Retry must not confer authority to a child accepted under
// the earlier root Attempt. Test both an ownerless Host parent and an Item
// that explicitly names the root's Invocation/Run. Both slipped through the
// previous final liveness check when the new root Run was already live.
func TestChildAdmissionAndRetryCannotRebindToNewRootAttempt(t *testing.T) {
	for _, rootOwned := range []bool{false, true} {
		name := "ownerless-item"
		if rootOwned {
			name = "root-owned-item"
		}
		t.Run(name, func(t *testing.T) {
			runtime := newFeatureInvocationRuntime(t)
			store := harness.NewMemoryStore()
			now := featureInvocationClock{}.Now()
			hostThread := harness.HostRef{Kind: testThreadKind, ID: "root-attempt-thread"}
			hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "root-attempt-turn"}
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
			const oldRunID = "root-run-attempt-1"
			firstRun, err := runtime.Create(t.Context(), kernel.CreateRequest{
				ID: oldRunID, Kind: agent.RunKind, Actor: actor, Thread: thread,
				RequestID: "root-1", Goal: "first attempt", State: json.RawMessage(`{}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			rootInput, err := json.Marshal(struct {
				Goal   string           `json:"goal"`
				Actor  kernel.ActorRef  `json:"actor"`
				Thread kernel.ThreadRef `json:"thread"`
			}{Goal: "root goal", Actor: actor, Thread: thread})
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(rootInput)
			root := harness.Invocation{
				ID: "root-invocation", TurnID: turnID, CapabilityKey: harness.CapabilityAgent,
				DefinitionVersion: harness.RuntimeCapabilityVersion, ExecutionClass: harness.ExecutionAgent,
				Input: rootInput, InputHash: hex.EncodeToString(hash[:]), ExecutionRefID: oldRunID,
				Status: harness.InvocationRunning, Attempt: 1, Revision: 1, OutputRefs: []harness.HostRef{},
				CreatedAt: now, UpdatedAt: now,
			}
			if _, _, err := store.CreateInvocation(t.Context(), root); err != nil {
				t.Fatal(err)
			}
			parent := harness.Item{ID: "selected-parent", TurnID: turnID, Kind: harness.ItemArtifact, Status: harness.ItemCompleted}
			if rootOwned {
				parent.Kind, parent.Status = harness.ItemInvocation, harness.ItemStarted
				parent.InvocationID, parent.RunID = root.ID, oldRunID
			}
			if _, _, err := store.AppendItem(t.Context(), parent); err != nil {
				t.Fatal(err)
			}

			relations := newFeatureInvocationRelations(t)
			feature := &auditCountedTeamStart{completedTeamFeature: completedTeamFeature{runtime: runtime}}
			newRunner := func(backing harness.Store) *harness.Runner {
				t.Helper()
				runner, err := harness.NewRunner(harness.Dependencies{
					Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: backing,
					Clock: featureInvocationClock{}, Teams: feature, Relations: relations,
				})
				if err != nil {
					t.Fatal(err)
				}
				return runner
			}
			request := harness.TeamInvocationRequest{
				ParentItemID: parent.ID, RequestID: "same-child-request", Goal: "must remain in first attempt",
				Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "task"}},
			}
			interrupted := &interruptedChildCreateStore{Store: store}
			if _, err := newRunner(interrupted).StartTeamInvocation(t.Context(), turnID, request); err == nil || !interrupted.interrupted {
				t.Fatalf("expected a committed child with lost ACK: %v", err)
			}
			childID, err := harness.InvocationID(turnID, parent.ID, harness.CapabilityTeam, request.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			child, err := store.GetInvocation(t.Context(), childID)
			if err != nil || child.Status != harness.InvocationAccepted || child.Attempt != 1 {
				t.Fatalf("child was not durably accepted: %+v %v", child, err)
			}

			if _, err := runtime.Cancel(t.Context(), oldRunID, firstRun.Run.Revision, "first root attempt ended"); err != nil {
				t.Fatal(err)
			}
			root.Status, root.ErrorCode = harness.InvocationFailed, "root.attempt_ended"
			failedRoot, err := store.UpdateInvocation(t.Context(), root, root.Revision)
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
			const newRunID = "root-run-attempt-2"
			retriedRoot, err := store.RetryInvocation(t.Context(), root.ID, failedRoot.Revision, newRunID, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runtime.Create(t.Context(), kernel.CreateRequest{
				ID: newRunID, Kind: agent.RunKind, Actor: actor, Thread: thread,
				RequestID: "root-2", Goal: "second attempt", State: json.RawMessage(`{}`),
			}); err != nil {
				t.Fatal(err)
			}
			retriedRoot.Status = harness.InvocationRunning
			if _, err := store.UpdateInvocation(t.Context(), retriedRoot, retriedRoot.Revision); err != nil {
				t.Fatal(err)
			}

			runner := newRunner(store)
			if _, err := runner.StartTeamInvocation(t.Context(), turnID, request); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("old child's replay reattached to root attempt 2: %v", err)
			}
			if feature.calls.Load() != 0 {
				t.Fatalf("replay invoked Team StartRun after root Attempt rotated: %d", feature.calls.Load())
			}
			after, err := store.GetInvocation(t.Context(), childID)
			if err != nil || after.Status != harness.InvocationAccepted || after.Attempt != 1 ||
				after.Revision != child.Revision || after.ExecutionRefID != child.ExecutionRefID {
				t.Fatalf("rejected replay mutated first-attempt child: %+v %v", after, err)
			}

			// Retry is a distinct path. The old child may become explicitly
			// failed, but must still not rotate a Run under root Attempt 2.
			after.Status = harness.InvocationFailed
			after.ErrorCode = "child.failed"
			failedChild, err := store.UpdateInvocation(t.Context(), after, after.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runner.RetryInvocation(t.Context(), childID); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("old child retry reattached to new root Attempt: %v", err)
			}
			stillFailed, err := store.GetInvocation(t.Context(), childID)
			if err != nil || stillFailed.Revision != failedChild.Revision || stillFailed.Attempt != 1 ||
				stillFailed.ExecutionRefID != child.ExecutionRefID || feature.calls.Load() != 0 {
				t.Fatalf("old child retry had side effects: %+v %v", stillFailed, err)
			}
		})
	}
}

// Applications have no Kernel Run of their own. They may admit Application
// children, but must not mint a native Team run without a Kernel parent.
func TestApplicationRootAdmitsApplicationChildWithoutKernelRun(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	now := featureInvocationClock{}.Now()
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "app-root-child-thread"}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "app-root-child-turn"}
	sessionID, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := harness.TurnID(sessionID, hostTurn)
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
	seedFeatureInvocationEnvelope(t, store, sessionID, turnID, hostThread, hostTurn, actor, now)
	if _, _, err := store.CreateInvocation(t.Context(), harness.Invocation{
		ID: "app-root", TurnID: turnID, CapabilityKey: "app.root", ExecutionClass: harness.ExecutionApplication,
		ExecutionRefID: "host-only-application", Status: harness.InvocationRunning,
		Attempt: 1, Revision: 1, OutputRefs: []harness.HostRef{}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	const parentID = "ownerless-application-parent"
	if _, _, err := store.AppendItem(t.Context(), harness.Item{
		ID: parentID, TurnID: turnID, Kind: harness.ItemArtifact, Status: harness.ItemCompleted,
	}); err != nil {
		t.Fatal(err)
	}
	executor := &applicationCapabilityExecutorFixture{}
	teams := &auditCountedTeamStart{completedTeamFeature: completedTeamFeature{runtime: runtime}}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store,
		Clock: featureInvocationClock{}, Applications: executor, Teams: teams,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.StartApplicationInvocation(t.Context(), turnID, harness.ApplicationInvocationRequest{
		ParentItemID: parentID, RequestID: "app-under-app", Goal: "valid child",
		CapabilityKey: testApplicationCapabilityKey, DefinitionVersion: testApplicationVersion, Input: json.RawMessage(`{}`),
	})
	if err != nil || executor.calls != 1 {
		t.Fatalf("live Application root rejected valid child: calls=%d err=%v", executor.calls, err)
	}
	childID, err := harness.InvocationID(turnID, parentID, testApplicationCapabilityKey, "app-under-app")
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.GetInvocation(t.Context(), childID)
	if err != nil || child.Status != harness.InvocationCompleted {
		t.Fatalf("Application child did not complete: %+v %v", child, err)
	}
	if _, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: parentID, RequestID: "native-child-under-host-root",
		Goal: "native child must be refused", Mode: team.ExecutionSequential,
		Members: []team.Member{{ID: "member", Goal: "task"}},
	}); !errors.Is(err, harness.ErrConflict) || teams.calls.Load() != 0 {
		t.Fatalf("Host-only root admitted a native Kernel child: calls=%d err=%v", teams.calls.Load(), err)
	}
}

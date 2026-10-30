package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

var errNativeCancellationRead = errors.New("injected native Kernel control pre-Store read error")

type nativeCancellationReadFaultStore struct {
	*memory.Store
	armed        atomic.Bool
	reads        atomic.Int32
	failAt       atomic.Int32
	controlCalls atomic.Int32
}

func (store *nativeCancellationReadFaultStore) Load(ctx context.Context, runID string) (kernel.Snapshot, error) {
	if store.armed.Load() && store.reads.Add(1) == store.failAt.Load() {
		return kernel.Snapshot{}, errNativeCancellationRead
	}
	return store.Store.Load(ctx, runID)
}

func (store *nativeCancellationReadFaultStore) ApplyControl(
	ctx context.Context, runID string, expectedRevision uint64,
	guard kernel.ControlGuard, change kernel.StoreMutation,
) (kernel.Snapshot, bool, error) {
	store.controlCalls.Add(1)
	return store.Store.ApplyControl(ctx, runID, expectedRevision, guard, change)
}

// Create a first-party Harness Turn with an already-admitted root Agent Run.
// Child tests use the same persisted parent and real RunRelation reader.
func newNativeCancellationSeededHarness(
	t *testing.T, runtime *kernel.Runtime,
) (*harness.Runner, *harness.MemoryStore, *runrelation.Registry, string, string, string) {
	t.Helper()
	store := harness.NewMemoryStore()
	relations := newFeatureInvocationRelations(t)
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Teams: auditRunningTeamFeature{runtime: runtime},
		Store: store, Clock: featureInvocationClock{}, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := featureInvocationClock{}.Now()
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "native-cancel-proof"}
	sessionID, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "native-cancel-proof"}
	turnID, err := harness.TurnID(sessionID, hostTurn)
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
	thread := kernel.ThreadRef{Kind: testThreadKind, ID: hostThread.ID}
	seedFeatureInvocationEnvelope(t, store, sessionID, turnID, hostThread, hostTurn, actor, now)
	rootRunID, parentItemID := seedFeatureInvocationParent(t, runtime, store, turnID, actor, thread, now)
	return runner, store, relations, turnID, parentItemID, rootRunID
}

// A direct Kernel proof is different from a generic ErrConflict. When no
// shared-budget or Interaction mutation occurred, a failed cancellation can
// retire only its own intent for any recognized nonterminal Kernel state.
func TestNativeCancellationPreStoreReadAllowsRetryForRootAndChildren(t *testing.T) {
	for _, operation := range []string{"Cancel", "CancelInvocation", "CancelSubtask"} {
		for _, initial := range []kernel.RunStatus{
			kernel.RunStatusRunning, kernel.RunStatusPausedBudget, kernel.RunStatusWaitingInput,
		} {
			t.Run(operation+"/"+string(initial), func(t *testing.T) {
				kernelStore := &nativeCancellationReadFaultStore{Store: memory.NewStore()}
				runtime, err := kernel.New(kernel.Dependencies{Store: kernelStore})
				if err != nil {
					t.Fatal(err)
				}
				runner, store, _, turnID, parentItemID, rootRunID := newNativeCancellationSeededHarness(t, runtime)
				runID := rootRunID
				var invocationID, taskID string
				if operation != "Cancel" {
					started, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
						ParentItemID: parentItemID, RequestID: "native-cancel-" + operation,
						Goal: "child cancellation", Mode: team.ExecutionSequential,
						Members: []team.Member{{ID: "worker", Goal: "task"}},
					})
					if err != nil {
						t.Fatal(err)
					}
					child := teamChildInvocation(t, started)
					invocationID = child.ID
					runID = child.ExecutionRefID
					for _, task := range started.Subtasks {
						if task.RunID == runID {
							taskID = task.ID
							break
						}
					}
					if taskID == "" {
						t.Fatal("missing child Subtask")
					}
				}
				current, err := runtime.Load(t.Context(), runID)
				if err != nil {
					t.Fatal(err)
				}
				if initial != kernel.RunStatusRunning {
					mutation := kernel.Mutation{Status: initial, State: current.State}
					if initial == kernel.RunStatusPausedBudget {
						mutation.ErrorCode = "audit.paused_budget"
					} else {
						mutation.Checkpoint = &kernel.Checkpoint{
							ID: "checkpoint-native-cancel", Kind: "audit.wait",
							Status: kernel.CheckpointPending, Payload: json.RawMessage("{}"),
							CreatedAt: featureInvocationClock{}.Now(),
						}
					}
					if _, err := runtime.Apply(t.Context(), runID, current.Run.Revision, mutation); err != nil {
						t.Fatalf("seed nonterminal Run status: %v", err)
					}
				}
				cancel := func() (harness.Snapshot, error) {
					switch operation {
					case "CancelInvocation":
						return runner.CancelInvocation(t.Context(), invocationID, "native no-effect cancellation")
					case "CancelSubtask":
						return runner.CancelSubtask(t.Context(), turnID, taskID, "native no-effect cancellation")
					default:
						return runner.Cancel(t.Context(), turnID, "native no-effect cancellation")
					}
				}
				// CancelSubtask reads its Subtask's Kernel Run during lookup,
				// before cancelRuntimeRun's own two pre-Store reads.
				failAt := int32(3)
				if operation == "CancelSubtask" {
					failAt = 4
				}
				kernelStore.failAt.Store(failAt)
				kernelStore.armed.Store(true)
				before := kernelStore.controlCalls.Load()
				_, firstErr := cancel()
				kernelStore.armed.Store(false)
				if !errors.Is(firstErr, errNativeCancellationRead) ||
					!kernel.IsControlNoEffect(firstErr) || kernelStore.controlCalls.Load() != before {
					t.Fatalf("native Kernel did not stop before Store CAS: reads=%d want=%d calls=%d->%d err=%v",
						kernelStore.reads.Load(), failAt, before, kernelStore.controlCalls.Load(), firstErr)
				}
				observed, err := runtime.Load(t.Context(), runID)
				if err != nil || observed.Run.Status != initial {
					t.Fatalf("no-effect cancellation changed Run: status=%s want=%s err=%v", observed.Run.Status, initial, err)
				}
				items, err := store.ListItems(t.Context(), turnID, 0, 500)
				if err != nil {
					t.Fatal(err)
				}
				if harness.ActiveCancellationFence(items) {
					t.Fatal("proven no-effect cancellation left its admission intent open")
				}
				_, secondErr := cancel()
				final, loadErr := runtime.Load(t.Context(), runID)
				if secondErr != nil || loadErr != nil || final.Run.Status != kernel.RunStatusCancelled {
					t.Fatalf("safe retry failed: error=%v load=%v status=%s", secondErr, loadErr, final.Run.Status)
				}
			})
		}
	}
}

// A first-party Kernel Store CAS response is outcome-unknown even if its
// returned error happens to wrap kernel.ErrConflict. A committed cancellation
// with a lost acknowledgment must NOT be retried as a no-effect conflict.
type nativeCancelLostConflictACKStore struct {
	*memory.Store
	armed atomic.Bool
	calls atomic.Int32
}

func (store *nativeCancelLostConflictACKStore) ApplyControl(
	ctx context.Context, runID string, revision uint64,
	guard kernel.ControlGuard, change kernel.StoreMutation,
) (kernel.Snapshot, bool, error) {
	store.calls.Add(1)
	snapshot, revoked, err := store.Store.ApplyControl(ctx, runID, revision, guard, change)
	if err == nil && store.armed.CompareAndSwap(true, false) {
		return kernel.Snapshot{}, revoked, kernel.ErrConflict
	}
	return snapshot, revoked, err
}

func TestNativeCancellationDoesNotReleaseUncertainStoreConflict(t *testing.T) {
	kernelStore := &nativeCancelLostConflictACKStore{Store: memory.NewStore()}
	runtime, err := kernel.New(kernel.Dependencies{Store: kernelStore})
	if err != nil {
		t.Fatal(err)
	}
	runner, store, _, turnID, _, rootRunID := newNativeCancellationSeededHarness(t, runtime)
	kernelStore.armed.Store(true)
	_, firstErr := runner.Cancel(t.Context(), turnID, "lost native Kernel Store CAS acknowledgment")
	if !errors.Is(firstErr, kernel.ErrConflict) || kernel.IsControlNoEffect(firstErr) {
		t.Fatalf("Store error was misclassified as native pre-Store no-effect: %v", firstErr)
	}
	if calls := kernelStore.calls.Load(); calls != 1 {
		t.Fatalf("uncertain Store outcome was retried: calls=%d", calls)
	}
	run, err := runtime.Load(t.Context(), rootRunID)
	if err != nil || run.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("fixture did not commit cancellation before losing ACK: status=%s err=%v", run.Run.Status, err)
	}
	items, err := store.ListItems(t.Context(), turnID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !harness.ActiveCancellationFence(items) {
		t.Fatal("uncertain Store CAS return prematurely retired cancellation intent")
	}
}

// A revision drift observed by Kernel.ApplyControl, before Store CAS, must
// retain its direct provenance after the bounded native retry budget is
// exhausted. Unlike a Store conflict, this is a proven no-effect condition.
type nativeCancelReadRevisionDriftStore struct {
	*memory.Store
	armed        atomic.Bool
	reads        atomic.Int32
	controlCalls atomic.Int32
}

func (store *nativeCancelReadRevisionDriftStore) Load(ctx context.Context, runID string) (kernel.Snapshot, error) {
	snapshot, err := store.Store.Load(ctx, runID)
	if err == nil && store.armed.Load() && store.reads.Add(1)%3 == 0 {
		snapshot.Run.Revision++
	}
	return snapshot, err
}

func (store *nativeCancelReadRevisionDriftStore) ApplyControl(
	ctx context.Context, runID string, revision uint64,
	guard kernel.ControlGuard, mutation kernel.StoreMutation,
) (kernel.Snapshot, bool, error) {
	store.controlCalls.Add(1)
	return store.Store.ApplyControl(ctx, runID, revision, guard, mutation)
}

func TestExhaustedNativePreStoreRevisionConflictsReleaseIntent(t *testing.T) {
	kernelStore := &nativeCancelReadRevisionDriftStore{Store: memory.NewStore()}
	runtime, err := kernel.New(kernel.Dependencies{Store: kernelStore})
	if err != nil {
		t.Fatal(err)
	}
	runner, store, _, turnID, _, rootRunID := newNativeCancellationSeededHarness(t, runtime)
	kernelStore.armed.Store(true)
	_, firstErr := runner.Cancel(t.Context(), turnID, "bounded pre-Store revision conflicts")
	kernelStore.armed.Store(false)
	if !errors.Is(firstErr, kernel.ErrConflict) ||
		!kernel.IsControlNoEffect(firstErr) || kernelStore.controlCalls.Load() != 0 ||
		kernelStore.reads.Load() < 24 {
		t.Fatalf("exhausted retries lost direct Kernel proof: error=%v reads=%d CAS=%d",
			firstErr, kernelStore.reads.Load(), kernelStore.controlCalls.Load())
	}
	items, err := store.ListItems(t.Context(), turnID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if harness.ActiveCancellationFence(items) {
		t.Fatal("exhausted pre-Store conflicts left a no-effect intent open")
	}
	_, retryErr := runner.Cancel(t.Context(), turnID, "retry after pre-Store conflict")
	final, err := runtime.Load(t.Context(), rootRunID)
	if retryErr != nil || err != nil || final.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("safe retry after native conflict exhaustion failed: error=%v load=%v status=%s",
			retryErr, err, final.Run.Status)
	}
}

// A waiting Harness Interaction may have its Invocation status changed by
// cancellation admission. Even with a trusted target-only Kernel no-effect
// proof, releasing the Turn-wide intent would discard that CAS mutation.
func TestNativeNoEffectDoesNotReleaseInteractionMutatedCancellation(t *testing.T) {
	kernelStore := &nativeCancellationReadFaultStore{Store: memory.NewStore()}
	runtime, err := kernel.New(kernel.Dependencies{Store: kernelStore})
	if err != nil {
		t.Fatal(err)
	}
	runner, store, _, turnID, parentItemID, rootRunID := newNativeCancellationSeededHarness(t, runtime)
	waiting, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
		InvocationID: "hiv_parent", ParentItemID: parentItemID,
		Key: "cancel-intent-audit-interaction", Kind: harness.InteractionChoice,
		Schema: json.RawMessage("{}"),
	})
	if err != nil || len(waiting.Interactions) == 0 {
		t.Fatalf("could not seed a waiting Interaction: turn=%s err=%v", waiting.Turn.Status, err)
	}
	kernelStore.failAt.Store(3)
	kernelStore.armed.Store(true)
	_, cancelErr := runner.Cancel(t.Context(), turnID, "native no-effect after Interaction CAS")
	kernelStore.armed.Store(false)
	if !errors.Is(cancelErr, errNativeCancellationRead) || kernelStore.controlCalls.Load() != 0 {
		t.Fatalf("failed to inject before Kernel Store CAS: error=%v reads=%d CAS=%d",
			cancelErr, kernelStore.reads.Load(), kernelStore.controlCalls.Load())
	}
	items, err := store.ListItems(t.Context(), turnID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !harness.ActiveCancellationFence(items) {
		t.Fatal("Interaction mutation was incorrectly treated as no-effect")
	}
	owner, err := store.GetInvocation(t.Context(), "hiv_parent")
	if err != nil || owner.Status != harness.InvocationCancelled {
		t.Fatalf("Interaction CAS not committed: status=%s err=%v", owner.Status, err)
	}
	run, err := runtime.Load(t.Context(), rootRunID)
	if err != nil || run.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("physical Kernel should remain running: status=%s err=%v", run.Run.Status, err)
	}
	if _, err := runner.Cancel(t.Context(), turnID, "retry unsafe partial cancellation"); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("partial Interaction cancellation should remain fenced: %v", err)
	}
}

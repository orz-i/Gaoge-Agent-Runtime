package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

// Simulate the crash-window race on the original PR #82 Head: the indexed
// parent Item was accepted when the root Run was live, but the root Kernel
// Run terminates after the child dispatch grant commits and before StartRun.
// Unlike an Invocation-owned parent, an ordinary same-Turn Item has no
// InvocationID, so its ancestor walk alone cannot certify root liveness.
type terminateRootAfterChildGrantStore struct {
	harness.Store
	runtime   *kernel.Runtime
	rootRunID string
	injected  bool
	cancelErr error
}

func (store *terminateRootAfterChildGrantStore) AppendItem(
	ctx context.Context, item harness.Item,
) (harness.Item, bool, error) {
	saved, fresh, err := store.Store.AppendItem(ctx, item)
	if err != nil || !fresh || store.injected ||
		item.Kind != harness.ItemDiagnostic || item.Status != harness.ItemStarted ||
		item.ParentItemID != harness.ExecutionDispatchParentItemID {
		return saved, fresh, err
	}
	store.injected = true
	state, loadErr := store.runtime.Load(ctx, store.rootRunID)
	if loadErr != nil {
		store.cancelErr = loadErr
		return saved, fresh, loadErr
	}
	terminated, cancelErr := store.runtime.Cancel(ctx, store.rootRunID, state.Run.Revision, "root cancelled after durable child grant")
	if cancelErr == nil && terminated.Run.Status != kernel.RunStatusCancelled {
		cancelErr = harness.ErrConflict
	}
	store.cancelErr = cancelErr
	return saved, fresh, err
}

type auditCountedTeamStart struct {
	completedTeamFeature
	calls atomic.Int32
}

func (feature *auditCountedTeamStart) StartRun(ctx context.Context, request team.StartRequest) (kernel.Snapshot, error) {
	feature.calls.Add(1)
	return feature.completedTeamFeature.StartRun(ctx, request)
}

func TestP1ChildDispatchRejectsOwnerlessParentAfterRootKernelTermination(t *testing.T) {
	_, turnID, _, relations, rootRunID, backing, runtime := newFeatureInvocationHarness(t)
	parentItemID := "ownerless-parent-item"
	if _, _, err := backing.AppendItem(t.Context(), harness.Item{
		ID: parentItemID, TurnID: turnID, Kind: harness.ItemArtifact, Status: harness.ItemCompleted,
	}); err != nil {
		t.Fatal(err)
	}
	store := &terminateRootAfterChildGrantStore{Store: backing, runtime: runtime, rootRunID: rootRunID}
	feature := &auditCountedTeamStart{completedTeamFeature: completedTeamFeature{runtime: runtime}}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store,
		Clock: featureInvocationClock{}, Teams: feature, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, startErr := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: parentItemID, RequestID: "ownerless-parent-root-fence",
		Goal: "must not start once root is cancelled", Mode: team.ExecutionSequential,
		Members: []team.Member{{ID: "member", Goal: "task"}},
	})
	if !store.injected || store.cancelErr != nil {
		t.Fatalf("root cancellation was not injected after durable grant: injected=%v err=%v", store.injected, store.cancelErr)
	}
	if feature.calls.Load() != 0 {
		t.Fatalf("P1: physical child StartRun executed after authoritative root Run termination: calls=%d err=%v", feature.calls.Load(), startErr)
	}
	if !errors.Is(startErr, harness.ErrConflict) {
		t.Fatalf("root-terminated physical dispatch was not rejected: %v", startErr)
	}
	invocations, err := backing.ListInvocations(t.Context(), turnID)
	if err != nil || len(invocations) != 2 {
		t.Fatalf("unexpected root/child Invocation records: invocations=%#v err=%v", invocations, err)
	}
	for _, invocation := range invocations {
		if invocation.ParentItemID == "" {
			continue
		}
		if _, loadErr := runtime.Load(t.Context(), invocation.ExecutionRefID); !errors.Is(loadErr, kernel.ErrNotFound) {
			t.Fatalf("new child Kernel Run was created after root cancellation: %v", loadErr)
		}
	}
	items, err := backing.ListItems(t.Context(), turnID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if harness.PendingExecutionDispatch(items, "") {
		t.Fatal("pre-effect rejection left an unresolved physical dispatch grant")
	}
}

// A live root still authorizes an explicitly selected child, even when
// the parent is an ordinary Host Item with no Invocation owner. The P1 fix
// must not turn every ownerless parent into an unconditional rejection.
func TestOwnerlessParentWithLiveRootCanStartChild(t *testing.T) {
	_, turnID, _, relations, _, backing, runtime := newFeatureInvocationHarness(t)
	if _, _, err := backing.AppendItem(t.Context(), harness.Item{
		ID: "live-ownerless-parent", TurnID: turnID,
		Kind: harness.ItemArtifact, Status: harness.ItemCompleted,
	}); err != nil {
		t.Fatal(err)
	}
	feature := &auditCountedTeamStart{completedTeamFeature: completedTeamFeature{runtime: runtime}}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: backing,
		Clock: featureInvocationClock{}, Teams: feature, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: "live-ownerless-parent", RequestID: "live-ownerless-child", Goal: "legitimate child",
		Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "task"}},
	})
	if err != nil || feature.calls.Load() != 1 {
		t.Fatalf("live root did not authorize exactly one child Start: calls=%d err=%v", feature.calls.Load(), err)
	}
}

// Application does not use the Kernel-backed child dispatch grant; its
// Accepted->Running CAS is the external-effect claim. Its second guard after
// that claim must also validate the root Run, including ownerless parent Items.
type terminateRootAfterApplicationClaimStore struct {
	harness.Store
	runtime   *kernel.Runtime
	rootRunID string
	injected  bool
	cancelErr error
}

func (store *terminateRootAfterApplicationClaimStore) UpdateInvocation(
	ctx context.Context, value harness.Invocation, expected uint64,
) (harness.Invocation, error) {
	updated, err := store.Store.UpdateInvocation(ctx, value, expected)
	if err != nil || store.injected ||
		value.ParentItemID == "" || value.ExecutionClass != harness.ExecutionApplication ||
		value.Status != harness.InvocationRunning {
		return updated, err
	}
	store.injected = true
	state, loadErr := store.runtime.Load(ctx, store.rootRunID)
	if loadErr != nil {
		store.cancelErr = loadErr
		return updated, loadErr
	}
	terminated, cancelErr := store.runtime.Cancel(ctx, store.rootRunID, state.Run.Revision, "root cancelled after Application effect claim")
	if cancelErr == nil && terminated.Run.Status != kernel.RunStatusCancelled {
		cancelErr = harness.ErrConflict
	}
	store.cancelErr = cancelErr
	return updated, nil
}

func TestP1ApplicationChildDoesNotEffectAfterOwnerlessParentRootTermination(t *testing.T) {
	_, turnID, _, _, rootRunID, backing, runtime := newFeatureInvocationHarness(t)
	parentItemID := "ownerless-application-parent"
	if _, _, err := backing.AppendItem(t.Context(), harness.Item{
		ID: parentItemID, TurnID: turnID, Kind: harness.ItemArtifact, Status: harness.ItemCompleted,
	}); err != nil {
		t.Fatal(err)
	}
	store := &terminateRootAfterApplicationClaimStore{Store: backing, runtime: runtime, rootRunID: rootRunID}
	executor := &applicationCapabilityExecutorFixture{}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Store: store, Clock: featureInvocationClock{}, Applications: executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, startErr := runner.StartApplicationInvocation(t.Context(), turnID, harness.ApplicationInvocationRequest{
		ParentItemID: parentItemID, RequestID: "ownerless-post-app-claim",
		Goal:          "do not invoke external executor after root termination",
		CapabilityKey: testApplicationCapabilityKey, DefinitionVersion: testApplicationVersion,
		Input: json.RawMessage(`{}`),
	})
	if !store.injected || store.cancelErr != nil {
		t.Fatalf("root cancellation not injected after Application claim: injected=%v err=%v", store.injected, store.cancelErr)
	}
	if executor.calls != 0 {
		t.Fatalf("P1: external Application effect executed after root termination: calls=%d err=%v", executor.calls, startErr)
	}
	if !errors.Is(startErr, harness.ErrConflict) {
		t.Fatalf("Application effect should be rejected after root termination: %v", startErr)
	}
	invocations, err := backing.ListInvocations(t.Context(), turnID)
	if err != nil || len(invocations) != 2 {
		t.Fatalf("unexpected root/child Invocation records: invocations=%#v err=%v", invocations, err)
	}
	for _, invocation := range invocations {
		if invocation.ParentItemID != "" && invocation.Status != harness.InvocationFailed {
			t.Fatalf("claimed child Application was not marked definite no-effect failure: %#v", invocation)
		}
	}
}

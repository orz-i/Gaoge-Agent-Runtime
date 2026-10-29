package harness_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

type recheckCancelBarrier struct {
	runtime *kernel.Runtime
	target  string
	entered chan struct{}
	release chan struct{}
	hit     atomic.Bool
}

func (b *recheckCancelBarrier) Cancel(ctx context.Context, id string, rev uint64, reason string) (kernel.Snapshot, error) {
	if id == b.target && b.hit.CompareAndSwap(false, true) {
		close(b.entered)
		<-b.release
	}
	return b.runtime.Cancel(ctx, id, rev, reason)
}

type recheckCountingTeam struct {
	auditRunningTeamFeature
	calls atomic.Int32
}

func (c *recheckCountingTeam) Resume(ctx context.Context, runID string, rev uint64) (kernel.Snapshot, error) {
	c.calls.Add(1)
	return c.runtime.Load(ctx, runID)
}

func TestReauditCancellationIntentDoesNotFenceExistingResume(t *testing.T) {
	for _, mode := range []string{"ResumeTurn", "ResumeInvocation"} {
		t.Run(mode, func(t *testing.T) {
			_, turnID, parentItemID, relations, rootID, store, runtime := newFeatureInvocationHarness(t)
			agentFeature := &recordingInteractionAgent{loadingFeatureAgent: loadingFeatureAgent{runtime: runtime}}
			teamFeature := &recheckCountingTeam{auditRunningTeamFeature: auditRunningTeamFeature{runtime: runtime}}
			barrier := &recheckCancelBarrier{runtime: runtime, target: rootID, entered: make(chan struct{}), release: make(chan struct{})}
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: agentFeature, Teams: teamFeature,
				Store: store, Clock: featureInvocationClock{}, Relations: relations, Cancellation: barrier,
			})
			if err != nil {
				t.Fatal(err)
			}
			var childID string
			if mode == "ResumeInvocation" {
				started, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
					ParentItemID: parentItemID, RequestID: "existing-child-resume", Goal: "team",
					Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "task"}},
				})
				if err != nil {
					t.Fatal(err)
				}
				for _, inv := range started.Invocations {
					if inv.ExecutionClass == harness.ExecutionTeam && inv.ParentItemID != "" {
						childID = inv.ID
						break
					}
				}
				if childID == "" {
					t.Fatal("missing child")
				}
			}
			cancelDone := make(chan error, 1)
			go func() { _, e := runner.Cancel(context.Background(), turnID, "cancel turn"); cancelDone <- e }()
			<-barrier.entered // Durable cancellation intent has committed.
			var resumed harness.Snapshot
			var resumeErr error
			if mode == "ResumeTurn" {
				resumed, resumeErr = runner.ResumeTurn(t.Context(), turnID)
			} else {
				resumed, resumeErr = runner.ResumeInvocation(t.Context(), childID)
			}
			calls := agentFeature.resumeCalls
			if mode == "ResumeInvocation" {
				calls = int(teamFeature.calls.Load())
			}
			close(barrier.release)
			cancelErr := <-cancelDone
			if calls != 0 {
				t.Fatalf("REPRO: %s entered physical Resume %d times while cancellation intent was committed; resumeErr=%v turn=%s cancelErr=%v",
					mode, calls, resumeErr, resumed.Turn.Status, cancelErr)
			}
			if resumeErr == nil || !errors.Is(resumeErr, harness.ErrConflict) && !errors.Is(resumeErr, harness.ErrInteractionOutcomeUnknown) {
				t.Fatalf("resume during cancellation should fail closed: err=%v", resumeErr)
			}
		})
	}
}

type recheckAcceptedBeforeCancelStore struct {
	harness.Store
	armed   atomic.Bool
	stopped atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (s *recheckAcceptedBeforeCancelStore) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	if s.armed.Load() && item.Kind == harness.ItemInvocation && item.InvocationID != interactionParentInvocationID && s.stopped.CompareAndSwap(false, true) {
		close(s.entered)
		<-s.release
	}
	return s.Store.AppendItem(ctx, item)
}

type recheckCountingTeamStart struct {
	auditRunningTeamFeature
	starts atomic.Int32
}

func (f *recheckCountingTeamStart) StartRun(ctx context.Context, req team.StartRequest) (kernel.Snapshot, error) {
	f.starts.Add(1)
	return f.auditRunningTeamFeature.StartRun(ctx, req)
}
func TestReauditAcceptedChildStartsAfterRootCancellation(t *testing.T) {
	_, turnID, parentItemID, relations, rootRunID, backing, runtime := newFeatureInvocationHarness(t)
	store := &recheckAcceptedBeforeCancelStore{Store: backing, entered: make(chan struct{}), release: make(chan struct{})}
	feature := &recheckCountingTeamStart{auditRunningTeamFeature: auditRunningTeamFeature{runtime: runtime}}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Teams: feature,
		Store: store, Clock: featureInvocationClock{}, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	store.armed.Store(true)
	started := make(chan error, 1)
	go func() {
		_, startErr := runner.StartTeamInvocation(context.Background(), turnID, harness.TeamInvocationRequest{
			ParentItemID: parentItemID, RequestID: "accepted-before-cancel", Goal: "delayed team effect",
			Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "task"}},
		})
		started <- startErr
	}()
	<-store.entered // Invocation.Accepted committed; no Run/RunRelation yet.
	cancelSnapshot, cancelErr := runner.Cancel(t.Context(), turnID, "cancel accepted child")
	rootBeforeRelease, rootErr := runtime.Load(t.Context(), rootRunID)
	close(store.release)
	startErr := <-started
	invocations, listErr := store.ListInvocations(t.Context(), turnID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	var child harness.Invocation
	for _, inv := range invocations {
		if inv.ExecutionClass == harness.ExecutionTeam && inv.ParentItemID != "" {
			child = inv
			break
		}
	}
	if child.ID == "" {
		t.Fatal("missing accepted child")
	}
	childRun, loadErr := runtime.Load(t.Context(), child.ExecutionRefID)
	if feature.starts.Load() != 0 && loadErr == nil && childRun.Run.Status == kernel.RunStatusRunning {
		t.Fatalf("REPRO: Team.StartRun issued a NEW Kernel Run after root cancellation intent/Kernel cancellation: root=%s cancelErr=%v turn=%s childStatus=%s featureStarts=%d startErr=%v",
			rootBeforeRelease.Run.Status, cancelErr, cancelSnapshot.Turn.Status, childRun.Run.Status, feature.starts.Load(), startErr)
	}
	if rootErr != nil {
		t.Fatal(rootErr)
	}
}

type recheckConflictThenCancel struct {
	runtime *kernel.Runtime
	calls   atomic.Int32
}

func (c *recheckConflictThenCancel) Cancel(ctx context.Context, id string, rev uint64, reason string) (kernel.Snapshot, error) {
	if c.calls.Add(1) <= 12 {
		return kernel.Snapshot{}, kernel.ErrConflict
	}
	return c.runtime.Cancel(ctx, id, rev, reason)
}
func TestReauditKernelCASConflictLeavesCancellationIntentPermanentlyBlockingSafeRetry(t *testing.T) {
	_, turnID, _, relations, rootRunID, store, runtime := newFeatureInvocationHarness(t)
	canceller := &recheckConflictThenCancel{runtime: runtime}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store,
		Clock: featureInvocationClock{}, Relations: relations, Cancellation: canceller,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, firstErr := runner.Cancel(t.Context(), turnID, "cancel with repeated CAS conflict")
	if !errors.Is(firstErr, harness.ErrConflict) && !errors.Is(firstErr, kernel.ErrConflict) {
		t.Fatalf("expected first physical cancellation to fail due to Store CAS without mutation: %v", firstErr)
	}
	root, err := runtime.Load(t.Context(), rootRunID)
	if err != nil {
		t.Fatal(err)
	}
	if root.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("first cancellation unexpectedly mutated Kernel root: %s", root.Run.Status)
	}
	before := canceller.calls.Load()
	_, retryErr := runner.Cancel(t.Context(), turnID, "retry after transient CAS contention")
	after := canceller.calls.Load()
	if errors.Is(retryErr, harness.ErrConflict) && before == after {
		t.Fatalf("REPRO: cancellation permanently blocked after PROVEN no-effect Kernel CAS conflicts: root still %s, retryErr=%v, physicalCancelAttempts=%d->%d",
			root.Run.Status, retryErr, before, after)
	}
}

// A child can be Accepted while its parent Team is cancelled without the
// overall Turn becoming terminal. The child still must not physically Start
// after its parent Capability Invocation has been cancelled.
func TestReauditAcceptedNestedChildCannotStartAfterParentCancellation(t *testing.T) {
	_, turnID, rootItemID, relations, _, backing, runtime := newFeatureInvocationHarness(t)
	store := &recheckAcceptedBeforeCancelStore{Store: backing, entered: make(chan struct{}), release: make(chan struct{})}
	feature := &recheckCountingTeamStart{auditRunningTeamFeature: auditRunningTeamFeature{runtime: runtime}}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Teams: feature,
		Store: store, Clock: featureInvocationClock{}, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	parentSnapshot, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: rootItemID, RequestID: "parent-to-cancel", Goal: "team parent",
		Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "task"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var parent harness.Invocation
	var parentItem string
	for _, inv := range parentSnapshot.Invocations {
		if inv.ParentItemID != "" && inv.ExecutionClass == harness.ExecutionTeam {
			parent = inv
			break
		}
	}
	for _, item := range parentSnapshot.Items {
		if item.InvocationID == parent.ID && item.Kind == harness.ItemInvocation && item.Status == harness.ItemStarted {
			parentItem = item.ID
			break
		}
	}
	if parent.ID == "" || parentItem == "" {
		t.Fatalf("parent fixture missing: inv=%+v item=%q", parent, parentItem)
	}
	feature.starts.Store(0)
	store.armed.Store(true)
	started := make(chan error, 1)
	go func() {
		_, startErr := runner.StartTeamInvocation(context.Background(), turnID, harness.TeamInvocationRequest{
			ParentItemID: parentItem, RequestID: "late-nested-child", Goal: "nested team",
			Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "task"}},
		})
		started <- startErr
	}()
	<-store.entered
	_, cancelErr := runner.CancelInvocation(t.Context(), parent.ID, "stop parent")
	close(store.release)
	startErr := <-started
	if cancelErr != nil {
		t.Fatalf("cancel parent: %v", cancelErr)
	}
	if feature.starts.Load() != 0 {
		t.Fatalf("REPRO: child Team.StartRun invoked AFTER parent CancelInvocation succeeded: startCalls=%d startErr=%v",
			feature.starts.Load(), startErr)
	}
}

// An untrusted Feature may report a generic Kernel conflict after performing
// a remote side effect. Without a real Kernel snapshot, no-effect is unproven;
// cancellation must not silently release that dispatch grant.
type auditAmbiguousTeamStart struct {
	calls atomic.Int32
}

func (f *auditAmbiguousTeamStart) StartRun(context.Context, team.StartRequest) (kernel.Snapshot, error) {
	f.calls.Add(1)
	return kernel.Snapshot{}, kernel.ErrConflict
}
func (*auditAmbiguousTeamStart) Resume(context.Context, string, uint64) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, kernel.ErrConflict
}
func TestReauditAmbiguousFeatureConflictKeepsPhysicalDispatchFence(t *testing.T) {
	_, tid, parentItem, relations, _, store, runtime := newFeatureInvocationHarness(t)
	feature := &auditAmbiguousTeamStart{}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Teams: feature, Store: store, Clock: featureInvocationClock{}, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, startErr := runner.StartTeamInvocation(t.Context(), tid, harness.TeamInvocationRequest{
		ParentItemID: parentItem, RequestID: "ambiguous-remote", Goal: "unknown remote dispatch",
		Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "task"}},
	})
	if !errors.Is(startErr, kernel.ErrConflict) || feature.calls.Load() != 1 {
		t.Fatalf("ambiguous start setup: err=%v count=%d", startErr, feature.calls.Load())
	}
	items, err := store.ListItems(t.Context(), tid, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !harness.PendingExecutionDispatch(items, "") {
		t.Fatal("generic kernel.ErrConflict incorrectly released an unproven physical Feature result")
	}
	if _, cancelErr := runner.Cancel(t.Context(), tid, "cancel uncertain"); !errors.Is(cancelErr, harness.ErrConflict) {
		t.Fatalf("cancellation crossed an unverified external Feature start: %v", cancelErr)
	}
}

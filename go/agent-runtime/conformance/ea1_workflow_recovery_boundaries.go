package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// RunEA1WorkflowRecoveryBoundariesSuite covers WR-Q/WC-Q P0/P5 only.
// Reconciliation accepts runID, not caller actor, expected revision or route.
func RunEA1WorkflowRecoveryBoundariesSuite(t *testing.T, factory func(*testing.T) (kernel.Store, func() (kernel.Store, kernel.Store))) {
	for _, child := range []bool{false, true} {
		for _, phase := range []string{"P0", "P5"} {
			name := "root"
			if child {
				name = "child"
			}
			t.Run(name+"/"+phase, func(t *testing.T) {
				first, reopen := factory(t)
				lost := &ea1WaitLostAck{Store: first, ChildExecutionStore: first.(kernel.ChildExecutionStore)}
				rt, err := kernel.New(kernel.Dependencies{Store: lost})
				if err != nil {
					t.Fatal(err)
				}
				actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
				thread := kernel.ThreadRef{Kind: "conversation", ID: "original-thread"}
				var parent kernel.Snapshot
				parentID := ""
				if child {
					parent, err = rt.Create(t.Context(), kernel.CreateRequest{ID: "parent", Kind: workflow.RunKind, Actor: actor, Thread: thread, Goal: "original parent", State: json.RawMessage("{}")})
					if err != nil {
						t.Fatal(err)
					}
					parentID = parent.Run.ID
				}
				wrongKind, err := rt.Create(t.Context(), kernel.CreateRequest{ID: "foreign-kind", Kind: "foreign", Actor: actor, Thread: thread, Goal: "non Workflow", State: json.RawMessage("{}")})
				if err != nil {
					t.Fatal(err)
				}
				corrupt, err := rt.Create(t.Context(), kernel.CreateRequest{ID: "corrupt-state", Kind: workflow.RunKind, Actor: actor, Thread: thread, Goal: "invalid Workflow state", State: json.RawMessage(`{"effects":"not-an-array"}`)})
				if err != nil {
					t.Fatal(err)
				}
				effect := &ea1WaitCounter{}
				setup, err := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Effects: effect})
				if err != nil {
					t.Fatal(err)
				}
				def, err := workflow.CompileDefinition(ea1UnknownWaitDraft())
				if err != nil {
					t.Fatal(err)
				}
				clean, err := setup.StartRun(t.Context(), workflow.StartRequest{ID: "clean-no-grant", Actor: actor, Thread: thread, Goal: "clean waiting sibling", Definition: def, Input: json.RawMessage("{}")})
				if err != nil {
					t.Fatal(err)
				}
				waiting, err := setup.StartRun(t.Context(), workflow.StartRequest{ID: "original", ParentRunID: parentID, Actor: actor, Thread: thread, Goal: "original recovery", Definition: def, Input: json.RawMessage("{}")})
				if err != nil || len(effect.requests) != 0 {
					t.Fatalf("zero-effect Wait: %v", err)
				}
				lost.armed.Store(true)
				completed, err := setup.ResolveWait(t.Context(), waiting.Run.ID, waiting.Run.Revision, json.RawMessage(`{"approved":true}`))
				if !errors.Is(err, kernel.ErrChildExecutionBusy) || completed.Run.Status != kernel.RunStatusCompleted || lost.lost.Load() != 1 || len(effect.requests) != 1 {
					t.Fatalf("original Mark ACK loss not reached: %v", err)
				}
				grant, err := rt.LookupChildExecution(t.Context(), completed.Run.ID)
				if err != nil || grant.ExpectedRevision != waiting.Run.Revision {
					t.Fatalf("original grant: %+v %v", grant, err)
				}
				assertEA1WaitReceipt(t, completed, waiting, def, effect.requests)
				a, b := reopen()
				observer, err := kernel.New(kernel.Dependencies{Store: a})
				if err != nil {
					t.Fatal(err)
				}
				replayA, replayB := &ea1WaitCounter{}, &ea1WaitCounter{}
				assertUnchanged := func() {
					for _, want := range []kernel.Snapshot{completed, wrongKind, corrupt, clean} {
						ea1CompensationSnapshotUnchanged(t, observer, want)
					}
					if child {
						ea1CompensationSnapshotUnchanged(t, observer, parent)
					}
					if len(effect.requests) != 1 || len(replayA.requests) != 0 || len(replayB.requests) != 0 {
						t.Fatal("recovery physically replayed")
					}
				}
				assertGrant := func() {
					assertUnchanged()
					same, e := observer.LookupChildExecution(t.Context(), completed.Run.ID)
					if e != nil || same != grant {
						t.Fatal("preflight/control changed original grant")
					}
				}
				assertGrant()
				if phase == "P0" {
					owner, e := workflow.NewRunner(workflow.Dependencies{Runtime: observer, Effects: replayA})
					if e != nil {
						t.Fatal(e)
					}
					var nilRunner *workflow.Runner
					if e = nilRunner.ReconcileChildPhysicalGrant(t.Context(), completed.Run.ID); !errors.Is(e, workflow.ErrInvalidExecution) {
						t.Fatalf("nil runner: %v", e)
					}
					assertGrant()
					for _, tc := range []struct {
						id   string
						want error
					}{{"", workflow.ErrInvalidExecution}, {"missing", kernel.ErrNotFound}, {wrongKind.Run.ID, kernel.ErrChildExecutionBusy}, {corrupt.Run.ID, kernel.ErrChildExecutionBusy}, {clean.Run.ID, kernel.ErrNotFound}} {
						if e = owner.ReconcileChildPhysicalGrant(t.Context(), tc.id); !errors.Is(e, tc.want) {
							t.Fatalf("preflight %q: %v want %v", tc.id, e, tc.want)
						}
						assertGrant()
					}
					// Only the explicitly selected original owner may retire its own Grant.
					if e = owner.ReconcileChildPhysicalGrant(t.Context(), completed.Run.ID); e != nil {
						t.Fatalf("original recovery: %v", e)
					}
					assertUnchanged()
				} else {
					entered := make(chan struct{}, 2)
					release := make(chan struct{})
					var once sync.Once
					unblock := func() { once.Do(func() { close(release) }) }
					defer unblock()
					wa := &ea1RecoveryFinishBarrier{Store: a, ChildExecutionStore: a.(kernel.ChildExecutionStore), entered: entered, release: release}
					wb := &ea1RecoveryFinishBarrier{Store: b, ChildExecutionStore: b.(kernel.ChildExecutionStore), entered: entered, release: release}
					ra, e := kernel.New(kernel.Dependencies{Store: wa})
					if e != nil {
						t.Fatal(e)
					}
					rb, e := kernel.New(kernel.Dependencies{Store: wb})
					if e != nil {
						t.Fatal(e)
					}
					oa, e := workflow.NewRunner(workflow.Dependencies{Runtime: ra, Effects: replayA})
					if e != nil {
						t.Fatal(e)
					}
					ob, e := workflow.NewRunner(workflow.Dependencies{Runtime: rb, Effects: replayB})
					if e != nil {
						t.Fatal(e)
					}
					results := make(chan error, 2)
					go func() { results <- oa.ReconcileChildPhysicalGrant(context.Background(), completed.Run.ID) }()
					go func() { results <- ob.ReconcileChildPhysicalGrant(context.Background(), completed.Run.ID) }()
					for i := 0; i < 2; i++ {
						select {
						case <-entered:
						case <-time.After(5 * time.Second):
							t.Fatal("both original recoverers did not reach Finish barrier")
						}
					}
					assertGrant()
					if _, e = observer.Cancel(t.Context(), completed.Run.ID, completed.Run.Revision, "competing self"); !errors.Is(e, kernel.ErrTerminal) {
						t.Fatalf("terminal original control: %v", e)
					}
					assertGrant()
					if child {
						if _, e = observer.Apply(t.Context(), parent.Run.ID, parent.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: parent.State}); !errors.Is(e, kernel.ErrChildExecutionBusy) {
							t.Fatalf("ancestor nonterminal control: %v", e)
						}
						assertGrant()
						if _, e = observer.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "competing ancestor"); !errors.Is(e, kernel.ErrChildExecutionBusy) {
							t.Fatalf("ancestor terminal control: %v", e)
						}
						assertGrant()
					}
					unblock()
					wins, losses := 0, 0
					for i := 0; i < 2; i++ {
						select {
						case result := <-results:
							if result == nil {
								wins++
							} else if errors.Is(result, kernel.ErrExecutionConflict) && errors.Is(result, kernel.ErrChildExecutionBusy) {
								losses++
							} else {
								t.Fatalf("concurrent retirement result: %v", result)
							}
						case <-time.After(5 * time.Second):
							t.Fatal("recovery race stalled")
						}
					}
					if wins != 1 || losses != 1 {
						t.Fatalf("recovery winners=%d losers=%d", wins, losses)
					}
					assertUnchanged()
				}
				owner, err := workflow.NewRunner(workflow.Dependencies{Runtime: observer, Effects: replayA})
				if err != nil {
					t.Fatal(err)
				}
				if err = owner.ReconcileChildPhysicalGrant(t.Context(), completed.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("retired original: %v", err)
				}
				assertUnchanged()
				if _, err = observer.LookupChildExecution(t.Context(), completed.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("retired grant still present: %v", err)
				}
				durable, err := observer.Load(t.Context(), completed.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				assertEA1WaitReceipt(t, durable, waiting, def, effect.requests)
				if child {
					if _, err = observer.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "after original retirement"); err != nil {
						t.Fatalf("ancestor not released: %v", err)
					}
					ea1CompensationSnapshotUnchanged(t, observer, completed)
				}
			})
		}
	}
}

type ea1RecoveryFinishBarrier struct {
	kernel.Store
	kernel.ChildExecutionStore
	entered chan<- struct{}
	release <-chan struct{}
}

func (s *ea1RecoveryFinishBarrier) FinishChildExecution(ctx context.Context, grant kernel.ChildExecutionGrant) error {
	select {
	case s.entered <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.ChildExecutionStore.FinishChildExecution(ctx, grant)
}

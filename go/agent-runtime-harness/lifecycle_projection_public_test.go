package harness_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

// RefreshInvocation is the public child-observation boundary. A physical
// Kernel Run that still reads Running must not roll back a terminal Harness
// Invocation, even if the Store's Revision is otherwise current.
func TestRefreshInvocationPreservesTerminalChildAgainstStaleRunningRun(t *testing.T) {
	for _, status := range []harness.InvocationStatus{
		harness.InvocationCompleted, harness.InvocationFailed, harness.InvocationCancelled,
	} {
		t.Run(string(status), func(t *testing.T) {
			_, turnID, parentItemID, relations, _, store, runtime := newFeatureInvocationHarness(t)
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
				Store: store, Clock: featureInvocationClock{},
				Teams: auditRunningTeamFeature{runtime: runtime}, Relations: relations,
			})
			if err != nil {
				t.Fatal(err)
			}

			started, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
				ParentItemID: parentItemID, RequestID: "terminal-observation",
				Goal: "observe physical running child", Mode: team.ExecutionSequential,
				Members: []team.Member{{ID: "worker", Goal: "still running"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			running := teamChildInvocation(t, started)
			physical, err := runtime.Load(t.Context(), running.ExecutionRefID)
			if err != nil || running.Status != harness.InvocationRunning || physical.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("fixture must have a running physical child: invocation=%+v run=%+v err=%v", running, physical.Run, err)
			}

			// Simulate an already-committed Harness terminal observation whose
			// physical Run reader supplies an older Running state. This is
			// a legal Running -> terminal Store transition, not a terminal
			// status rewrite or an unverified retry.
			running.Status = status
			running.UpdatedAt = running.UpdatedAt.Add(time.Second)
			settled, err := store.UpdateInvocation(t.Context(), running, running.Revision)
			if err != nil {
				t.Fatal(err)
			}
			beforeItems, err := store.ListItems(t.Context(), turnID, 0, 500)
			if err != nil {
				t.Fatal(err)
			}
			beforeTurn, err := store.GetTurn(t.Context(), turnID)
			if err != nil {
				t.Fatal(err)
			}

			for range 2 {
				if _, err = runner.RefreshInvocation(t.Context(), settled.ID); !errors.Is(err, harness.ErrConflict) {
					t.Fatalf("stale physical Running state changed terminal %s: %v", status, err)
				}
				actual, getErr := store.GetInvocation(t.Context(), settled.ID)
				if getErr != nil || !reflect.DeepEqual(actual, settled) {
					t.Fatalf("terminal Invocation changed: want=%+v got=%+v err=%v", settled, actual, getErr)
				}
				afterItems, listErr := store.ListItems(t.Context(), turnID, 0, 500)
				if listErr != nil || !reflect.DeepEqual(afterItems, beforeItems) {
					t.Fatalf("rejected Refresh changed immutable Items: before=%d after=%d err=%v", len(beforeItems), len(afterItems), listErr)
				}
				afterTurn, turnErr := store.GetTurn(t.Context(), turnID)
				if turnErr != nil || !reflect.DeepEqual(afterTurn, beforeTurn) {
					t.Fatalf("rejected child Refresh mutated owning Turn: before=%+v after=%+v err=%v", beforeTurn, afterTurn, turnErr)
				}
				physicalAfter, loadErr := runtime.Load(t.Context(), settled.ExecutionRefID)
				if loadErr != nil || physicalAfter.Run.Status != kernel.RunStatusRunning ||
					physicalAfter.Run.Revision != physical.Run.Revision {
					t.Fatalf("rejected Refresh advanced physical Run: before=%+v after=%+v err=%v", physical.Run, physicalAfter.Run, loadErr)
				}
			}
		})
	}
}

// Matching terminal observations must still repair/replay their original
// presentation facts, without creating a new Invocation revision or Item.
func TestRefreshInvocationMatchingTerminalChildIsIdempotent(t *testing.T) {
	runner, turnID, parentItemID, _, _, store, runtime := newFeatureInvocationHarness(t)
	started, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: parentItemID, RequestID: "terminal-replay",
		Goal: "finish once", Mode: team.ExecutionSequential,
		Members: []team.Member{{ID: "worker", Goal: "done"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	completed := teamChildInvocation(t, started)
	physical, err := runtime.Load(t.Context(), completed.ExecutionRefID)
	if err != nil || completed.Status != harness.InvocationCompleted ||
		physical.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("fixture must be durably complete: invocation=%+v run=%+v err=%v", completed, physical.Run, err)
	}
	beforeItems, err := store.ListItems(t.Context(), turnID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		replayed, refreshErr := runner.RefreshInvocation(t.Context(), completed.ID)
		if refreshErr != nil {
			t.Fatalf("identical terminal projection should replay: %v", refreshErr)
		}
		projected := teamChildInvocation(t, replayed)
		if !reflect.DeepEqual(projected, completed) {
			t.Fatalf("terminal replay changed Invocation: want=%+v got=%+v", completed, projected)
		}
		items, listErr := store.ListItems(t.Context(), turnID, 0, 500)
		if listErr != nil || !reflect.DeepEqual(items, beforeItems) {
			t.Fatalf("terminal replay appended Item: before=%d after=%d err=%v", len(beforeItems), len(items), listErr)
		}
		unchanged, loadErr := runtime.Load(t.Context(), completed.ExecutionRefID)
		if loadErr != nil || unchanged.Run.Revision != physical.Run.Revision {
			t.Fatalf("terminal replay advanced physical Run: before=%+v after=%+v err=%v", physical.Run, unchanged.Run, loadErr)
		}
	}
}

package harnesspostgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type cancellationAdmissionCut struct {
	generationStore
	entered, release chan struct{}
	once             sync.Once
}

func (s *cancellationAdmissionCut) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	if item.ParentItemID == harness.CancellationFenceParentItemID {
		s.once.Do(func() { close(s.entered); <-s.release })
	}
	return s.generationStore.AppendItem(ctx, item)
}

func generationRunnerForStore(t *testing.T, store generationStore, rt *kernel.Runtime, native *agent.Runner) *harness.Runner {
	t.Helper()
	shared, err := harness.NewBudgetAuthority(harness.BudgetAuthorityDependencies{Store: store, Ledgers: store, Clock: generationClock{}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: native, Store: store, Budget: shared, Clock: generationClock{}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCancellationAdmissionCompetesWithExactRetryAttempt(t *testing.T) {
	for _, sqlBackend := range []bool{false, true} {
		name := "memory"
		if sqlBackend {
			name = "sql"
		}
		t.Run(name, func(t *testing.T) {
			r, hs, rt, native, req := newGenerationHarness(t, sqlBackend, &generationModel{failFirst: true, delegateEveryRoot: true})
			first, err := r.StartAgentTurn(t.Context(), req)
			if first.Turn.Status != harness.TurnFailed {
				t.Fatalf("known original failure: %+v %v", first.Turn, err)
			}
			root, _ := harness.TopLevelInvocation(first)
			cut := &cancellationAdmissionCut{generationStore: hs, entered: make(chan struct{}), release: make(chan struct{})}
			stale := generationRunnerForStore(t, cut, rt, native)
			result := make(chan error, 1)
			go func() {
				_, cancelErr := stale.Cancel(t.Context(), first.Turn.ID, "old original stop")
				result <- cancelErr
			}()
			<-cut.entered
			next, retryErr := r.RetryInvocation(t.Context(), root.ID)
			close(cut.release)
			staleErr := <-result
			if retryErr != nil || next.Turn.Status != harness.TurnRunning || !errors.Is(staleErr, harness.ErrConflict) {
				t.Fatalf("Attempt CAS arbitration: retry=%v status=%s staleCancel=%v", retryErr, next.Turn.Status, staleErr)
			}
			items, err := hs.ListItems(t.Context(), first.Turn.ID, 0, 500)
			if err != nil || harness.ActiveCancellationFence(items) {
				t.Fatalf("stale intent escaped atomic owner check: %v", err)
			}
			if stopped, err := r.Cancel(t.Context(), first.Turn.ID, "current replacement stop"); err != nil || stopped.Turn.Status != harness.TurnCancelled {
				t.Fatalf("current Attempt could not cancel: %+v %v", stopped.Turn, err)
			}
		})
	}
}

func TestHistoricalChildControlsRejectBeforeIntentAdmission(t *testing.T) {
	for _, sqlBackend := range []bool{false, true} {
		for _, byInvocation := range []bool{false, true} {
			name := "memory"
			if sqlBackend {
				name = "sql"
			}
			if byInvocation {
				name += "/invocation"
			} else {
				name += "/subtask"
			}
			t.Run(name, func(t *testing.T) {
				r, hs, _, _, req := newGenerationHarness(t, sqlBackend, &generationModel{delegateEveryRoot: true})
				first, err := r.StartAgentTurn(t.Context(), req)
				if err != nil {
					t.Fatal(err)
				}
				oldTask := first.Subtasks[0]
				stopped, err := r.Cancel(t.Context(), first.Turn.ID, "stop original")
				if err != nil {
					t.Fatal(err)
				}
				root, _ := harness.TopLevelInvocation(stopped)
				child, err := hs.GetInvocationByExecutionRefID(t.Context(), oldTask.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = r.RetryInvocation(t.Context(), root.ID); err != nil {
					t.Fatal(err)
				}
				before, err := hs.ListItems(t.Context(), first.Turn.ID, 0, 500)
				if err != nil {
					t.Fatal(err)
				}
				if byInvocation {
					_, err = r.CancelInvocation(t.Context(), child.ID, "obsolete child")
				} else {
					_, err = r.CancelSubtask(t.Context(), first.Turn.ID, oldTask.ID, "obsolete child")
				}
				if !errors.Is(err, harness.ErrConflict) {
					t.Fatalf("obsolete target was not rejected: %v", err)
				}
				after, err := hs.ListItems(t.Context(), first.Turn.ID, 0, 500)
				if err != nil || len(after) != len(before) || harness.ActiveCancellationFence(after) {
					t.Fatalf("rejected historical control mutated current Turn: %v", err)
				}
				if _, err = r.Cancel(t.Context(), first.Turn.ID, "stop current replacement"); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

package harnesspostgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var errWaitingRetryACKLost = errors.New("committed Retry response lost")

type waitingRetryACKCut struct {
	generationStore
	armed bool
}

func (s *waitingRetryACKCut) RetryInvocation(ctx context.Context, id string, rev uint64, next string, at time.Time) (harness.Invocation, error) {
	updated, err := s.generationStore.RetryInvocation(ctx, id, rev, next, at)
	if err != nil {
		return harness.Invocation{}, err
	}
	if !s.armed {
		return updated, nil
	}
	s.armed = false
	return harness.Invocation{}, errWaitingRetryACKLost
}

func TestWaitingOnlyHistorySurvivesRetryAndNoRunCancellation(t *testing.T) {
	for _, sqlBackend := range []bool{false, true} {
		name := "memory"
		if sqlBackend {
			name = "sql"
		}
		t.Run(name, func(t *testing.T) {
			cut := &waitingRetryACKCut{}
			r, hs, rt, _, req := newGenerationHarnessBackend(t, name, &generationModel{}, func(base generationStore) generationStore { cut.generationStore = base; return cut })
			req.Config.SharedBudget.MaxConcurrentDescendants = 1
			first, err := r.StartAgentTurn(t.Context(), req)
			if err != nil || len(first.Subtasks) != 1 {
				t.Fatalf("original child: %+v %v", first.Subtasks, err)
			}
			wait, err := r.Delegate(t.Context(), first.Turn.ID, harness.DelegateRequest{MemberID: "researcher", Goal: "second task awaiting capacity"})
			if err != nil || wait.Delegation.ChildRunID == "" {
				t.Fatalf("capacity waiting: %+v %v", wait.Delegation, err)
			}
			if _, err = rt.Load(t.Context(), wait.Delegation.ChildRunID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("waiting-only child dispatched: %v", err)
			}
			stopped, err := r.Cancel(t.Context(), first.Turn.ID, "stop original subtree")
			if err != nil || stopped.Turn.Status != harness.TurnCancelled {
				t.Fatalf("original Cancel: %+v %v", stopped.Turn, err)
			}
			root, _ := harness.TopLevelInvocation(stopped)
			cut.armed = true
			if _, err = r.RetryInvocation(t.Context(), root.ID); !errors.Is(err, errWaitingRetryACKLost) {
				t.Fatalf("Retry crash cut: %v", err)
			}
			for range 2 {
				cancelled, cancelErr := r.Cancel(t.Context(), first.Turn.ID, "cancel undispatched replacement")
				if cancelErr != nil || cancelled.Turn.Status != harness.TurnCancelled {
					t.Fatalf("no-Run cancellation/replay: %+v %v", cancelled.Turn, cancelErr)
				}
			}
			items, err := hs.ListItems(t.Context(), first.Turn.ID, 0, 500)
			if err != nil || harness.ActiveCancellationFence(items) {
				t.Fatalf("retained stale cancellation fence: %v", err)
			}
			waitingRetained := false
			for _, item := range items {
				if item.Kind == harness.ItemDelegation && item.Status == harness.ItemWaiting && item.RunID == wait.Delegation.ChildRunID {
					waitingRetained = true
				}
			}
			if !waitingRetained {
				t.Fatal("cancellation deleted original waiting history")
			}
		})
	}
}

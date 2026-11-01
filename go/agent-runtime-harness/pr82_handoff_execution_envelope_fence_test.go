package harness

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type pr82UnrelatedHandoffExecution struct {
	snapshot kernel.Snapshot
	starts   int
}

func (runner *pr82UnrelatedHandoffExecution) LoadRun(context.Context, string) (kernel.Snapshot, error) {
	return runner.snapshot, nil
}

func (runner *pr82UnrelatedHandoffExecution) StartRun(context.Context, agent.StartRequest) (kernel.Snapshot, error) {
	runner.starts++
	return kernel.Snapshot{}, errors.New("unrelated child must not be started")
}

// The child ID, Kind, Actor and Thread can all match despite a misrouted
// ChildRunner returning a DIFFERENT delegated task. Its Result must never
// settle the persisted child-owned Handoff physical grant.
func TestPR82HandoffForeignGoalOrRequestCannotSettleGrant(t *testing.T) {
	for _, tc := range []string{"goal", "request"} {
		t.Run(tc, func(t *testing.T) {
			f := newPR82HandoffFixture(t)
			d := f.delegation()
			childRun := kernel.Run{
				ID: d.ChildRunID, Kind: agent.RunKind,
				Actor: f.parentRun.Run.Actor, Thread: f.parentRun.Run.Thread,
				Goal: d.Goal, RequestID: f.parentRun.Run.ID + ":" + d.ID,
				Status: kernel.RunStatusCompleted,
			}
			if tc == "goal" {
				childRun.Goal = "another task"
			} else {
				childRun.RequestID = "other-parent:other-delegation"
			}
			foreign := &pr82UnrelatedHandoffExecution{snapshot: kernel.Snapshot{
				Run:    childRun,
				Result: &kernel.Result{ContentType: "application/json", Content: []byte(`{"foreign":"result"}`)},
			}}
			coordinator, err := handoff.New(foreign)
			if err != nil {
				t.Fatal(err)
			}
			f.runner.handoffs = coordinator
			if err := f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, d, f.startedItemID); err != nil {
				t.Fatal(err)
			}
			result, err := f.runner.dispatchHandoff(t.Context(), f.turn, f.parentRun, d, f.startedItemID)
			if !errors.Is(err, ErrExecutionOutcomeUnknown) || !errors.Is(err, handoff.ErrChildRunUnverified) ||
				result.ID != "" || len(result.Result) != 0 {
				t.Fatalf("foreign child result projected or grant settled: %+v %v", result, err)
			}
			items, err := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
			if err != nil || !PendingExecutionDispatch(items, "") {
				t.Fatalf("foreign child cleared physical grant: %v", err)
			}
			if _, err := f.runner.dispatchHandoff(t.Context(), f.turn, f.parentRun, d, f.startedItemID); !errors.Is(err, ErrConflict) {
				t.Fatalf("unreconciled foreign child result permitted replay: %v", err)
			}
			if foreign.starts != 0 {
				t.Fatalf("a mismatched loaded child started a second physical Run: %d", foreign.starts)
			}
		})
	}
}

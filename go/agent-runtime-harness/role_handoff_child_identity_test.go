package harness

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type pr82WrongChildRunner struct {
	start bool
	id    string
	loads int
	calls int
}

func (child *pr82WrongChildRunner) LoadRun(context.Context, string) (kernel.Snapshot, error) {
	child.loads++
	if child.start {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	return kernel.Snapshot{Run: kernel.Run{ID: child.id, Status: kernel.RunStatusRunning}}, nil
}

func (child *pr82WrongChildRunner) StartRun(context.Context, agent.StartRequest) (kernel.Snapshot, error) {
	child.calls++
	return kernel.Snapshot{Run: kernel.Run{ID: child.id, Status: kernel.RunStatusRunning}}, nil
}

// End-to-end: a projected Delegation must never turn a different ChildRunner
// Run's state into a durable physical dispatch completion receipt.
func TestPR82HandoffWrongChildRunKeepsDispatchGrant(t *testing.T) {
	for _, fromStart := range []bool{false, true} {
		name := "load"
		if fromStart {
			name = "start"
		}
		t.Run(name, func(t *testing.T) {
			f := newPR82HandoffFixture(t)
			child := &pr82WrongChildRunner{start: fromStart, id: "unrelated-child-run"}
			coordinator, err := handoff.New(child)
			if err != nil {
				t.Fatal(err)
			}
			f.runner.handoffs = coordinator
			delegation := f.delegation()
			if err := f.runner.prepareRoleChild(t.Context(), f.turn, f.parent, delegation, f.startedItemID); err != nil {
				t.Fatal(err)
			}
			result, err := f.runner.dispatchRoleHandoff(t.Context(), f.turn, f.parentRun, delegation, f.startedItemID)
			if !errors.Is(err, ErrExecutionOutcomeUnknown) ||
				!errors.Is(err, handoff.ErrChildRunUnverified) || result.ID != "" {
				t.Fatalf("unverified child was certified: result=%+v err=%v", result, err)
			}
			items, err := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
			if err != nil {
				t.Fatal(err)
			}
			if !PendingExecutionDispatch(items, "") {
				t.Fatal("incorrect child identity closed the physical dispatch grant")
			}
			_, err = f.runner.dispatchRoleHandoff(t.Context(), f.turn, f.parentRun, delegation, f.startedItemID)
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("unreconciled grant allowed duplicate dispatch: %v", err)
			}
			expectedCalls := 0
			if fromStart {
				expectedCalls = 1
			}
			if child.calls != expectedCalls || child.loads != 1 {
				t.Fatalf("unexpected child calls: starts=%d loads=%d", child.calls, child.loads)
			}
		})
	}
}

package http

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestCancellationRouterUsesExactStaticRunKind(t *testing.T) {
	t.Parallel()
	canceller := &recordingCanceller{}
	router, err := NewCancellationRouter(CancellationRoute{
		Kind: "workflow", Canceller: canceller,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, routed, err := router.cancel(t.Context(), kernel.Snapshot{Run: kernel.Run{
		ID: "run-1", Kind: "workflow", Revision: 4,
	}}, 4, "undo first")
	if err != nil || !routed || snapshot.Run.Status != kernel.RunStatusCancelled ||
		canceller.runID != "run-1" || canceller.revision != 4 {
		t.Fatalf("snapshot=%#v routed=%v canceller=%#v err=%v", snapshot, routed, canceller, err)
	}
	_, routed, err = router.cancel(t.Context(), kernel.Snapshot{Run: kernel.Run{Kind: "agent"}}, 1, "")
	if err != nil || routed {
		t.Fatalf("unexpected route: routed=%v err=%v", routed, err)
	}
}

func TestCancellationRouterRejectsDuplicateKind(t *testing.T) {
	t.Parallel()
	canceller := &recordingCanceller{}
	_, err := NewCancellationRouter(
		CancellationRoute{Kind: "workflow", Canceller: canceller},
		CancellationRoute{Kind: "workflow", Canceller: canceller},
	)
	if !errors.Is(err, ErrInvalidCancellationRoute) {
		t.Fatalf("err=%v", err)
	}
}

type recordingCanceller struct {
	runID    string
	revision uint64
}

func (canceller *recordingCanceller) Cancel(
	_ context.Context,
	runID string,
	revision uint64,
	_ string,
) (kernel.Snapshot, error) {
	canceller.runID = runID
	canceller.revision = revision
	return kernel.Snapshot{Run: kernel.Run{
		ID: runID, Kind: "workflow", Revision: revision + 1, Status: kernel.RunStatusCancelled,
	}}, nil
}

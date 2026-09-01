package memory

import (
	"context"
	"math"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestListEventsHandlesAfterSequenceBeyondNativeInt(t *testing.T) {
	t.Parallel()
	store := NewStore()
	const runID = "run_large_after_sequence"
	_, err := store.Create(
		context.Background(),
		kernel.Record{Run: kernel.Run{ID: runID}},
		[]kernel.EventDraft{{Type: "created"}},
	)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	events, err := store.ListEvents(context.Background(), runID, math.MaxInt64, 1)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %#v, want empty page", events)
	}
}

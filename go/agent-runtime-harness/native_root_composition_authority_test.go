package harness_test

import (
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func TestNativeRootRejectsIndependentKernelComposition(t *testing.T) {
	local, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	native, err := agent.NewRunner(agent.Dependencies{Runtime: foreign, Model: directModel{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = harness.NewRunner(harness.Dependencies{Runtime: local, Agent: native, Store: harness.NewMemoryStore()}); !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("independent native Kernel admitted as local authority: %v", err)
	}
}

func TestNativeRootCustomCancellationDoesNotElectHostStart(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	native, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: directModel{}})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	canceller := &customPreflightCancellationRecorder{runtime: rt}
	host, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: native, Cancellation: canceller, Store: store, Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := host.StartAgentTurn(t.Context(), testStartRequest())
	if err != nil || completed.Turn.Status != harness.TurnCompleted {
		t.Fatalf("same-Kernel native Start: %s %v", completed.Turn.Status, err)
	}
	items, err := store.ListItems(t.Context(), completed.Turn.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ParentItemID == harness.ExecutionDispatchParentItemID {
			t.Fatal("custom canceller changed native Start into Host dispatch")
		}
	}
	if canceller.calls.Load() != 0 {
		t.Fatal("Start invoked independent cancellation cleanup")
	}
}

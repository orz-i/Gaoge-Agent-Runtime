package harness_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

// A concrete native Agent cannot lend its Kernel-CAS cancellation exception
// to an arbitrary Host RuntimeCanceller. The latter may report cancellation
// without fencing the Agent's actual Run, so its unsettled Start must remain
// an authoritative physical-effect conflict.
func TestPR82NativeAgentWithCustomCancellerKeepsUnsettledStartFence(t *testing.T) {
	model := &blockingModel{started: make(chan struct{}), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(model.release) })
	defer release()
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	native, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	canceller := &customPreflightCancellationRecorder{runtime: rt}
	host, err := harness.NewRunner(harness.Dependencies{
		Runtime: rt, Agent: native, Cancellation: canceller,
		Store: harness.NewMemoryStore(), Clock: fixedClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	startDone := make(chan error, 1)
	go func() {
		_, startErr := host.StartAgentTurn(context.Background(), testStartRequest())
		startDone <- startErr
	}()
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("native Agent did not enter its physical Model call")
	}
	_, cancelErr := host.Cancel(t.Context(), harnessTurnID(t), "custom cancel is not Kernel proof")
	if !errors.Is(cancelErr, harness.ErrConflict) || canceller.calls.Load() != 0 {
		t.Fatalf("custom canceller bypassed native Agent's unresolved Start: err=%v physical cancels=%d", cancelErr, canceller.calls.Load())
	}
	release()
	select {
	case startErr := <-startDone:
		if startErr != nil {
			t.Fatalf("original native Agent Start failed after rejected Cancel: %v", startErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native Agent did not finish after rejected Cancel")
	}
}

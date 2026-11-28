package harness_test

import (
	"context"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestNativeUnmeteredAdvancedCommandsNeverElectHostAuthority(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		name := "ordinary"
		if advanced {
			name = "pure-preparer"
		}
		t.Run(name, func(t *testing.T) {
			rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
			if err != nil {
				t.Fatal(err)
			}
			calls := map[string]int{}
			client := &agentSessionModel{generate: func(req model.Request) (model.Response, error) {
				calls[req.RunID]++
				return model.Response{Content: "original", Continue: calls[req.RunID] == 1}, nil
			}}
			deps := agent.Dependencies{Runtime: rt, Model: client}
			if advanced {
				deps.PrepareModelRequest = func(_ context.Context, req model.Request) (model.Request, error) { return req, nil }
			}
			native, err := agent.NewRunner(deps)
			if err != nil {
				t.Fatal(err)
			}
			store := harness.NewMemoryStore()
			host, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: native, Store: store})
			if err != nil {
				t.Fatal(err)
			}
			request := testStartRequest()
			request.Config.SharedBudget = nil
			started, err := host.StartAgentTurn(t.Context(), request)
			if err != nil || started.Turn.Status != harness.TurnRunning {
				t.Fatalf("native Start: %s %v", started.Turn.Status, err)
			}
			completed, err := host.ResumeTurn(t.Context(), started.Turn.ID)
			if err != nil || completed.Turn.Status != harness.TurnCompleted {
				t.Fatalf("native Resume: %s %v", completed.Turn.Status, err)
			}
			root, ok := harness.TopLevelInvocation(completed)
			if !ok || calls[root.ExecutionRefID] != 2 {
				t.Fatal("native Resume changed or replayed original execution")
			}
			request.HostTurn.ID += "-cancel"
			pending, err := host.StartAgentTurn(t.Context(), request)
			if err != nil || pending.Turn.Status != harness.TurnRunning {
				t.Fatalf("cancellable Start: %s %v", pending.Turn.Status, err)
			}
			stopped, err := host.Cancel(t.Context(), pending.Turn.ID, "stop original")
			if err != nil || stopped.Turn.Status != harness.TurnCancelled {
				t.Fatalf("native Cancel: %s %v", stopped.Turn.Status, err)
			}
			cancelledRoot, ok := harness.TopLevelInvocation(stopped)
			committed, err := rt.Load(t.Context(), cancelledRoot.ExecutionRefID)
			if !ok || err != nil || committed.Run.Status != kernel.RunStatusCancelled || calls[cancelledRoot.ExecutionRefID] != 1 {
				t.Fatalf("cancellation lacked original Runtime fact: %+v %v", committed.Run, err)
			}
			for _, out := range []harness.Snapshot{completed, stopped} {
				for _, item := range out.Items {
					if item.ParentItemID == harness.ExecutionDispatchParentItemID || item.ParentItemID == harness.CancellationFenceParentItemID {
						t.Fatal("first-party command elected Host execution/cancellation")
					}
				}
			}
		})
	}
}

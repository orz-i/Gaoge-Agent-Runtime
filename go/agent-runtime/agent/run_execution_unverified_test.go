package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// This checks that an unknown external attempt cannot be marked complete
// unless the provider-side lookup actually confirms its stable identity.
func TestNativeReconciliationCannotInventMissingCommit(t *testing.T) {
	clock := &advancementClock{instant: time.Now().UTC().Truncate(time.Millisecond)}
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStoreWithExecutionClock(clock)})
	if err != nil {
		t.Fatal(err)
	}
	m := &lateModel{invokeTool: true}
	executions, commits, queries := 0, 0, 0
	catalog, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: "slow", Name: "Slow", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, _ tools.ExecutionRequest) (tools.ExecutionResult, error) {
			executions++
			return tools.ExecutionResult{}, errors.New("provider refused before commit")
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: m, Catalog: catalog, Executor: catalog, Clock: clock,
		RunExecution: &agent.RunExecution{OwnerID: "native", LeaseDuration: 2 * time.Minute},
		ToolOutcomeVerifier: agent.ToolOutcomeVerifierFunc(func(_ context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
			queries++
			if req.RunID != "no-provider-commit" || req.Call.ID != "late-call" {
				t.Fatalf("verifier received wrong invocation %+v", req)
			}
			if commits == 0 {
				return tools.ExecutionResult{}, errors.New("no provider COMMIT receipt")
			}
			return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: req.Call.ID, Disposition: "committed"}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	started, e := runner.StartRun(t.Context(), agent.StartRequest{
		ID: "no-provider-commit", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "no side effects", ToolKeys: []string{"slow"},
	})
	if !errors.Is(e, agent.ErrToolOutcomeUnknown) || executions != 1 || commits != 0 {
		t.Fatalf("incorrect initial unknown state: status=%s err=%v", started.Run.Status, e)
	}
	clock.Advance(3 * time.Minute)
	if _, e = runner.ReconcileToolOutcome(t.Context(), started.Run.ID, started.Run.Revision, "late-call"); !errors.Is(e, agent.ErrToolOutcomeUnknown) || queries != 1 {
		t.Fatalf("no-COMMIT verified as complete: err=%v queries=%d", e, queries)
	}
	current, loadErr := rt.Load(t.Context(), started.Run.ID)
	if loadErr != nil || current.Run.Revision != started.Run.Revision || current.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("unverified receipt mutated Run: %+v %v", current.Run, loadErr)
	}
	if _, e = runner.Resume(t.Context(), current.Run.ID, current.Run.Revision); !errors.Is(e, agent.ErrToolOutcomeUnknown) || executions != 1 {
		t.Fatalf("unverified action replayed: %v executions=%d", e, executions)
	}
}

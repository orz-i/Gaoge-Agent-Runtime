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
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type auditRetryThenEffectModel struct{}

func (auditRetryThenEffectModel) Generate(_ context.Context, req model.Request) (model.Response, error) {
	for _, message := range req.Messages {
		if message.Role == model.RoleTool {
			return model.Response{Content: "done", Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}}, nil
		}
	}
	return model.Response{ToolCalls: []tools.Call{{ID: "effect-tool", ToolKey: "effect", Arguments: json.RawMessage(`{}`)}},
		Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}}, nil
}

func TestNativeToolUnknownOutcomeBlocksReplayAfterExpiry(t *testing.T) {
	clock := &advancementClock{instant: time.Now().UTC().Truncate(time.Millisecond)}
	store := memory.NewStoreWithExecutionClock(clock)
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	attempts, physicalEffects := 0, 0
	catalog, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: "effect", Name: "Effect", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
			attempts++
			if attempts == 1 {
				clock.Advance(100 * time.Second)
				return tools.ExecutionResult{}, tools.NewRetryableExecutionError(errors.New("retry safe, no effect"))
			}
			if attempts == 2 {
				clock.Advance(31 * time.Second)
			}
			physicalEffects++ // real side effect while caller lease might have expired
			return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: req.Call.ID, Disposition: "committed"}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	mkRunner := func(owner string) *agent.Runner {
		t.Helper()
		runner, err := agent.NewRunner(agent.Dependencies{
			Runtime: rt, Model: auditRetryThenEffectModel{}, Catalog: catalog, Executor: catalog,
			Clock:        clock,
			RunExecution: &agent.RunExecution{OwnerID: owner, LeaseDuration: 2 * time.Minute},
			ToolOutcomeVerifier: agent.ToolOutcomeVerifierFunc(func(_ context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
				// Simulate the provider-owned receipt lookup, NOT user input.
				if req.RunID != "tool-retry-expiry" || req.Call.ID != "effect-tool" || physicalEffects != 1 {
					return tools.ExecutionResult{}, errors.New("provider does not confirm this COMMIT")
				}
				return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
					Receipt: tools.Receipt{ExecutionID: req.Call.ID, Disposition: "committed"}}, nil
			}),
			Execution: agent.ExecutionPolicy{Tool: agent.CallPolicy{MaxAttempts: 2, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return runner
	}
	first := mkRunner("first-owner")
	run, err := first.StartRun(t.Context(), agent.StartRequest{
		ID: "tool-retry-expiry", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "effect", ToolKeys: []string{"effect"},
	})
	if err == nil {
		t.Fatal("expected CAS failure after first physical external commit and Store lease expiry")
	}
	if !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("expected stale fencing after effect, got %v", err)
	}
	if physicalEffects != 1 || attempts != 2 {
		t.Fatalf("first segment expected 1 physical effect after two invocations: attempts=%d effects=%d", attempts, physicalEffects)
	}
	resumeCandidate, err := rt.Load(t.Context(), run.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	next := mkRunner("second-owner")
	resumed, resumeErr := next.Resume(t.Context(), run.Run.ID, resumeCandidate.Run.Revision)
	t.Logf("AUDIT TOOL leaseTTL=120s external attempt1+attempt2=131s; effects-before=%d effects-after=%d attempted=%d errorFirst=%v resumed=%s resumedErr=%v",
		1, physicalEffects, attempts, err, resumed.Run.Status, resumeErr)
	if !errors.Is(resumeErr, agent.ErrToolOutcomeUnknown) || physicalEffects != 1 {
		t.Fatalf("ambiguous physical Tool was replayed: effects=%d err=%v", physicalEffects, resumeErr)
	}
	if _, err := next.ReconcileToolOutcome(t.Context(), run.Run.ID,
		resumeCandidate.Run.Revision, "wrong-call"); !errors.Is(err, agent.ErrToolOutcomeUnknown) {
		t.Fatalf("wrong CallID reached provider verifier: %v", err)
	}
	reconciled, err := next.ReconcileToolOutcome(t.Context(), run.Run.ID,
		resumeCandidate.Run.Revision, "effect-tool")
	if err != nil || reconciled.Run.Revision != resumeCandidate.Run.Revision+1 {
		t.Fatalf("known Tool receipt could not be reconciled: %+v %v", reconciled.Run, err)
	}
	if physicalEffects != 1 {
		t.Fatalf("reconciliation re-executed physical Tool: %d", physicalEffects)
	}
	done, err := next.Resume(t.Context(), run.Run.ID, reconciled.Run.Revision)
	if err != nil || done.Run.Status != kernel.RunStatusCompleted || physicalEffects != 1 {
		t.Fatalf("Run could not finish from reconciled receipt: %+v effects=%d err=%v", done.Run, physicalEffects, err)
	}
}

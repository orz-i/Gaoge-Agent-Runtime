package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// A legacy unleased Agent currently invokes physical Tools before committing
// an execution intent. Losing its Tool receipt then lets a restarted Runner
// dispatch the exact same business Tool a second time.
func TestUnleasedToolReceiptLossCannotDispatchExternalEffectAgain(t *testing.T) {
	backing := memory.NewStore()
	faults := &modelInvocationFaultStore{
		Store: backing, eventType: "tool.completed", afterCommit: false,
	}
	provider := &autonomousContinuationModel{}
	effects := 0
	registry := autonomousContinuationRegistry(t, &effects)
	first := newAutonomousAgentRunner(t, faults, provider, registry)
	request := startRequest(
		"unleased-crash-tool-receipt", "unleased-crash-request", "publish",
		"test.autonomous",
	)
	_, firstErr := first.StartRun(t.Context(), request)
	if !errors.Is(firstErr, errInjectedModelInvocationCrash) {
		t.Fatalf("receipt failure not injected: %v", firstErr)
	}
	if effects != 1 || provider.calls != 1 {
		t.Fatalf("fixture expected one physical Tool and one Model: effects=%d model=%d", effects, provider.calls)
	}
	runtime := newModelInvocationRuntime(t, backing)
	pending, err := runtime.Load(t.Context(), request.ID)
	if err != nil || pending.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("persisted pending Run unavailable: status=%s err=%v", pending.Run.Status, err)
	}
	restart := newAutonomousAgentRunnerWithRuntime(t, runtime, provider, registry)
	_, resumeErr := restart.Resume(t.Context(), request.ID, pending.Run.Revision)
	if !errors.Is(resumeErr, agent.ErrToolOutcomeUnknown) || effects != 1 ||
		provider.calls != 1 {
		t.Fatalf("unverified Tool replay after crash: err=%v effects=%d modelCalls=%d", resumeErr, effects, provider.calls)
	}
	persisted, err := runtime.Load(t.Context(), request.ID)
	if err != nil || persisted.Run.Revision != pending.Run.Revision ||
		persisted.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("unverified effect altered persisted intent: before=%+v after=%+v err=%v",
			pending.Run, persisted.Run, err)
	}
}

// A direct Agent without RunExecution must use the SAME trusted, read-only
// verifier and exact Call ID as a lease-backed Agent. No external Tool handler
// execution occurs during recovery, even if the owner reopens after a crash.
func TestUnleasedToolReceiptLossReconcilesExactVerifiedCommit(t *testing.T) {
	backing := memory.NewStore()
	faults := &modelInvocationFaultStore{Store: backing, eventType: "tool.completed"}
	provider := &autonomousContinuationModel{}
	effects, verifiedReads := 0, 0
	registry := autonomousContinuationRegistry(t, &effects)
	first := newAutonomousAgentRunner(t, faults, provider, registry)
	request := startRequest("unleased-verified-commit", "request-verify-commit", "publish", "test.autonomous")
	_, firstErr := first.StartRun(t.Context(), request)
	if !errors.Is(firstErr, errInjectedModelInvocationCrash) || effects != 1 {
		t.Fatalf("initial committed-effect/lost-receipt fixture failed: err=%v effects=%d", firstErr, effects)
	}
	runtime := newModelInvocationRuntime(t, backing)
	before, err := runtime.Load(t.Context(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	mkRunner := func(receiptID string) *agent.Runner {
		t.Helper()
		runner, runnerErr := agent.NewRunner(agent.Dependencies{
			Runtime: runtime, Model: provider, Catalog: registry, Executor: registry,
			Clock: modelInvocationClock{},
			ToolOutcomeVerifier: agent.ToolOutcomeVerifierFunc(func(_ context.Context, call tools.ExecutionRequest) (tools.ExecutionResult, error) {
				verifiedReads++
				if call.RunID != request.ID || call.Call.ID != "autonomous-call" {
					return tools.ExecutionResult{}, errors.New("wrong provider identity")
				}
				return tools.ExecutionResult{
					Content: json.RawMessage(`{"ok":true}`),
					Receipt: tools.Receipt{ExecutionID: receiptID, Disposition: "committed"},
				}, nil
			}),
		})
		if runnerErr != nil {
			t.Fatal(runnerErr)
		}
		return runner
	}
	_, err = mkRunner("wrong-call").ReconcileToolOutcome(t.Context(), before.Run.ID,
		before.Run.Revision, "autonomous-call")
	if !errors.Is(err, agent.ErrToolReceiptMismatch) || effects != 1 {
		t.Fatalf("unverified outcome accepted: err=%v effects=%d", err, effects)
	}
	persisted, err := runtime.Load(t.Context(), before.Run.ID)
	if err != nil || persisted.Run.Revision != before.Run.Revision {
		t.Fatalf("rejected verifier advanced state: before=%+v after=%+v err=%v", before.Run, persisted.Run, err)
	}
	reconciled, err := mkRunner("autonomous-call").ReconcileToolOutcome(t.Context(), before.Run.ID,
		before.Run.Revision, "autonomous-call")
	if err != nil || reconciled.Run.Revision != before.Run.Revision+1 || effects != 1 {
		t.Fatalf("direct read-only COMMIT reconciliation failed: %+v err=%v effects=%d", reconciled.Run, err, effects)
	}
	finished, err := mkRunner("autonomous-call").Resume(t.Context(), request.ID, reconciled.Run.Revision)
	if err != nil || finished.Run.Status != kernel.RunStatusCompleted ||
		effects != 1 || provider.calls != 2 || verifiedReads != 2 {
		t.Fatalf("verified receipt was not consumed without replay: status=%s err=%v effects=%d modelCalls=%d reads=%d",
			finished.Run.Status, err, effects, provider.calls, verifiedReads)
	}
}

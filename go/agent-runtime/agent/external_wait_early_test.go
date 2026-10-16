package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// An external Provider may signal readiness synchronously DURING Tool.Execute,
// after the native dispatch intent is committed but BEFORE ToolWait is stored.
// A host must receive a RETRYABLE refusal rather than a generic conflict;
// it must keep its own durable signal until the wait is committed and replay
// the exact same identity. No second provider Tool execution is allowed.
func TestNativeExternalSignalBeforeToolWaitIsRetryable(t *testing.T) {
	ctx := t.Context()
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	signal := agent.ExternalToolResultSignal{
		RunID: "native-early-signal", CallID: "external-call", OperationID: "provider-early",
		InputDigest: strings.Repeat("a", 64), SignalID: "early-signal-1",
	}
	var runner *agent.Runner
	var earlyErr error
	var beforePending kernel.Snapshot
	effects := 0
	catalog, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: "test.external", Name: "External", InputSchema: json.RawMessage(`{"type":"object"}`), Terminal: true},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			effects++
			beforePending, earlyErr = runner.NotifyExternalToolResultReady(ctx, signal)
			until := time.Now().UTC().Add(time.Minute)
			return tools.ExecutionResult{Content: json.RawMessage(`null`),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: tools.ReceiptDispositionPending},
				Pending: &tools.PendingExecution{Kind: tools.PendingExternalResult,
					OperationID: signal.OperationID, InputDigest: signal.InputDigest, DeadlineAt: &until}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err = agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: &externalModel{}, Catalog: catalog, Executor: catalog,
		RunExecution: &agent.RunExecution{OwnerID: "native", LeaseDuration: 2 * time.Minute},
		ToolOutcomeVerifier: agent.ToolOutcomeVerifierFunc(func(_ context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
			if effects != 1 || r.RunID != signal.RunID || r.Call.ID != signal.CallID {
				return tools.ExecutionResult{}, errors.New("no verified provider COMMIT")
			}
			return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: r.Call.ID, Disposition: "committed"}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runner.StartRun(ctx, agent.StartRequest{
		ID: signal.RunID, Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "ready before wait", ToolKeys: []string{"test.external"},
	})
	if err != nil || effects != 1 || started.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("early signal fixture failed %+v effects=%d err=%v", started.Run, effects, err)
	}
	if !errors.Is(earlyErr, agent.ErrToolSignalNotReady) ||
		beforePending.Run.Revision == 0 || beforePending.Run.Revision >= started.Run.Revision {
		t.Fatalf("host was not told to retry before persisted wait: early=%v before=%d after=%d", earlyErr, beforePending.Run.Revision, started.Run.Revision)
	}
	// Before the persisted wait exists, other unrelated signals are still
	// rejected as conflicts, not ambiguously marked retryable.
	wrong := signal
	wrong.CallID = "other-call"
	_, err = runner.NotifyExternalToolResultReady(ctx, wrong)
	if !errors.Is(err, agent.ErrToolSignalConflict) {
		t.Fatalf("unrelated signal classified as matching early call: %v", err)
	}
	ready, err := runner.NotifyExternalToolResultReady(ctx, signal)
	if err != nil || ready.Run.Revision != started.Run.Revision+1 {
		t.Fatalf("host could not redeliver exact signal: %+v %v", ready.Run, err)
	}
	duplicate, err := runner.NotifyExternalToolResultReady(ctx, signal)
	if err != nil || duplicate.Run.Revision != ready.Run.Revision {
		t.Fatalf("host duplicate redelivery advanced Run: %+v %v", duplicate.Run, err)
	}
	done, err := runner.ReconcileToolOutcome(ctx, ready.Run.ID, ready.Run.Revision, signal.CallID)
	if err != nil || done.Run.Status != kernel.RunStatusCompleted || effects != 1 {
		t.Fatalf("external Tool was replayed or never reconciled %+v effects=%d err=%v", done.Run, effects, err)
	}
}

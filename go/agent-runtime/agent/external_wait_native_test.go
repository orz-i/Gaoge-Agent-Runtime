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

// Native external-result notification must be durable without using legacy
// Store.Apply, bypassing execution epochs, or dispatching a second Tool.
func TestNativeExternalResultSignalIsGuardedAndIdempotent(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	effects, lookups := 0, 0
	confirmed := false
	catalog, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: "test.external", Name: "External", InputSchema: json.RawMessage(`{"type":"object"}`), Terminal: true},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			effects++
			deadline := time.Now().Add(time.Minute)
			return tools.ExecutionResult{Content: json.RawMessage(`null`),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: tools.ReceiptDispositionPending},
				Pending: &tools.PendingExecution{Kind: tools.PendingExternalResult, OperationID: "provider-op-1",
					InputDigest: strings.Repeat("a", 64), DeadlineAt: &deadline}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: &externalModel{}, Catalog: catalog, Executor: catalog,
		RunExecution: &agent.RunExecution{OwnerID: "signal-owner", LeaseDuration: 2 * time.Minute},
		ToolOutcomeVerifier: agent.ToolOutcomeVerifierFunc(func(_ context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
			lookups++
			if !confirmed {
				return tools.ExecutionResult{}, errors.New("provider receipt is not yet committed")
			}
			return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: req.Call.ID, Disposition: "committed"}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := runner.StartRun(t.Context(), agent.StartRequest{
		ID: "native-external-signal", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "external", ToolKeys: []string{"test.external"},
	})
	if err != nil || waiting.Run.Status != kernel.RunStatusRunning || effects != 1 {
		t.Fatalf("native pending startup: %+v physical=%d err=%v", waiting.Run, effects, err)
	}
	view, err := runner.QueryPendingTool(t.Context(), waiting.Run.ID, "external-call")
	if err != nil || view.Generation == 0 {
		t.Fatalf("missing durable external wait: %+v %v", view, err)
	}
	signal := agent.ExternalToolResultSignal{
		RunID: waiting.Run.ID, CallID: "external-call", OperationID: "provider-op-1",
		InputDigest: strings.Repeat("a", 64), SignalID: "signal-1",
	}
	ready, err := runner.NotifyExternalToolResultReady(t.Context(), signal)
	if err != nil || ready.Run.Revision != waiting.Run.Revision+1 {
		t.Fatalf("native external ready signal not committed: %+v %v", ready.Run, err)
	}
	identical, err := runner.NotifyExternalToolResultReady(t.Context(), signal)
	if err != nil || identical.Run.Revision != ready.Run.Revision {
		t.Fatalf("duplicate external signal advanced Run: %+v %v", identical.Run, err)
	}
	other := signal
	other.SignalID = "different"
	if _, err = runner.NotifyExternalToolResultReady(t.Context(), other); !errors.Is(err, agent.ErrToolSignalConflict) {
		t.Fatalf("stale conflicting signal accepted: %v", err)
	}
	if _, err = runner.ResumeToolWait(t.Context(), ready.Run.ID, ready.Run.Revision, view.Generation); !errors.Is(err, agent.ErrToolOutcomeUnknown) || effects != 1 {
		t.Fatalf("native ready notification executed pending Tool: effects=%d err=%v", effects, err)
	}
	confirmed = true
	done, err := runner.ReconcileToolOutcome(t.Context(), ready.Run.ID, ready.Run.Revision, "external-call")
	if err != nil || done.Run.Status != kernel.RunStatusCompleted || effects != 1 || lookups != 1 {
		t.Fatalf("verified external ready receipt did not complete: %+v effects=%d lookups=%d err=%v", done.Run, effects, lookups, err)
	}
}

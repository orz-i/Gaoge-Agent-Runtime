package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Stage 2C's first slice verifies native external-result readiness using
// independent real PostgreSQL connections, not GORM's SQLite dialect.
func TestRealPostgresNativeExternalSignalAndVerifiedReceipt(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	db, isolation := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	effects, modelCalls := 0, 0
	var earlyErr error
	var earlyRevision uint64
	mk := func(store *KernelStore, owner string) (*agent.Runner, *kernel.Runtime) {
		t.Helper()
		rt, err := kernel.New(kernel.Dependencies{Store: store})
		if err != nil {
			t.Fatal(err)
		}
		var runner *agent.Runner
		registry, err := tools.NewRegistry([]tools.Registration{{
			Definition: tools.Definition{Key: "lookup", Name: "Lookup", InputSchema: json.RawMessage(`{"type":"object"}`), Terminal: true},
			Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
				effects++
				if owner == "starter" {
					// Real PostgreSQL transaction: prepared dispatch intent is
					// durable, but ToolWait is not yet committed.
					signal := agent.ExternalToolResultSignal{
						RunID: request.RunID, CallID: request.Call.ID, OperationID: "real-pg-op",
						InputDigest: strings.Repeat("c", 64), SignalID: "signal-1",
					}
					early, sendErr := runner.NotifyExternalToolResultReady(t.Context(), signal)
					earlyErr = sendErr
					earlyRevision = early.Run.Revision
					if !errors.Is(sendErr, agent.ErrToolSignalNotReady) {
						return tools.ExecutionResult{}, errors.New("early PostgreSQL signal wasn't retryable")
					}
				}
				until := time.Now().UTC().Add(2 * time.Minute)
				return tools.ExecutionResult{Content: json.RawMessage(`null`),
					Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: tools.ReceiptDispositionPending},
					Pending: &tools.PendingExecution{Kind: tools.PendingExternalResult, OperationID: "real-pg-op",
						InputDigest: strings.Repeat("c", 64), DeadlineAt: &until}}, nil
			}),
		}})
		if err != nil {
			t.Fatal(err)
		}
		runner, err = agent.NewRunner(agent.Dependencies{
			Runtime: rt, Model: nativePostgresModel{calls: &modelCalls}, Catalog: registry, Executor: registry,
			RunExecution: &agent.RunExecution{OwnerID: owner, LeaseDuration: 2 * time.Minute},
			ToolOutcomeVerifier: agent.ToolOutcomeVerifierFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
				if request.Call.ID != "real-pg-tool" || request.RunID != "pg-native-external" || effects != 1 {
					return tools.ExecutionResult{}, errors.New("no verified external provider result")
				}
				return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
					Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
			}),
		})
		if err != nil {
			t.Fatal(err)
		}
		return runner, rt
	}
	starter, _ := mk(NewKernelStore(db), "starter")
	wait, err := starter.StartRun(t.Context(), agent.StartRequest{
		ID: "pg-native-external", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "native signal + external receipt", ToolKeys: []string{"lookup"},
	})
	if err != nil || wait.Run.Status != kernel.RunStatusRunning || effects != 1 {
		t.Fatalf("PG pending: %+v %v effects=%d", wait.Run, err, effects)
	}
	if !errors.Is(earlyErr, agent.ErrToolSignalNotReady) || earlyRevision == 0 || earlyRevision >= wait.Run.Revision {
		t.Fatalf("real PG early signal wasn't returned for retry: early=%v revision=%d pendingRevision=%d",
			earlyErr, earlyRevision, wait.Run.Revision)
	}
	notifier, rt := mk(NewKernelStore(reopenRecoveryPostgres(t, isolation)), "notifier")
	signal := agent.ExternalToolResultSignal{RunID: wait.Run.ID, CallID: "real-pg-tool", OperationID: "real-pg-op",
		InputDigest: strings.Repeat("c", 64), SignalID: "signal-1"}
	notified, err := notifier.NotifyExternalToolResultReady(t.Context(), signal)
	if err != nil || notified.Run.Revision != wait.Run.Revision+1 {
		t.Fatalf("PG native guarded signal: %+v %v", notified.Run, err)
	}
	if err = rt.RequireExecution(t.Context(), wait.Run.ID); !errors.Is(err, kernel.ErrExecutionRequired) {
		t.Fatalf("signal did not preserve native sticky fence: %v", err)
	}
	reconciler, _ := mk(NewKernelStore(reopenRecoveryPostgres(t, isolation)), "reconciler")
	done, err := reconciler.ReconcileToolOutcome(t.Context(), notified.Run.ID, notified.Run.Revision, "real-pg-tool")
	if err != nil || done.Run.Status != kernel.RunStatusCompleted || effects != 1 || modelCalls != 1 {
		t.Fatalf("PG verified receipt after signal failed: %+v effects=%d model=%d err=%v",
			done.Run, effects, modelCalls, err)
	}
}

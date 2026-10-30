package continuatione2e

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	postgresadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type customRealPreflightCancellationRecorder struct {
	runtime *kernel.Runtime
	calls   atomic.Int32
}

func (c *customRealPreflightCancellationRecorder) Cancel(
	ctx context.Context, runID string, revision uint64, reason string,
) (kernel.Snapshot, error) {
	c.calls.Add(1)
	return c.runtime.Cancel(ctx, runID, revision, reason)
}

// The first Kernel Load is owned by Harness, before it calls either native
// or custom RuntimeCanceller. A disconnect at this read must retire only
// the proven no-effect intent; the fresh caller may then safely cancel.
func TestRealPostgresCustomCancellerPreflightReadRecoveryAcrossConnections(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	for _, recovery := range []string{"same-connection", "new-connection"} {
		t.Run(recovery, func(t *testing.T) {
			db, isolated := managedRecoveryDatabase(t, dsn)
			if err := postgresadapter.Migrate(db); err != nil {
				t.Fatal(err)
			}
			if err := harnesspostgres.Migrate(db); err != nil {
				t.Fatal(err)
			}
			physical := &realCancelledRequestKernelStore{
				KernelStore: postgresadapter.NewKernelStore(db), readAt: 1,
			}
			first, hostStore, runtime := newRealCancelledRequestHost(t, db, physical)
			request := harness.StartRequest{
				HostThread:    harness.HostRef{Kind: "conversation", ID: "custom-cancel-preflight"},
				HostTurn:      harness.HostRef{Kind: "conversation_turn", ID: "custom-cancel-preflight"},
				Actor:         kernel.ActorRef{TenantID: "test", ActorID: "agent"},
				Thread:        kernel.ThreadRef{Kind: "conversation", ID: "custom-cancel-preflight"},
				Goal:          "pause then cancel without custom dispatch",
				Config:        harness.ConfigSnapshot{Model: "fixture-control"},
				CallAllowance: &agent.CallAllowance{LLMCalls: 1},
			}
			paused, err := first.StartAgentTurn(t.Context(), request)
			if err != nil || paused.Turn.Status != harness.TurnPausedBudget {
				t.Fatalf("could not create paused Agent: status=%s err=%v", paused.Turn.Status, err)
			}
			owner, ok := harness.TopLevelInvocation(paused)
			if !ok {
				t.Fatal("missing root Invocation")
			}
			native, err := agent.NewRunner(agent.Dependencies{
				Runtime: runtime, Model: realControlResponseModel{},
				CompletionPolicies: []agent.CompletionPolicy{&realControlPauseOnce{}},
				Limits:             agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 2},
			})
			if err != nil {
				t.Fatal(err)
			}
			custom := &customRealPreflightCancellationRecorder{runtime: runtime}
			withCustom, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: native, Store: hostStore,
				Clock: managedRecoveryClock{}, Cancellation: custom,
			})
			if err != nil {
				t.Fatal(err)
			}
			disconnected, abort := context.WithCancel(t.Context())
			defer abort()
			physical.cancel = abort
			physical.armed.Store(true)
			_, firstErr := withCustom.Cancel(disconnected, paused.Turn.ID, "disconnected before callback")
			physical.armed.Store(false)
			if !errors.Is(firstErr, context.Canceled) || custom.calls.Load() != 0 ||
				physical.controlCalls.Load() != 0 || physical.reads.Load() < 1 {
				t.Fatalf("first request failed the pre-dispatch no-effect boundary: err=%v customCalls=%d physicalCAS=%d reads=%d",
					firstErr, custom.calls.Load(), physical.controlCalls.Load(), physical.reads.Load())
			}
			recovered, err := runtime.Load(t.Context(), owner.ExecutionRefID)
			if err != nil || recovered.Run.Status != kernel.RunStatusPausedBudget {
				t.Fatalf("first disconnected request changed Kernel: status=%s err=%v", recovered.Run.Status, err)
			}
			second := withCustom
			secondStore := hostStore
			secondRuntime := runtime
			if recovery == "new-connection" {
				db2 := openManagedRecoveryDB(t, isolated)
				second, secondStore, secondRuntime = newRealControlHost(t, db2, nil)
			}
			items, err := secondStore.ListItems(t.Context(), paused.Turn.ID, 0, 500)
			if err != nil {
				t.Fatal(err)
			}
			foundIntent := false
			for _, item := range items {
				if item.ParentItemID == harness.CancellationFenceParentItemID &&
					item.Kind == harness.ItemDiagnostic && item.Status == harness.ItemStarted {
					foundIntent = true
					break
				}
			}
			if !foundIntent {
				t.Fatal("first request did not commit a durable cancellation intent")
			}
			if harness.ActiveCancellationFence(items) {
				t.Fatal("proven pre-custom read failure stranded SQL cancellation intent")
			}
			cancelled, err := second.Cancel(t.Context(), paused.Turn.ID, "fresh authorized cancellation")
			if err != nil || cancelled.Turn.Status != harness.TurnCancelled {
				t.Fatalf("new cancellation blocked: status=%s err=%v", cancelled.Turn.Status, err)
			}
			final, err := secondRuntime.Load(t.Context(), owner.ExecutionRefID)
			if err != nil || final.Run.Status != kernel.RunStatusCancelled {
				t.Fatalf("authoritative Kernel Run not cancelled: status=%s err=%v", final.Run.Status, err)
			}
			expectedCustomCalls := int32(0)
			if recovery == "same-connection" {
				expectedCustomCalls = 1
			}
			if custom.calls.Load() != expectedCustomCalls {
				t.Fatalf("unexpected physical custom Canceller calls: got=%d want=%d",
					custom.calls.Load(), expectedCustomCalls)
			}
		})
	}
}

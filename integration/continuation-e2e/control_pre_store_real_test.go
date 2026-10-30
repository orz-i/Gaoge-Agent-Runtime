package continuatione2e

import (
	"errors"
	"os"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	postgresadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// The third Kernel read is inside Runtime.ApplyControl, after the native
// Agent has finished its own preflight but BEFORE entering the Store's
// write-capable ApplyControl transaction. A new PostgreSQL connection must
// observe the no-effect completion and safely retry or cancel the same Turn.
func TestRealPostgresKernelControlPreStoreReadRecoveryAcrossConnections(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	for _, recovery := range []string{"retry", "cancel"} {
		t.Run(recovery, func(t *testing.T) {
			db, isolated := managedRecoveryDatabase(t, dsn)
			if err := postgresadapter.Migrate(db); err != nil {
				t.Fatal(err)
			}
			if err := harnesspostgres.Migrate(db); err != nil {
				t.Fatal(err)
			}
			fault := &realControlReadFault{KernelStore: postgresadapter.NewKernelStore(db)}
			fault.failAt.Store(3)
			first, _, _ := newRealControlHost(t, db, fault)
			request := harness.StartRequest{
				HostThread:    harness.HostRef{Kind: "conversation", ID: "kernel-control-pre-store"},
				HostTurn:      harness.HostRef{Kind: "conversation_turn", ID: "kernel-control-pre-store"},
				Actor:         kernel.ActorRef{TenantID: "test", ActorID: "agent"},
				Thread:        kernel.ThreadRef{Kind: "conversation", ID: "kernel-control-pre-store"},
				Goal:          "continue after Kernel read failure",
				Config:        harness.ConfigSnapshot{Model: "fixture-control"},
				CallAllowance: &agent.CallAllowance{LLMCalls: 1},
			}
			paused, err := first.StartAgentTurn(t.Context(), request)
			if err != nil || paused.Turn.Status != harness.TurnPausedBudget {
				t.Fatalf("initial Agent not paused: status=%s err=%v", paused.Turn.Status, err)
			}
			owner, ok := harness.TopLevelInvocation(paused)
			if !ok {
				t.Fatal("missing root Invocation")
			}
			fault.armed.Store(true)
			_, failure := first.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision,
				agent.CallAllowance{LLMCalls: 1})
			fault.armed.Store(false)
			if fault.reads.Load() != 3 || !errors.Is(failure, errRealControlReadFault) ||
				!errors.Is(failure, agent.ErrControlNoEffect) {
				t.Fatalf("third Kernel read not classified as native no-effect: reads=%d err=%v",
					fault.reads.Load(), failure)
			}
			// The second Runner owns no process memory from the first
			// Runner. Persisted SQL is its only dispatch/cancellation truth.
			secondDB := openManagedRecoveryDB(t, isolated)
			second, store, runtime := newRealControlHost(t, secondDB, nil)
			current, err := runtime.Load(t.Context(), owner.ExecutionRefID)
			if err != nil || current.Run.Status != kernel.RunStatusPausedBudget {
				t.Fatalf("Kernel changed before any Store CAS: status=%s err=%v", current.Run.Status, err)
			}
			items, err := store.ListItems(t.Context(), paused.Turn.ID, 0, 500)
			if err != nil {
				t.Fatal(err)
			}
			if harness.PendingExecutionDispatch(items, owner.ID) {
				t.Fatal("new PostgreSQL connection observed an unsettled proven no-effect grant")
			}
			switch recovery {
			case "retry":
				resumed, retryErr := second.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision,
					agent.CallAllowance{LLMCalls: 1})
				if retryErr != nil || resumed.Turn.Status != harness.TurnRunning {
					t.Fatalf("valid allowance remained blocked: status=%s err=%v", resumed.Turn.Status, retryErr)
				}
			case "cancel":
				cancelled, cancelErr := second.Cancel(t.Context(), paused.Turn.ID,
					"operator cancels after Kernel pre-Store read outage")
				if cancelErr != nil || cancelled.Turn.Status != harness.TurnCancelled {
					t.Fatalf("operator cancellation remained blocked: status=%s err=%v", cancelled.Turn.Status, cancelErr)
				}
			}
			final, err := runtime.Load(t.Context(), owner.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			expected := kernel.RunStatusRunning
			if recovery == "cancel" {
				expected = kernel.RunStatusCancelled
			}
			if final.Run.Status != expected {
				t.Fatalf("final Kernel status=%s expected=%s", final.Run.Status, expected)
			}
		})
	}
}

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

// The same native pre-Store proof which allows a paused Agent's allowance
// retry must allow cancellation admission to close when cancellation itself
// failed before any Kernel Store CAS. The second Runner has a separate
// PostgreSQL connection, Kernel Runtime, and Agent instance.
func TestRealPostgresNativeNoEffectCancelAcrossConnections(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	for _, scenario := range []struct {
		name               string
		read               int32
		separateConnection bool
	}{
		{"harness-first-read/same-connection", 1, false},
		{"harness-first-read/separate-connection", 1, true},
		{"kernel-cancel-second-read/same-connection", 2, false},
		{"kernel-cancel-second-read/separate-connection", 2, true},
		{"kernel-apply-control-third-read/same-connection", 3, false},
		{"kernel-apply-control-third-read/separate-connection", 3, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db, isolated := managedRecoveryDatabase(t, dsn)
			if err := postgresadapter.Migrate(db); err != nil {
				t.Fatal(err)
			}
			if err := harnesspostgres.Migrate(db); err != nil {
				t.Fatal(err)
			}
			fault := &realControlReadFault{KernelStore: postgresadapter.NewKernelStore(db)}
			fault.failAt.Store(scenario.read)
			first, firstStore, firstRuntime := newRealControlHost(t, db, fault)
			request := harness.StartRequest{
				HostThread:    harness.HostRef{Kind: "conversation", ID: "native-noeffect-cancel"},
				HostTurn:      harness.HostRef{Kind: "conversation_turn", ID: "native-noeffect-cancel"},
				Actor:         kernel.ActorRef{TenantID: "test", ActorID: "agent"},
				Thread:        kernel.ThreadRef{Kind: "conversation", ID: "native-noeffect-cancel"},
				Goal:          "continue after cancellation database read error",
				Config:        harness.ConfigSnapshot{Model: "fixture-control"},
				CallAllowance: &agent.CallAllowance{LLMCalls: 1},
			}
			paused, err := first.StartAgentTurn(t.Context(), request)
			if err != nil || paused.Turn.Status != harness.TurnPausedBudget {
				t.Fatalf("Agent is not paused: status=%s err=%v", paused.Turn.Status, err)
			}
			owner, ok := harness.TopLevelInvocation(paused)
			if !ok {
				t.Fatal("missing durable root Invocation")
			}
			fault.armed.Store(true)
			_, firstErr := first.Cancel(t.Context(), paused.Turn.ID,
				"cancel during Kernel pre-Store read outage")
			fault.armed.Store(false)
			if fault.reads.Load() < scenario.read || !errors.Is(firstErr, errRealControlReadFault) ||
				(scenario.read >= 2 && !kernel.IsControlNoEffect(firstErr)) ||
				(scenario.read == 1 && kernel.IsControlNoEffect(firstErr)) {
				t.Fatalf("cancellation did not retain the correct read-only proof: stage=%d reads=%d err=%v",
					scenario.read, fault.reads.Load(), firstErr)
			}
			current, err := firstRuntime.Load(t.Context(), owner.ExecutionRefID)
			if err != nil || current.Run.Status != kernel.RunStatusPausedBudget {
				t.Fatalf("first cancellation changed Kernel despite read failure: status=%s err=%v",
					current.Run.Status, err)
			}
			second := first
			secondStore := firstStore
			secondRuntime := firstRuntime
			if scenario.separateConnection {
				secondDB := openManagedRecoveryDB(t, isolated)
				second, secondStore, secondRuntime = newRealControlHost(t, secondDB, nil)
			}
			items, err := secondStore.ListItems(t.Context(), paused.Turn.ID, 0, 500)
			if err != nil {
				t.Fatal(err)
			}
			if harness.ActiveCancellationFence(items) {
				t.Fatal("a proven no-effect native cancellation retained its SQL admission intent")
			}
			cancelled, err := second.Cancel(t.Context(), paused.Turn.ID,
				"operator retries proven no-effect cancellation")
			if err != nil || cancelled.Turn.Status != harness.TurnCancelled {
				t.Fatalf("second cancellation failed: status=%s err=%v", cancelled.Turn.Status, err)
			}
			final, err := secondRuntime.Load(t.Context(), owner.ExecutionRefID)
			if err != nil || final.Run.Status != kernel.RunStatusCancelled {
				t.Fatalf("authoritative Kernel Run not cancelled: status=%s err=%v", final.Run.Status, err)
			}
		})
	}
}

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
	"gorm.io/gorm"
)

// Simulate the client disconnecting at a precisely chosen Kernel read, after
// Harness has persisted its cancellation intent. No write-capable Store CAS
// is entered on any failing attempt.
type realCancelledRequestKernelStore struct {
	*postgresadapter.KernelStore
	armed        atomic.Bool
	readAt       int32
	reads        atomic.Int32
	controlCalls atomic.Int32
	cancel       context.CancelFunc
}

func (store *realCancelledRequestKernelStore) Load(ctx context.Context, runID string) (kernel.Snapshot, error) {
	if store.armed.Load() && store.reads.Add(1) == store.readAt {
		store.cancel()
		return kernel.Snapshot{}, ctx.Err()
	}
	return store.KernelStore.Load(ctx, runID)
}

func (store *realCancelledRequestKernelStore) ApplyControl(
	ctx context.Context, runID string, revision uint64,
	guard kernel.ControlGuard, mutation kernel.StoreMutation,
) (kernel.Snapshot, bool, error) {
	store.controlCalls.Add(1)
	return store.KernelStore.ApplyControl(ctx, runID, revision, guard, mutation)
}

func newRealCancelledRequestHost(
	t *testing.T, db *gorm.DB, fault *realCancelledRequestKernelStore,
) (*harness.Runner, *harnesspostgres.Store, *kernel.Runtime) {
	t.Helper()
	store, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := kernel.New(kernel.Dependencies{Store: fault})
	if err != nil {
		t.Fatal(err)
	}
	native, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: realControlResponseModel{},
		CompletionPolicies: []agent.CompletionPolicy{&realControlPauseOnce{}},
		Limits:             agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: native, Store: store, Clock: managedRecoveryClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner, store, runtime
}

func TestRealPostgresCancelledRequestNoEffectRecoveryAcrossConnections(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	for _, stage := range []int32{1, 2, 3} {
		for _, freshConnection := range []bool{false, true} {
			name := map[int32]string{
				1: "harness-first-read", 2: "kernel-cancel-second-read",
				3: "kernel-control-third-read",
			}[stage] + "/same-connection"
			if freshConnection {
				name = strings.TrimSuffix(name, "same-connection") + "new-connection"
			}
			t.Run(name, func(t *testing.T) {
				db, isolated := managedRecoveryDatabase(t, dsn)
				if err := postgresadapter.Migrate(db); err != nil {
					t.Fatal(err)
				}
				if err := harnesspostgres.Migrate(db); err != nil {
					t.Fatal(err)
				}
				fault := &realCancelledRequestKernelStore{
					KernelStore: postgresadapter.NewKernelStore(db), readAt: stage,
				}
				first, _, runtime := newRealCancelledRequestHost(t, db, fault)
				request := harness.StartRequest{
					HostThread:    harness.HostRef{Kind: "conversation", ID: "cancel-request-disconnected"},
					HostTurn:      harness.HostRef{Kind: "conversation_turn", ID: "cancel-request-disconnected"},
					Actor:         kernel.ActorRef{TenantID: "test", ActorID: "agent"},
					Thread:        kernel.ThreadRef{Kind: "conversation", ID: "cancel-request-disconnected"},
					Goal:          "continue after a disconnected cancellation",
					Config:        harness.ConfigSnapshot{Model: "fixture-control"},
					CallAllowance: &agent.CallAllowance{LLMCalls: 1},
				}
				paused, err := first.Start(t.Context(), request)
				if err != nil || paused.Turn.Status != harness.TurnPausedBudget {
					t.Fatalf("could not create paused Agent: status=%s err=%v", paused.Turn.Status, err)
				}
				owner, ok := harness.TopLevelInvocation(paused)
				if !ok {
					t.Fatal("no root Invocation")
				}
				requestCtx, cancelRequest := context.WithCancel(t.Context())
				defer cancelRequest()
				fault.cancel = cancelRequest
				fault.armed.Store(true)
				_, firstErr := first.Cancel(requestCtx, paused.Turn.ID, "request disconnected before cancellation CAS")
				fault.armed.Store(false)
				if !errors.Is(firstErr, context.Canceled) || fault.reads.Load() < stage ||
					fault.controlCalls.Load() != 0 {
					t.Fatalf("cancellation fault bypassed pre-Store boundary: stage=%d reads=%d calls=%d err=%v",
						stage, fault.reads.Load(), fault.controlCalls.Load(), firstErr)
				}
				if (stage == 1 && kernel.IsControlNoEffect(firstErr)) ||
					(stage > 1 && !kernel.IsControlNoEffect(firstErr)) {
					t.Fatalf("incorrect direct no-effect provenance: stage=%d err=%v", stage, firstErr)
				}
				unchanged, err := runtime.Load(t.Context(), owner.ExecutionRefID)
				if err != nil || unchanged.Run.Status != kernel.RunStatusPausedBudget {
					t.Fatalf("cancelled client executed physical control: status=%s err=%v", unchanged.Run.Status, err)
				}
				second := first
				var secondStore *harnesspostgres.Store
				var secondRuntime *kernel.Runtime
				if freshConnection {
					db2 := openManagedRecoveryDB(t, isolated)
					second, secondStore, secondRuntime = newRealControlHost(t, db2, nil)
				} else {
					secondStore, err = harnesspostgres.New(db)
					if err != nil {
						t.Fatal(err)
					}
					secondRuntime = runtime
				}
				items, err := secondStore.ListItems(t.Context(), paused.Turn.ID, 0, 500)
				if err != nil {
					t.Fatal(err)
				}
				if harness.ActiveCancellationFence(items) {
					t.Fatal("new request observed an unresolved no-effect cancellation intent")
				}
				retried, err := second.Cancel(t.Context(), paused.Turn.ID, "fresh request retries after disconnect")
				if err != nil || retried.Turn.Status != harness.TurnCancelled {
					t.Fatalf("fresh request cancellation blocked: status=%s err=%v", retried.Turn.Status, err)
				}
				run, err := secondRuntime.Load(t.Context(), owner.ExecutionRefID)
				if err != nil || run.Run.Status != kernel.RunStatusCancelled {
					t.Fatalf("authoritative Kernel Run did not cancel: status=%s err=%v", run.Run.Status, err)
				}
			})
		}
	}
}

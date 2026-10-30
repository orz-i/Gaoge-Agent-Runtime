package continuatione2e

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	postgresadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type realCancellationSettlementProbeStore struct {
	harness.Store
	interactions atomic.Int32
	onSecondRead func(context.Context, string) error
	receiptSeen  chan bool
}

func (s *realCancellationSettlementProbeStore) ListInteractions(
	ctx context.Context, turnID string,
) ([]harness.Interaction, error) {
	if s.interactions.Add(1) == 2 {
		return nil, s.onSecondRead(ctx, turnID)
	}
	return s.Store.ListInteractions(ctx, turnID)
}

func (s *realCancellationSettlementProbeStore) AppendItem(
	ctx context.Context, value harness.Item,
) (harness.Item, bool, error) {
	if value.Kind == harness.ItemDiagnostic && value.Status == harness.ItemCompleted &&
		strings.HasPrefix(value.ParentItemID, "hcancel_") {
		_, bounded := ctx.Deadline()
		select {
		case s.receiptSeen <- bounded:
		default:
		}
	}
	return s.Store.AppendItem(ctx, value)
}

// A real SQL Turn-row lock blocks a cancellation-intent completion transaction.
// The receipt must inherit a NEW bounded context rather than the original
// cancelled request context; with a prolonged lock, the attempt must time out
// without committing any completion Item or releasing admission authority.
func TestRealPostgresCancellationSettlementReceiptHasBoundedRowLockWait(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	for _, keepLocked := range []bool{false, true} {
		name := "released-before-deadline"
		if keepLocked {
			name = "held-past-deadline"
		}
		t.Run(name, func(t *testing.T) {
			db, isolated := managedRecoveryDatabase(t, dsn)
			if err := postgresadapter.Migrate(db); err != nil {
				t.Fatal(err)
			}
			if err := harnesspostgres.Migrate(db); err != nil {
				t.Fatal(err)
			}
			first, store, runtime := newRealControlHost(t, db, nil)
			started, err := first.Start(t.Context(), harness.StartRequest{
				HostThread:    harness.HostRef{Kind: "conversation", ID: "bounded-cancel-row-lock"},
				HostTurn:      harness.HostRef{Kind: "conversation_turn", ID: "bounded-cancel-row-lock"},
				Actor:         kernel.ActorRef{TenantID: "test", ActorID: "agent"},
				Thread:        kernel.ThreadRef{Kind: "conversation", ID: "bounded-cancel-row-lock"},
				Goal:          "recover after post-intent read-only outage",
				Config:        harness.ConfigSnapshot{Model: "fixture-control"},
				CallAllowance: &agent.CallAllowance{LLMCalls: 1},
			})
			if err != nil || started.Turn.Status != harness.TurnPausedBudget {
				t.Fatalf("could not start paused Agent: status=%s err=%v", started.Turn.Status, err)
			}
			root, ok := harness.TopLevelInvocation(started)
			if !ok {
				t.Fatal("missing root Invocation")
			}
			secondDB := openManagedRecoveryDB(t, isolated)
			lockedTransaction := secondDB.Begin()
			if lockedTransaction.Error != nil {
				t.Fatal(lockedTransaction.Error)
			}
			t.Cleanup(func() { _ = lockedTransaction.Rollback().Error })
			locked := make(chan struct{})
			probe := &realCancellationSettlementProbeStore{
				Store: store, receiptSeen: make(chan bool, 1),
			}
			requestCtx, cancelRequest := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancelRequest()
			probe.onSecondRead = func(_ context.Context, turnID string) error {
				if lockErr := lockedTransaction.Exec(
					"UPDATE agent_harness_turns SET revision = revision WHERE id = ?", turnID,
				).Error; lockErr != nil {
					return lockErr
				}
				close(locked)
				cancelRequest()
				return context.Canceled
			}
			direct, err := agent.NewRunner(agent.Dependencies{
				Runtime: runtime, Model: realControlResponseModel{},
				CompletionPolicies: []agent.CompletionPolicy{&realControlPauseOnce{}},
				Limits:             agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 2},
			})
			if err != nil {
				t.Fatal(err)
			}
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: direct, Store: probe, Clock: managedRecoveryClock{},
			})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, cancelErr := runner.Cancel(requestCtx, started.Turn.ID, "read-only claim error with row lock")
				done <- cancelErr
			}()
			select {
			case <-locked:
			case <-time.After(5 * time.Second):
				t.Fatal("did not acquire PostgreSQL Turn row lock during post-intent read")
			}
			select {
			case bounded := <-probe.receiptSeen:
				if !bounded {
					t.Fatal("cancellation completion wrote to PostgreSQL without a deadline")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancellation completion receipt was not attempted")
			}
			if keepLocked {
				// The original caller is already cancelled, but its
				// independent completion must continue for a MAXIMUM of
				// one recovery window, then return without lock release.
				select {
				case cancelErr := <-done:
					if !errors.Is(cancelErr, context.Canceled) ||
						!errors.Is(cancelErr, context.DeadlineExceeded) {
						t.Fatalf("row lock timeout did not remain fail-closed: %v", cancelErr)
					}
				case <-time.After(9 * time.Second):
					t.Fatal("cancellation completion exceeded its bounded deadline while lock held")
				}
			}
			if err := lockedTransaction.Rollback().Error; err != nil {
				t.Fatal(err)
			}
			if !keepLocked {
				select {
				case cancelErr := <-done:
					if !errors.Is(cancelErr, context.Canceled) ||
						errors.Is(cancelErr, context.DeadlineExceeded) {
						t.Fatalf("unblocked completion failed despite live recovery window: %v", cancelErr)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("unblocked completion did not finish before deadline")
				}
			}
			checkDB := openManagedRecoveryDB(t, isolated)
			_, checkStore, checkRuntime := newRealControlHost(t, checkDB, nil)
			items, err := checkStore.ListItems(t.Context(), started.Turn.ID, 0, 500)
			if err != nil {
				t.Fatal(err)
			}
			if harness.ActiveCancellationFence(items) != keepLocked {
				t.Fatalf("completion receipt released wrong authority after PostgreSQL lock: held=%v active=%v",
					keepLocked, harness.ActiveCancellationFence(items))
			}
			run, err := checkRuntime.Load(t.Context(), root.ExecutionRefID)
			if err != nil || run.Run.Status != kernel.RunStatusPausedBudget {
				t.Fatalf("read-only failure unexpectedly cancelled physical Kernel: status=%s err=%v",
					run.Run.Status, err)
			}
		})
	}
}

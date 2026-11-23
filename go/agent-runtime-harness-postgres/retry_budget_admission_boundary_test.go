package harnesspostgres_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

// A privileged Store.RetryInvocation is still a durable execution-authority
// boundary. No caller may rotate an Attempt and reopen its Turn while leaving
// a previously cancelled shared accounting ledger on the old generation.
// Check the same two sources on both Memory and SQL: the live ledger and the
// immutable frozen budget requirement, including missing-ledger recovery.
func TestRootRetryDirectStoreCannotBypassOriginalBudgetCAS(t *testing.T) {
	for name, create := range rootInvocationStores() {
		for _, mode := range []string{"persisted-ledger", "frozen-budget-without-ledger"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				store := create(t)
				ledgers, ok := store.(budget.LedgerStore)
				if !ok {
					t.Fatal("Harness Store did not expose its authoritative ledger")
				}
				now := time.Date(2026, 11, 19, 2, 0, 0, 0, time.UTC)
				scope := "budgeted-retry-" + name + "-" + mode
				configID := "legacy-unbudgeted-fixture"
				if mode == "frozen-budget-without-ledger" {
					sealed, err := harness.SealConfigSnapshot(scope, harness.ConfigSnapshot{
						Model: "test", SharedBudget: &budget.Limits{MaxLLMCalls: 2, MaxTotalTokens: 300},
					}, now)
					if err != nil {
						t.Fatal(err)
					}
					if _, fresh, err := store.PutConfigSnapshot(t.Context(), sealed); err != nil || !fresh {
						t.Fatalf("frozen shared-budget config: %t %v", fresh, err)
					}
					configID = sealed.ID
				}
				turn := harness.Turn{
					ID: scope, SessionID: "session", HostTurn: harness.HostRef{Kind: "message", ID: scope},
					ConfigSnapshotID: configID, Status: harness.TurnCancelled,
					Revision: 1, CreatedAt: now, UpdatedAt: now,
				}
				if _, fresh, err := store.CreateTurn(t.Context(), turn); err != nil || !fresh {
					t.Fatalf("original Turn: %t %v", fresh, err)
				}
				input := []byte(`{"goal":"original cancelled, no external retry"}`)
				digest := sha256.Sum256(input)
				root := harness.Invocation{
					ID: "root-" + scope, TurnID: turn.ID, CapabilityKey: harness.CapabilityAgent,
					ExecutionRefID: "original-" + scope,
					Input:          input, InputHash: hex.EncodeToString(digest[:]),
					Status: harness.InvocationCancelled, Attempt: 1, Revision: 1,
					CreatedAt: now, UpdatedAt: now, OutputRefs: []harness.HostRef{},
				}
				if _, fresh, err := store.CreateInvocation(t.Context(), root); err != nil || !fresh {
					t.Fatalf("original Invocation: %t %v", fresh, err)
				}
				var originalRevision uint64
				if mode == "persisted-ledger" {
					c := budget.Coordinator{Store: ledgers}
					if _, err := c.Ensure(t.Context(), scope, budget.Limits{MaxLLMCalls: 2}); err != nil {
						t.Fatal(err)
					}
					if _, err := c.RegisterRun(t.Context(), scope, root.ExecutionRefID, budget.RunBudget{}); err != nil {
						t.Fatal(err)
					}
					l, err := c.CancelRoot(t.Context(), scope, root.ExecutionRefID)
					if err != nil {
						t.Fatal(err)
					}
					originalRevision = l.Revision
				}
				_, err := store.RetryInvocation(t.Context(), root.ID, root.Revision, "forbidden-new-"+scope, now.Add(time.Second))
				if !errors.Is(err, harness.ErrConflict) {
					t.Fatalf("direct Store Retry bypassed budget authorization: %v", err)
				}
				persisted, err := store.GetInvocation(t.Context(), root.ID)
				if err != nil || persisted.Attempt != 1 || persisted.ExecutionRefID != root.ExecutionRefID ||
					persisted.Status != harness.InvocationCancelled || persisted.Revision != root.Revision {
					t.Fatalf("denied Retry mutated Invocation: %+v %v", persisted, err)
				}
				currentTurn, err := store.GetTurn(t.Context(), scope)
				if err != nil || currentTurn.Status != harness.TurnCancelled || currentTurn.Revision != turn.Revision {
					t.Fatalf("denied Retry reopened Turn: %+v %v", currentTurn, err)
				}
				if mode == "persisted-ledger" {
					l, err := ledgers.LoadBudget(t.Context(), scope)
					if err != nil || !l.Cancelled || l.Revision != originalRevision || len(l.RootRetries) != 0 {
						t.Fatalf("denied Retry changed original budget: %+v %v", l, err)
					}
				} else if _, err := ledgers.LoadBudget(t.Context(), scope); !errors.Is(err, budget.ErrLedgerNotFound) {
					t.Fatalf("unexpected ledger created from forbidden Retry: %v", err)
				}
			})
		}
	}
}

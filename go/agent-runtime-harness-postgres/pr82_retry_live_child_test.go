package harnesspostgres_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
)

// A settled physical Start receipt must never be mistaken for child terminal
// completion. Exercise the same cross-connection CAS behavior through Memory,
// SQLite and actual PostgreSQL stores, including accepted-but-dispatched
// Handoff/native children whose projection was interrupted.
func TestPR82RetryRefusesUnresolvedNativeAndRemoteChildrenAcrossStores(t *testing.T) {
	for name, create := range rootInvocationStores() {
		t.Run(name, func(t *testing.T) {
			assertRetryChildExecutionFenceScenarios(t, create(t), "retry-live-"+name)
		})
	}
}

func TestRealPostgresPR82RetryRefusesUnresolvedNativeAndRemoteChildren(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, _ := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	store, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	assertRetryChildExecutionFenceScenarios(t, store, "real-postgres-retry-live")
}

func assertRetryChildExecutionFenceScenarios(t *testing.T, store harness.Store, prefix string) {
	t.Helper()
	scenarios := []struct {
		name        string
		class       harness.ExecutionClass
		status      harness.InvocationStatus
		receipt     string
		wantBlocked bool
	}{
		{"native-running", harness.ExecutionTeam, harness.InvocationRunning, "physical_dispatch_returned", true},
		{"native-waiting", harness.ExecutionTeam, harness.InvocationWaitingInput, "physical_dispatch_returned", true},
		{"native-paused", harness.ExecutionTeam, harness.InvocationPausedBudget, "physical_dispatch_returned", true},
		{"app-running", harness.ExecutionApplication, harness.InvocationRunning, "", true},
		{"accepted-native-start", harness.ExecutionTeam, harness.InvocationAccepted, "physical_dispatch_returned", true},
		{"accepted-remote-handoff", harness.ExecutionAgent, harness.InvocationAccepted, "handoff_start_returned", true},
		{"accepted-unknown-outcome", harness.ExecutionAgent, harness.InvocationAccepted, "none", true},
		{"accepted-never-dispatched", harness.ExecutionTeam, harness.InvocationAccepted, "", false},
		{"accepted-definitive-no-effect", harness.ExecutionTeam, harness.InvocationAccepted, "physical_dispatch_rejected", false},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			turnID := prefix + "-" + scenario.name
			ctx := t.Context()
			now := time.Now().UTC()
			turn := harness.Turn{
				ID: turnID, SessionID: "session-" + turnID,
				HostTurn:         harness.HostRef{Kind: "turn", ID: turnID},
				ConfigSnapshotID: "config-" + turnID, Status: harness.TurnRunning,
				Revision: 1, CreatedAt: now, UpdatedAt: now,
			}
			if _, _, err := store.CreateTurn(ctx, turn); err != nil {
				t.Fatal(err)
			}
			input := json.RawMessage(`{"goal":"frozen"}`)
			sum := sha256.Sum256(input)
			hash := hex.EncodeToString(sum[:])
			root := harness.Invocation{
				ID: "root-" + turnID, TurnID: turnID, CapabilityKey: "runtime.agent",
				ExecutionClass: harness.ExecutionAgent, ExecutionRefID: "r1-" + turnID,
				Input: input, InputHash: hash, Status: harness.InvocationRunning,
				Attempt: 1, Revision: 1, OutputRefs: []harness.HostRef{}, CreatedAt: now, UpdatedAt: now,
			}
			if _, _, err := store.CreateInvocation(ctx, root); err != nil {
				t.Fatal(err)
			}
			child := harness.Invocation{
				ID: "child-" + turnID, TurnID: turnID, ParentItemID: "parent-item",
				CapabilityKey: "feature.child", ExecutionClass: scenario.class,
				ExecutionRefID: "c1-" + turnID, Input: input, InputHash: hash,
				Status: scenario.status, Attempt: 1, Revision: 1,
				OutputRefs: []harness.HostRef{}, CreatedAt: now, UpdatedAt: now,
			}
			if _, _, err := store.CreateInvocation(ctx, child); err != nil {
				t.Fatal(err)
			}
			if scenario.receipt != "" {
				grantID := "grant-" + turnID
				grant := harness.Item{
					ID: grantID, TurnID: turnID, InvocationID: child.ID, RunID: child.ExecutionRefID,
					Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
					ParentItemID: harness.ExecutionDispatchParentItemID,
					Payload:      json.RawMessage(`{"phase":"start","physicalRunID":"child"}`),
					CreatedAt:    now, UpdatedAt: now,
				}
				if _, _, err := store.AppendItem(ctx, grant); err != nil {
					t.Fatal(err)
				}
				if scenario.receipt != "none" {
					receipt, err := json.Marshal(map[string]string{"boundary": scenario.receipt})
					if err != nil {
						t.Fatal(err)
					}
					if _, _, err := store.AppendItem(ctx, harness.Item{
						ID: "receipt-" + turnID, TurnID: turnID, InvocationID: child.ID, RunID: child.ExecutionRefID,
						Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
						ParentItemID: grantID, Payload: receipt, CreatedAt: now, UpdatedAt: now,
					}); err != nil {
						t.Fatal(err)
					}
				}
			}
			root.Status = harness.InvocationFailed
			failed, err := store.UpdateInvocation(ctx, root, root.Revision)
			if err != nil {
				t.Fatal(err)
			}
			turn.Status = harness.TurnFailed
			if _, err := store.UpdateTurn(ctx, turn, turn.Revision); err != nil {
				t.Fatal(err)
			}
			next := "r2-" + turnID
			rotated, err := store.RetryInvocation(ctx, root.ID, failed.Revision, next, now.Add(time.Second))
			if scenario.wantBlocked {
				if scenario.class == harness.ExecutionApplication {
					if !errors.Is(err, harness.ErrApplicationOutcomeUnknown) {
						t.Fatalf("Running Application child did not fence retry: %v", err)
					}
				} else if !errors.Is(err, harness.ErrConflict) {
					t.Fatalf("unresolved native/remote child allowed Attempt rotation: %+v %v", rotated, err)
				}
				unchanged, loadErr := store.GetInvocation(ctx, root.ID)
				if loadErr != nil || unchanged.Attempt != failed.Attempt || unchanged.ExecutionRefID != failed.ExecutionRefID {
					t.Fatalf("rejected retry mutated Attempt: %+v %v", unchanged, loadErr)
				}
				// Only verified terminal child status permits the parent retry.
				child.Status = harness.InvocationCompleted
				child, err = store.UpdateInvocation(ctx, child, child.Revision)
				if err != nil {
					t.Fatal(err)
				}
				if scenario.receipt == "none" {
					// A terminal projection alone does not close an unknown
					// physical grant. Recovery must also persist its real receipt.
					if _, _, err := store.AppendItem(ctx, harness.Item{
						ID: "receipt-" + turnID, TurnID: turnID, InvocationID: child.ID, RunID: child.ExecutionRefID,
						Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
						ParentItemID: "grant-" + turnID, Payload: json.RawMessage(`{"boundary":"physical_dispatch_returned"}`),
						CreatedAt: now, UpdatedAt: now,
					}); err != nil {
						t.Fatal(err)
					}
				}
				rotated, err = store.RetryInvocation(ctx, root.ID, failed.Revision, next, now.Add(time.Second))
			}
			if err != nil || rotated.Attempt != 2 || rotated.ExecutionRefID != next {
				t.Fatalf("safely settled child should allow retry: %+v %v", rotated, err)
			}
		})
	}
}

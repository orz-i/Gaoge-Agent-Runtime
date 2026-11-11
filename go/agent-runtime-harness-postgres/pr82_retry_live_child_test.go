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
		name    string
		class   harness.ExecutionClass
		status  harness.InvocationStatus
		receipt string
	}{
		{"native-running", harness.ExecutionTeam, harness.InvocationRunning, "physical_dispatch_returned"},
		{"native-waiting", harness.ExecutionTeam, harness.InvocationWaitingInput, "physical_dispatch_returned"},
		{"native-paused", harness.ExecutionTeam, harness.InvocationPausedBudget, "physical_dispatch_returned"},
		{"app-running", harness.ExecutionApplication, harness.InvocationRunning, ""},
		{"accepted-native-start", harness.ExecutionTeam, harness.InvocationAccepted, "physical_dispatch_returned"},
		{"accepted-remote-handoff", harness.ExecutionAgent, harness.InvocationAccepted, "handoff_start_returned"},
		{"accepted-unknown-outcome", harness.ExecutionAgent, harness.InvocationAccepted, "none"},
		{"accepted-never-dispatched", harness.ExecutionTeam, harness.InvocationAccepted, ""},
		{"accepted-definitive-no-effect", harness.ExecutionTeam, harness.InvocationAccepted, "physical_dispatch_rejected"},
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
				phase := "start"
				if scenario.receipt == "handoff_start_returned" {
					// A remote Handoff grant has its own original physical
					// phase; it cannot be closed by a generic Start receipt.
					phase = "handoff-start"
				}
				grant := harness.Item{
					ID: grantID, TurnID: turnID, InvocationID: child.ID, RunID: child.ExecutionRefID,
					Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
					ParentItemID: harness.ExecutionDispatchParentItemID,
					Payload:      json.RawMessage(`{"phase":"` + phase + `","physicalRunID":"` + child.ExecutionRefID + `"}`),
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
						ID: sqlProofFactID("hdispatchdone", "grant-"+turnID), TurnID: turnID, InvocationID: child.ID, RunID: child.ExecutionRefID,
						Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
						ParentItemID: grantID, Payload: receipt, CreatedAt: now, UpdatedAt: now,
					}); err != nil {
						t.Fatal(err)
					}
				}
			}
			// A root failure must be backed by its own original
			// definite no-effect receipt; the child's returned Start
			// does not grant terminal authority to the parent.
			recordTestOriginalNoEffect(t, store, root, now)
			root.Status = harness.InvocationFailed
			failed, err := store.UpdateInvocation(ctx, root, root.Revision)
			if err != nil {
				t.Fatalf("root failed after exact original no-effect proof: %v", err)
			}
			turn.Status = harness.TurnFailed
			// All cases here deliberately retain a nonterminal child:
			// Running, WaitingInput, PausedBudget or Accepted. A root
			// with a valid no-effect response cannot terminalize its Turn
			// until the child's own original lifecycle is finished.
			if _, err := store.UpdateTurn(ctx, turn, turn.Revision); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("live child was hidden by no-effect root: child=%s outcome=%s err=%v",
					scenario.status, scenario.receipt, err)
			}
			_, err = store.RetryInvocation(ctx, root.ID, failed.Revision, "r2-"+turnID, now.Add(time.Second))
			if scenario.class == harness.ExecutionApplication && scenario.status == harness.InvocationRunning {
				if !errors.Is(err, harness.ErrApplicationOutcomeUnknown) && !errors.Is(err, harness.ErrConflict) {
					t.Fatalf("live Application child permitted parent Retry: %v", err)
				}
			} else if !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("nonterminal native/remote child permitted Retry: %v", err)
			}
			unchanged, loadErr := store.GetInvocation(ctx, root.ID)
			if loadErr != nil || unchanged.Attempt != failed.Attempt ||
				unchanged.ExecutionRefID != failed.ExecutionRefID {
				t.Fatalf("rejected retry mutated original Attempt: %+v err=%v", unchanged, loadErr)
			}
		})
	}
}

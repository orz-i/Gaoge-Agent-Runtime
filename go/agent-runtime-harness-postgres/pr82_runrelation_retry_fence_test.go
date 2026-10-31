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

// An independently executing RunRelation child has no Harness Invocation row.
// A terminal composite Run alone cannot prove that all its physical children
// terminated; for a previous Feature Start only a first-party no-effect
// receipt can authorize the next parent Attempt without a coordinated
// descendant-drain protocol.
func TestPR82CompositeRootRequiresNoEffectProofAcrossStores(t *testing.T) {
	for name, create := range rootInvocationStores() {
		t.Run(name, func(t *testing.T) {
			assertCompositeRootRetryFence(t, create(t), name)
		})
	}
}

func TestRealPostgresPR82CompositeRootRequiresNoEffectProof(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	db, _ := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	store, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	assertCompositeRootRetryFence(t, store, "real-postgres")
}

func assertCompositeRootRetryFence(t *testing.T, store harness.Store, prefix string) {
	t.Helper()
	tests := []struct {
		name    string
		class   harness.ExecutionClass
		receipt string
		allow   bool
	}{
		{"team-physical-start", harness.ExecutionTeam, "physical_dispatch_returned", false},
		{"workflow-physical-start", harness.ExecutionWorkflow, "physical_dispatch_returned", false},
		{"groupchat-physical-start", harness.ExecutionGroupChat, "physical_dispatch_returned", false},
		{"team-no-grant-unverified", harness.ExecutionTeam, "", false},
		{"workflow-no-effect", harness.ExecutionWorkflow, "feature_start_no_effect", true},
		{"team-pre-effect-rejection", harness.ExecutionTeam, "physical_dispatch_rejected", true},
		{"groupchat-pre-effect-rejection", harness.ExecutionGroupChat, "physical_dispatch_rejected", true},
		{"team-wrong-owner-no-effect", harness.ExecutionTeam, "wrong-owner", false},
		{"team-wrong-run-no-effect", harness.ExecutionTeam, "wrong-run", false},
		{"team-wrong-boundary", harness.ExecutionTeam, "native_agent_start_no_effect", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			now := time.Now().UTC()
			turnID := "ra82-" + prefix + "-" + tc.name
			turn := harness.Turn{ID: turnID, SessionID: "session-" + turnID, HostTurn: harness.HostRef{Kind: "turn", ID: turnID},
				ConfigSnapshotID: "config-" + turnID, Status: harness.TurnRunning, Revision: 1, CreatedAt: now, UpdatedAt: now}
			if _, _, err := store.CreateTurn(ctx, turn); err != nil {
				t.Fatal(err)
			}
			raw := json.RawMessage(`{"goal":"freeze-root"}`)
			digest := sha256.Sum256(raw)
			inv := harness.Invocation{ID: "inv-" + turnID, TurnID: turnID, CapabilityKey: "runtime.feature",
				ExecutionClass: tc.class, Input: raw, InputHash: hex.EncodeToString(digest[:]),
				ExecutionRefID: "r1-" + turnID, Status: harness.InvocationRunning, Attempt: 1, Revision: 1,
				OutputRefs: []harness.HostRef{}, CreatedAt: now, UpdatedAt: now}
			if _, _, err := store.CreateInvocation(ctx, inv); err != nil {
				t.Fatal(err)
			}
			if tc.receipt != "" {
				grantID := "grant-" + turnID
				grant := harness.Item{ID: grantID, TurnID: turnID, InvocationID: inv.ID, RunID: inv.ExecutionRefID,
					Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, ParentItemID: harness.ExecutionDispatchParentItemID,
					Payload: json.RawMessage(`{"phase":"start","physicalRunID":"child"}`), CreatedAt: now, UpdatedAt: now}
				if _, _, err := store.AppendItem(ctx, grant); err != nil {
					t.Fatal(err)
				}
				receiptOwner := inv.ID
				receiptRun := inv.ExecutionRefID
				boundary := tc.receipt
				switch tc.receipt {
				case "wrong-owner":
					receiptOwner = "another-invocation"
					boundary = "feature_start_no_effect"
				case "wrong-run":
					receiptRun = "another-run"
					boundary = "feature_start_no_effect"
				}
				payload, err := json.Marshal(map[string]string{"boundary": boundary})
				if err != nil {
					t.Fatal(err)
				}
				receipt := harness.Item{ID: "done-" + turnID, TurnID: turnID, InvocationID: receiptOwner, RunID: receiptRun,
					Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: grantID,
					Payload: payload, CreatedAt: now, UpdatedAt: now}
				if _, _, err = store.AppendItem(ctx, receipt); err != nil {
					t.Fatal(err)
				}
			}
			inv.Status = harness.InvocationFailed
			failed, err := store.UpdateInvocation(ctx, inv, inv.Revision)
			if err != nil {
				t.Fatal(err)
			}
			turn.Status = harness.TurnFailed
			if _, err := store.UpdateTurn(ctx, turn, turn.Revision); err != nil {
				t.Fatal(err)
			}
			retried, err := store.RetryInvocation(ctx, inv.ID, failed.Revision, "r2-"+turnID, now.Add(time.Second))
			if tc.allow {
				if err != nil || retried.Attempt != 2 {
					t.Fatalf("safe no-effect root retry rejected: %+v %v", retried, err)
				}
				return
			}
			if !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("unsafe composite root retry crossed unverified RunRelation descendants: %+v %v", retried, err)
			}
			unchanged, err := store.GetInvocation(ctx, inv.ID)
			if err != nil || unchanged.Attempt != 1 || unchanged.ExecutionRefID != inv.ExecutionRefID {
				t.Fatalf("failed composite retry mutated old Attempt: %+v %v", unchanged, err)
			}
		})
	}
}

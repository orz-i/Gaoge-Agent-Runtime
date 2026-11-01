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

func TestPR82DispatchGrantRejectsForeignAndContradictorySettlementsAcrossStores(t *testing.T) {
	for name, create := range rootInvocationStores() {
		t.Run(name, func(t *testing.T) {
			assertPR82DispatchSettlementIntegrity(t, create(t), name)
		})
	}
}

func TestRealPostgresPR82DispatchGrantRejectsForeignAndContradictorySettlements(t *testing.T) {
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
	assertPR82DispatchSettlementIntegrity(t, store, "real-postgres")
}

func assertPR82DispatchSettlementIntegrity(t *testing.T, store harness.Store, prefix string) {
	t.Helper()
	now := time.Now().UTC()
	ctx := t.Context()
	turnID := "receipt-fence-" + prefix
	turn := harness.Turn{
		ID: turnID, SessionID: "session-" + turnID,
		HostTurn:         harness.HostRef{Kind: "turn", ID: turnID},
		ConfigSnapshotID: "config-" + turnID,
		Status:           harness.TurnRunning, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.CreateTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"goal":"frozen"}`)
	digest := sha256.Sum256(input)
	root := harness.Invocation{
		ID: "root-" + turnID, TurnID: turnID, CapabilityKey: harness.CapabilityTeam,
		ExecutionClass: harness.ExecutionTeam, ExecutionRefID: "team-" + turnID,
		Input: input, InputHash: hex.EncodeToString(digest[:]),
		Status: harness.InvocationRunning, Attempt: 1, Revision: 1,
		OutputRefs: []harness.HostRef{}, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.CreateInvocation(ctx, root); err != nil {
		t.Fatal(err)
	}
	grant := harness.Item{
		ID: "grant-" + turnID, TurnID: turnID, InvocationID: root.ID, RunID: root.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		ParentItemID: harness.ExecutionDispatchParentItemID,
		Payload:      json.RawMessage(`{"phase":"start","physicalRunID":"team"}`),
		CreatedAt:    now, UpdatedAt: now,
	}
	if _, _, err := store.AppendItem(ctx, grant); err != nil {
		t.Fatal(err)
	}
	receipt := harness.Item{
		ID: "receipt-" + turnID, TurnID: turnID, InvocationID: root.ID, RunID: root.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
		ParentItemID: grant.ID, Payload: json.RawMessage(`{"boundary":"physical_dispatch_returned"}`),
		CreatedAt: now, UpdatedAt: now,
	}
	for _, tc := range []struct {
		name   string
		mutate func(*harness.Item)
	}{
		{"wrong-invocation", func(item *harness.Item) { item.InvocationID = "some-other-invocation" }},
		{"wrong-run", func(item *harness.Item) { item.RunID = "another-run" }},
		{"wrong-boundary", func(item *harness.Item) { item.Payload = json.RawMessage(`{"boundary":"invalid"}`) }},
		{"wrong-status", func(item *harness.Item) { item.Status = harness.ItemFailed }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := receipt
			invalid.ID = tc.name + "-" + turnID
			tc.mutate(&invalid)
			if _, _, err := store.AppendItem(ctx, invalid); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("invalid receipt allowed grant settlement: %v", err)
			}
			items, err := store.ListItems(ctx, turnID, 0, 500)
			if err != nil || !harness.PendingExecutionDispatch(items, root.ID) {
				t.Fatalf("foreign receipt closed an unresolved grant: %v", err)
			}
			failed := root
			failed.Status = harness.InvocationFailed
			if _, err := store.UpdateInvocation(ctx, failed, root.Revision); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("unknown physical dispatch allowed Failed CAS: %v", err)
			}
		})
	}
	if _, created, err := store.AppendItem(ctx, receipt); err != nil || !created {
		t.Fatalf("first authoritative settlement rejected: created=%t err=%v", created, err)
	}
	for _, tc := range []struct {
		name     string
		boundary string
	}{
		{"contradictory-rejected", "physical_dispatch_rejected"},
		{"duplicate-returned", "physical_dispatch_returned"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conflicting := receipt
			conflicting.ID = tc.name + "-" + turnID
			conflicting.Payload = json.RawMessage(`{"boundary":"` + tc.boundary + `"}`)
			if _, _, err := store.AppendItem(ctx, conflicting); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("second physical outcome appended to the same grant: %v", err)
			}
		})
	}
	items, err := store.ListItems(ctx, turnID, 0, 500)
	if err != nil || harness.PendingExecutionDispatch(items, "") {
		t.Fatalf("sole authoritative outcome did not settle grant: %v", err)
	}
	failed := root
	failed.Status = harness.InvocationFailed
	failed, err = store.UpdateInvocation(ctx, failed, root.Revision)
	if err != nil {
		t.Fatal(err)
	}
	turn.Status = harness.TurnFailed
	if _, err := store.UpdateTurn(ctx, turn, turn.Revision); err != nil {
		t.Fatal(err)
	}
	if rotated, retryErr := store.RetryInvocation(ctx, failed.ID, failed.Revision, "next-team-"+turnID, now.Add(time.Second)); !errors.Is(retryErr, harness.ErrConflict) {
		t.Fatalf("effectful Team start allowed another Attempt: %+v %v", rotated, retryErr)
	}
}

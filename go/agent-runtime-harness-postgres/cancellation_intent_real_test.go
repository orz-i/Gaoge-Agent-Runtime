package harnesspostgres_test

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRealPostgresCancellationFenceAcrossConnections(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, isolated := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	first, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	// A distinct handle models a second Harness process that shares only SQL.
	parsed, err := url.Parse(isolated)
	if err != nil {
		t.Fatal(err)
	}
	secondDB, err := gorm.Open(postgres.Open(parsed.String()), realHarnessPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := secondDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	second, err := harnesspostgres.New(secondDB)
	if err != nil {
		t.Fatal(err)
	}

	turn, invocation, interaction := createWaitingInteractionFixture(t, first, "cancel_fence_real")
	now := time.Now().UTC()
	intent := harness.Item{
		ID: "pg_fence_pending", TurnID: turn.ID, Kind: harness.ItemDiagnostic,
		Status: harness.ItemStarted, ParentItemID: harness.CancellationFenceParentItemID,
		RunID: invocation.ExecutionRefID, Payload: json.RawMessage(`{"boundary":"cancel_scope_pending"}`),
		CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := first.AppendItem(t.Context(), intent); err != nil || !fresh {
		t.Fatalf("persist intent: fresh=%v err=%v", fresh, err)
	}
	late := harness.Invocation{
		ID: "pg_fence_late_child", TurnID: turn.ID, ParentItemID: "parent_item",
		CapabilityKey: harness.CapabilityTeam, DefinitionVersion: harness.RuntimeCapabilityVersion,
		ExecutionClass: harness.ExecutionTeam, ExecutionRefID: "pg_fence_child",
		Status: harness.InvocationAccepted, Attempt: 1, Revision: 1, OutputRefs: []harness.HostRef{},
		CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err = second.CreateInvocation(t.Context(), late); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("remote admission crossed committed cancellation fence: %v", err)
	}
	interaction.Status = harness.InteractionResolved
	interaction.Response = json.RawMessage("{}")
	if _, err = second.ResolveInteraction(t.Context(), interaction, interaction.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("remote Interaction dispatched across fence: %v", err)
	}
	invocation.Status = harness.InvocationRunning
	if _, err = second.UpdateInvocation(t.Context(), invocation, invocation.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("remote callback CAS accepted across fence: %v", err)
	}
	for _, phase := range []string{"resolve-approval", "resolve-subtask-approval", "grant-call-allowance", "handoff-start"} {
		// The distinct DB handle must never commit a physical control
		// authorization while the other process owns cancellation.
		grant := harness.Item{
			ID: "pg_cancel_control_" + phase, TurnID: turn.ID,
			InvocationID: invocation.ID, RunID: invocation.ExecutionRefID,
			Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
			ParentItemID: harness.ExecutionDispatchParentItemID,
			Payload:      json.RawMessage(`{"phase":"` + phase + `","physicalRunID":"child-agent-run"}`),
			CreatedAt:    now, UpdatedAt: now,
		}
		if _, _, err = second.AppendItem(t.Context(), grant); !errors.Is(err, harness.ErrConflict) {
			t.Fatalf("remote %s bypassed committed cancellation intent: %v", phase, err)
		}
	}
	closed := harness.Item{
		ID: "pg_fence_settled", TurnID: turn.ID, Kind: harness.ItemDiagnostic,
		Status: harness.ItemCompleted, ParentItemID: intent.ID,
		Payload:   json.RawMessage(`{"boundary":"cancel_scope_settled"}`),
		CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err = first.AppendItem(t.Context(), closed); err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := second.CreateInvocation(t.Context(), late); err != nil || !fresh {
		t.Fatalf("remote admission remained blocked after terminal fence: fresh=%v err=%v", fresh, err)
	}
	dispatch := harness.Item{
		ID: "pg_real_dispatch_first", TurnID: turn.ID, InvocationID: late.ID, RunID: late.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		ParentItemID: harness.ExecutionDispatchParentItemID,
		Payload:      json.RawMessage(`{"phase":"start"}`),
		CreatedAt:    now, UpdatedAt: now,
	}
	if _, fresh, err := first.AppendItem(t.Context(), dispatch); err != nil || !fresh {
		t.Fatalf("first process could not claim physical dispatch: fresh=%v err=%v", fresh, err)
	}
	secondDispatch := dispatch
	secondDispatch.ID = "pg_real_dispatch_second"
	if _, _, err := second.AppendItem(t.Context(), secondDispatch); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("second connection obtained a concurrent physical grant: %v", err)
	}
	if _, _, err := second.AppendItem(t.Context(), harness.Item{
		ID: "pg_real_dispatch_first_done", TurnID: turn.ID,
		InvocationID: late.ID, RunID: late.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
		ParentItemID: dispatch.ID, Payload: json.RawMessage(`{"boundary":"physical_dispatch_returned"}`),
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := second.AppendItem(t.Context(), secondDispatch); err != nil || !fresh {
		t.Fatalf("second process could not claim after first dispatch settled: fresh=%v err=%v", fresh, err)
	}
}

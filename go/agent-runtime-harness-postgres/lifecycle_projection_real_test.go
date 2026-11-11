package harnesspostgres_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// A second physical PostgreSQL connection must not evade the terminal-status
// predicate on UpdateInvocation, even with the current expected Revision.
func TestRealPostgresTerminalInvocationFenceAcrossConnections(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, isolatedDSN := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	first, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	secondDB, err := gorm.Open(postgres.Open(isolatedDSN), realHarnessPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	secondSQL, err := secondDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = secondSQL.Close() }()
	second, err := harnesspostgres.New(secondDB)
	if err != nil {
		t.Fatal(err)
	}
	root := rootInvocationFixture("real-terminal-child", "real-terminal-turn", "parent-item")
	turn := harness.Turn{
		ID: root.TurnID, SessionID: "real-terminal-session",
		HostTurn:         harness.HostRef{Kind: "conversation_turn", ID: "terminal-turn"},
		ConfigSnapshotID: "real-terminal-config", Status: harness.TurnRunning,
		Revision: 1, CreatedAt: root.CreatedAt, UpdatedAt: root.UpdatedAt,
	}
	if _, _, err = first.CreateTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	created, _, err := first.CreateInvocation(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	// Commit an authentic first-party physical no-effect receipt rather
	// than creating a terminal state from a bare Store status update.
	// A second PostgreSQL connection must then preserve this original
	// Failed outcome for the full lifetime of its Attempt.
	recordTestOriginalNoEffect(t, first, created, created.CreatedAt)
	created.Status = harness.InvocationFailed
	created.UpdatedAt = created.UpdatedAt.Add(time.Second)
	settled, err := first.UpdateInvocation(t.Context(), created, created.Revision)
	if err != nil {
		t.Fatal(err)
	}

	for _, staleStatus := range []harness.InvocationStatus{
		harness.InvocationRunning, harness.InvocationCompleted, harness.InvocationCancelled,
	} {
		stale, readErr := second.GetInvocation(t.Context(), settled.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		stale.Status = staleStatus
		stale.UpdatedAt = stale.UpdatedAt.Add(time.Second)
		if _, updateErr := second.UpdateInvocation(t.Context(), stale, stale.Revision); !errors.Is(updateErr, harness.ErrConflict) {
			t.Fatalf("second connection accepted terminal rewrite to %s: %v", staleStatus, updateErr)
		}
		persisted, readErr := first.GetInvocation(t.Context(), settled.ID)
		if readErr != nil || persisted.Status != settled.Status || persisted.Revision != settled.Revision ||
			persisted.Attempt != settled.Attempt {
			t.Fatalf("terminal authority changed: before=%+v after=%+v err=%v", settled, persisted, readErr)
		}
	}

	same, err := second.GetInvocation(t.Context(), settled.ID)
	if err != nil {
		t.Fatal(err)
	}
	same.ErrorDetail = "updated after durable terminal failure"
	same.UpdatedAt = same.UpdatedAt.Add(time.Second)
	updated, err := second.UpdateInvocation(t.Context(), same, same.Revision)
	if err != nil || updated.Status != harness.InvocationFailed || updated.Revision != settled.Revision+1 {
		t.Fatalf("same-terminal repair unexpectedly rejected: result=%+v err=%v", updated, err)
	}
}

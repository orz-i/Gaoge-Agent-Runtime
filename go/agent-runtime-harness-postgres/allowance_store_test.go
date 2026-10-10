package harnesspostgres_test

import (
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Exercised over the production GORM adapter with an isolated SQLite database;
// the exact status values and revision-CAS SQL are shared with PostgreSQL.
func TestPausedAllowanceLifecyclePersistsAcrossHarnessStoreReload(t *testing.T) {
	store := newStore(t)
	now := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	actor := kernel.ActorRef{TenantID: "tenant-paused", ActorID: "operator"}
	session := harness.Session{
		ID: "session-paused", HostThread: harness.HostRef{Kind: "conversation", ID: "thread-paused"},
		Actor: actor, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := store.CreateSession(t.Context(), session); err != nil || !fresh {
		t.Fatalf("session fresh=%v err=%v", fresh, err)
	}
	config, err := harness.SealConfigSnapshot("turn-paused", harness.ConfigSnapshot{
		Model: "model", Commands: harness.FirstPartyCommandDescriptors(),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := store.PutConfigSnapshot(t.Context(), config); err != nil || !fresh {
		t.Fatalf("config fresh=%v err=%v", fresh, err)
	}
	turn := harness.Turn{
		ID: "turn-paused", SessionID: session.ID, HostTurn: harness.HostRef{Kind: "conversation_turn", ID: "turn-paused"},
		ConfigSnapshotID: config.ID, Status: harness.TurnRunning, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := store.CreateTurn(t.Context(), turn); err != nil || !fresh {
		t.Fatalf("turn fresh=%v err=%v", fresh, err)
	}
	invocation := harness.Invocation{
		ID: "invocation-paused", TurnID: turn.ID, CapabilityKey: harness.CapabilityAgent,
		ExecutionClass: harness.ExecutionAgent, ExecutionRefID: "private-agent-run",
		Status: harness.InvocationRunning, Attempt: 1, OutputRefs: []harness.HostRef{}, Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := store.CreateInvocation(t.Context(), invocation); err != nil || !fresh {
		t.Fatalf("invocation fresh=%v err=%v", fresh, err)
	}
	turn.Status = harness.TurnPausedBudget
	turn.ErrorCode = "agent.model_allowance_exhausted"
	turn.UpdatedAt = now.Add(time.Second)
	paused, err := store.UpdateTurn(t.Context(), turn, turn.Revision)
	if err != nil || paused.Revision != 2 {
		t.Fatalf("pause Turn=%#v err=%v", paused, err)
	}
	invocation.Status = harness.InvocationPausedBudget
	invocation.ErrorCode = turn.ErrorCode
	invocation.UpdatedAt = turn.UpdatedAt
	pausedInvocation, err := store.UpdateInvocation(t.Context(), invocation, invocation.Revision)
	if err != nil || pausedInvocation.Revision != 2 {
		t.Fatalf("pause Invocation=%#v err=%v", pausedInvocation, err)
	}
	reloadedTurn, err := store.GetTurn(t.Context(), turn.ID)
	if err != nil || reloadedTurn.Status != harness.TurnPausedBudget ||
		reloadedTurn.ErrorCode != turn.ErrorCode {
		t.Fatalf("persisted Turn=%#v err=%v", reloadedTurn, err)
	}
	reloadedInvocation, err := store.GetInvocation(t.Context(), invocation.ID)
	if err != nil || reloadedInvocation.Status != harness.InvocationPausedBudget ||
		reloadedInvocation.ExecutionRefID != invocation.ExecutionRefID {
		t.Fatalf("persisted Invocation=%#v err=%v", reloadedInvocation, err)
	}
	reloadedTurn.Status = harness.TurnRunning
	reloadedTurn.ErrorCode = ""
	reloadedTurn.UpdatedAt = now.Add(2 * time.Second)
	resumed, err := store.UpdateTurn(t.Context(), reloadedTurn, reloadedTurn.Revision)
	if err != nil || resumed.Revision != 3 {
		t.Fatalf("resume Turn=%#v err=%v", resumed, err)
	}
	if _, err = store.UpdateTurn(t.Context(), reloadedTurn, reloadedTurn.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("stale CAS unexpectedly changed resumed Turn: %v", err)
	}
	reloadedInvocation.Status = harness.InvocationRunning
	reloadedInvocation.ErrorCode = ""
	reloadedInvocation.UpdatedAt = reloadedTurn.UpdatedAt
	resumedInvocation, err := store.UpdateInvocation(t.Context(), reloadedInvocation, reloadedInvocation.Revision)
	if err != nil || resumedInvocation.Revision != 3 {
		t.Fatalf("resume Invocation=%#v err=%v", resumedInvocation, err)
	}
	if _, err = store.UpdateInvocation(t.Context(), reloadedInvocation, reloadedInvocation.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("stale CAS unexpectedly changed Invocation: %v", err)
	}
}

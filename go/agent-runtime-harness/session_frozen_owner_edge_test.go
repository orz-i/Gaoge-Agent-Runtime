package harness_test

import (
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func TestSessionFacadeSameActorCannotReadDifferentFrozenThread(t *testing.T) {
	store := harness.NewMemoryStore()
	base := memory.NewStore()
	clock := newResumeTurnClock()
	provider := &multiContextCaptureModel{}
	runner, _ := newResumeTurnHarness(t, base, store, provider, clock, nil)
	options := resumeTurnSessionOptions()
	original := mustResumeSession(t, runner, options)
	completed, err := original.Prompt(t.Context(), "turn", "own work")
	if err != nil || completed.Turn.Status != harness.TurnCompleted {
		t.Fatalf("fixture failed: %+v %v", completed.Turn, err)
	}
	options.Thread = kernel.ThreadRef{Kind: "conversation", ID: "unrelated-kernel-thread"}
	options.Transcript = nil
	mismatched := mustResumeSession(t, runner, options)
	if _, err := mismatched.Load(t.Context(), "turn"); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("same Actor read another frozen Thread: %v", err)
	}
	if _, err := mismatched.LoadState(t.Context(), "turn"); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("same Actor accessed frozen Thread status: %v", err)
	}
	if _, err := mismatched.Timeline(t.Context(), "turn", 0, 50); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("same Actor accessed frozen Thread history: %v", err)
	}
	if _, err := mismatched.Resume(t.Context(), "turn"); !errors.Is(err, harness.ErrConflict) || len(provider.requests) != 1 {
		t.Fatalf("same Actor resumed another frozen Thread: %v calls=%d", err, len(provider.requests))
	}
}

func TestSessionFacadeUnownedAcceptedStartupGapFailsClosed(t *testing.T) {
	store := harness.NewMemoryStore()
	base := memory.NewStore()
	clock := newResumeTurnClock()
	runner, _ := newResumeTurnHarness(t, base, store, &multiContextCaptureModel{}, clock, nil)
	options := resumeTurnSessionOptions()
	sessionID, err := harness.SessionID(options.HostThread)
	if err != nil {
		t.Fatal(err)
	}
	hostTurn := harness.HostRef{Kind: "conversation_turn", ID: "startup-gap"}
	id, err := harness.TurnID(sessionID, hostTurn)
	if err != nil {
		t.Fatal(err)
	}
	now := clock.Now()
	if _, _, err := store.CreateSession(t.Context(), harness.Session{
		ID: sessionID, HostThread: options.HostThread, Actor: options.Actor,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	config, err := harness.SealConfigSnapshot(id, options.Config, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutConfigSnapshot(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CreateTurn(t.Context(), harness.Turn{
		ID: id, SessionID: sessionID, HostTurn: hostTurn, ConfigSnapshotID: config.ID,
		Status: harness.TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	session := mustResumeSession(t, runner, options)
	if _, err := session.Load(t.Context(), "startup-gap"); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("unowned accepted Turn produced a Session snapshot: %v", err)
	}
	if _, err := session.LoadState(t.Context(), "startup-gap"); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("unowned accepted Turn produced bounded state: %v", err)
	}
	if _, err := session.Timeline(t.Context(), "startup-gap", 0, 50); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("unowned accepted Turn produced a history page: %v", err)
	}
	if _, err := session.Resume(t.Context(), "startup-gap"); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("unowned accepted Turn unexpectedly resumed: %v", err)
	}
}

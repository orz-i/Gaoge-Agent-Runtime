package harness

import (
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func TestLoadAcceptedTurnBeforeRuntimeRunExists(t *testing.T) {
	t.Parallel()
	runner := newAcceptedStartupGapRunner(t)
	snapshot, err := runner.Load(t.Context(), "ht_startup_gap")
	if err != nil {
		t.Fatalf("accepted startup gap must be loadable: %v", err)
	}
	if snapshot.Turn.Status != TurnAccepted || len(snapshot.Invocations) != 1 || snapshot.Output != nil {
		t.Fatalf("unexpected startup snapshot: %#v", snapshot)
	}
}

func newAcceptedStartupGapRunner(t *testing.T) *Runner {
	t.Helper()
	now := time.Date(2026, 8, 22, 1, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = "hs_startup_gap"
	const turnID = "ht_startup_gap"
	if _, _, err = store.CreateSession(t.Context(), Session{
		ID:         sessionID,
		HostThread: HostRef{Kind: "conversation", ID: "thread_startup_gap"},
		Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Revision:   1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	config, err := SealConfigSnapshot(turnID, ConfigSnapshot{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutConfigSnapshot(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CreateTurn(t.Context(), Turn{
		ID: turnID, SessionID: sessionID,
		HostTurn:         HostRef{Kind: "conversation_turn", ID: "client_turn_startup_gap"},
		ConfigSnapshotID: config.ID,
		Status:           TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CreateInvocation(t.Context(), Invocation{
		ID: "hiv_startup_gap", TurnID: turnID, CapabilityKey: CapabilityAgent,
		DefinitionVersion: RuntimeCapabilityVersion, ExecutionClass: ExecutionAgent,
		ExecutionRefID: "hr_startup_gap", Status: InvocationAccepted,
		Attempt: 1, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return &Runner{runtime: runtime, store: store}
}

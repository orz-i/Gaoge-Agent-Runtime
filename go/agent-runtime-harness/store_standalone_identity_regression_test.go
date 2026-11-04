package harness_test

import (
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func TestStandaloneStoreIdentifiersRemainNonHostAuthorities(t *testing.T) {
	for _, tc := range []struct {
		name, sessionID, turnID string
	}{
		{"session-nonhex", "hs_" + strings.Repeat("z", 24), "turn-custom"},
		{"turn-nonhex", "session-custom", "ht_" + strings.Repeat("z", 24)},
		{"session-uppercase", "hs_" + strings.Repeat("A", 24), "turn-custom"},
		{"turn-uppercase", "session-custom", "ht_" + strings.Repeat("F", 24)},
		{"session-short", "hs_" + strings.Repeat("a", 23), "turn-custom"},
		{"turn-short", "session-custom", "ht_" + strings.Repeat("a", 23)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := harness.NewMemoryStore()
			now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
			sid, tid := tc.sessionID, tc.turnID
			hostThread := harness.HostRef{Kind: "conversation", ID: "custom"}
			session := harness.Session{ID: sid, HostThread: hostThread, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Revision: 1, CreatedAt: now, UpdatedAt: now}
			if _, _, err := store.CreateSession(t.Context(), session); err != nil {
				t.Fatal(err)
			}
			config, err := harness.SealConfigSnapshot(tid, harness.ConfigSnapshot{Model: "fixture"}, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = store.PutConfigSnapshot(t.Context(), config); err != nil {
				t.Fatal(err)
			}
			turn := harness.Turn{ID: tid, SessionID: sid, HostTurn: harness.HostRef{Kind: "turn", ID: "custom"}, ConfigSnapshotID: config.ID, Status: harness.TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now}
			if _, _, err = store.CreateTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
			if err != nil {
				t.Fatal(err)
			}
			loop, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: directModel{}})
			if err != nil {
				t.Fatal(err)
			}
			runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: loop, Store: store})
			if err != nil {
				t.Fatal(err)
			}
			state, err := runner.LoadState(t.Context(), tid)
			if err != nil || state.Turn.ID != tid || state.Session.ID != sid {
				t.Fatalf("LEGIT_STANDALONE_REJECTED: session=%s turn=%s err=%v readTurn=%s readSession=%s", sid, tid, err, state.Turn.ID, state.Session.ID)
			}
		})
	}
}

package consumer_test

import (
	"path/filepath"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesssqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile/testprofile"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var (
	_ harness.Store                   = (*harnesssqlite.Store)(nil)
	_ budget.LedgerStore              = (*harnesssqlite.Store)(nil)
	_ kernel.ManagedRecoveryHostStore = (*harnesssqlite.Store)(nil)
)

func TestStandaloneHarnessSQLiteConsumerReopensSession(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(testprofile.Dir(t), "harness.sqlite")
	store, err := harnesssqlite.CreateNew(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	session := harness.Session{ID: "session", HostThread: harness.HostRef{Kind: "local", ID: "thread"}, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Revision: 1, CreatedAt: now, UpdatedAt: now}
	t.Cleanup(func() { _ = store.Close() })
	if _, fresh, err := store.CreateSession(ctx, session); err != nil || !fresh {
		t.Fatalf("create session: %t %v", fresh, err)
	}
	config, err := harness.SealConfigSnapshot("turn", harness.ConfigSnapshot{Model: "local"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutConfigSnapshot(ctx, config); err != nil {
		t.Fatal(err)
	}
	turn := harness.Turn{ID: "turn", SessionID: session.ID, HostTurn: harness.HostRef{Kind: "turn", ID: "one"}, ConfigSnapshotID: config.ID, Status: harness.TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, _, err = store.CreateTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := harnesssqlite.OpenExisting(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	saved, err := reopened.GetSession(ctx, session.ID)
	if err != nil || saved.Actor != session.Actor || saved.HostThread != session.HostThread {
		t.Fatalf("reopen session: %+v %v", saved, err)
	}
	query := harnesssqlite.TurnConfigQuery{TenantID: session.Actor.TenantID, ActorID: session.Actor.ActorID, TurnIDs: []string{turn.ID}}
	rows, err := reopened.ListTurnConfigs(ctx, query)
	if err != nil || len(rows) != 1 || rows[0].Config.ID != config.ID {
		t.Fatalf("owner lookup: %+v %v", rows, err)
	}
	query.ActorID = "foreign"
	rows, err = reopened.ListTurnConfigs(ctx, query)
	if err != nil || len(rows) != 0 {
		t.Fatalf("foreign lookup: %+v %v", rows, err)
	}

}

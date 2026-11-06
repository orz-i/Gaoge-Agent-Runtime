package harnesspostgres_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRealPostgresTrustedSessionRootOwnerAcrossConnections(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("requires TEST_POSTGRES_DSN")
	}
	db, isolated := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	first, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	secondDB, err := gorm.Open(postgres.Open(isolated), realHarnessPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := secondDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	second, err := harnesspostgres.New(secondDB)
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "trusted-cross-connection"
	assertTrustedHarnessStoreOwners(t, first, prefix)
	hostThread := harness.HostRef{Kind: "conversation", ID: prefix + "-tenant-b-thread"}
	sid, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	expectedActor := kernel.ActorRef{TenantID: "tenant-b", ActorID: "original-owner"}
	expectedThread := kernel.ThreadRef{Kind: "conversation", ID: "tenant-b-thread"}
	turn := harness.HostRef{Kind: "conversation_turn", ID: "turn-one"}
	tid, err := harness.TurnID(sid, turn)
	if err != nil {
		t.Fatal(err)
	}
	rootID, err := harness.AgentInvocationID(tid, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []harness.Store{first, second} {
		session, readErr := source.GetSession(t.Context(), sid)
		if readErr != nil || session.Actor != expectedActor || session.HostThread != hostThread {
			t.Fatalf("cross-connection Session owner drift: %+v %v", session, readErr)
		}
		root, readErr := source.GetInvocation(t.Context(), rootID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var input struct {
			Actor  kernel.ActorRef  `json:"actor"`
			Thread kernel.ThreadRef `json:"thread"`
		}
		if json.Unmarshal(root.Input, &input) != nil || input.Actor != expectedActor || input.Thread != expectedThread {
			t.Fatalf("cross-connection root owner drift: %+v", root)
		}
	}
	// Alternating independent SQL connections cannot mutate either owner,
	// even when the candidate passes a correctly recomputed unkeyed SHA-256.
	const workers = 16
	var group sync.WaitGroup
	start := make(chan struct{})
	errorsCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			source := harness.Store(first)
			if index%2 != 0 {
				source = second
			}
			if index%2 == 0 {
				session, readErr := source.GetSession(t.Context(), sid)
				if readErr != nil {
					errorsCh <- readErr
					return
				}
				session.Actor = kernel.ActorRef{TenantID: "foreign", ActorID: "hijack"}
				_, _, writeErr := source.CreateSession(t.Context(), session)
				errorsCh <- writeErr
				return
			}
			root, readErr := source.GetInvocation(t.Context(), rootID)
			if readErr != nil {
				errorsCh <- readErr
				return
			}
			root.Input, root.InputHash = trustedRootIdentityInput(t, kernel.ActorRef{TenantID: "foreign", ActorID: "hijack"}, expectedThread, "complete original work")
			_, writeErr := source.UpdateInvocation(t.Context(), root, root.Revision)
			errorsCh <- writeErr
		}(i)
	}
	close(start)
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if !errors.Is(err, harness.ErrConflict) {
			t.Fatalf("cross-connection owner retarget was not rejected: %v", err)
		}
	}
	storedSession, err := second.GetSession(t.Context(), sid)
	if err != nil || storedSession.Actor != expectedActor {
		t.Fatalf("persistent owner changed after SQL race: %+v %v", storedSession, err)
	}
	storedRoot, err := first.GetInvocation(t.Context(), rootID)
	if err != nil {
		t.Fatal(err)
	}
	var rootOwner struct {
		Actor kernel.ActorRef `json:"actor"`
	}
	if json.Unmarshal(storedRoot.Input, &rootOwner) != nil || rootOwner.Actor != expectedActor {
		t.Fatalf("durable root Actor changed after SQL race: %+v", rootOwner)
	}
}

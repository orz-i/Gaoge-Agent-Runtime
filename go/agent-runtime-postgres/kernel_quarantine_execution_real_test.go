package postgres

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestRealPostgresExecutionQuarantineSurvivesReconnection(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	db, isolation := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	rt, err := kernel.New(kernel.Dependencies{Store: NewKernelStore(db)})
	if err != nil {
		t.Fatal(err)
	}
	created, err := rt.Create(t.Context(), kernel.CreateRequest{
		ExecutionRequired: true, ID: "durable-poison", Kind: "agent",
		Actor:  kernel.ActorRef{TenantID: "t", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "persist quarantine", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{
		RunID: created.Run.ID, OwnerID: "previous", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	block := kernel.ExecutionQuarantine{RunID: created.Run.ID,
		Revision: created.Run.Revision, Generation: 7, Reason: "unsafe_work"}
	if err = rt.QuarantineExecution(t.Context(), block); err != nil {
		t.Fatal(err)
	}
	restarted, err := kernel.New(kernel.Dependencies{
		Store: NewKernelStore(reopenRecoveryPostgres(t, isolation)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.RequireExecution(t.Context(), created.Run.ID); !errors.Is(err, kernel.ErrExecutionQuarantined) {
		t.Fatalf("separate connection missed durable poison guard: %v", err)
	}
	if _, err = restarted.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{
		RunID: created.Run.ID, OwnerID: "new", LeaseDuration: time.Minute,
	}); !errors.Is(err, kernel.ErrExecutionQuarantined) {
		t.Fatalf("reopened worker ran quarantined work: %v", err)
	}
	if err = restarted.RepairExecutionQuarantine(t.Context(), block); err != nil {
		t.Fatal(err)
	}
	renewed, err := restarted.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{
		RunID: created.Run.ID, OwnerID: "after-repair", LeaseDuration: time.Minute,
	})
	if err != nil || renewed.Epoch <= lease.Epoch {
		t.Fatalf("new connection did not re-fence old owner: %+v %v", renewed, err)
	}
}

func TestRealPostgresExecutionQuarantineRacesWithCommit(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	db, _ := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	rt, err := kernel.New(kernel.Dependencies{Store: NewKernelStore(db)})
	if err != nil {
		t.Fatal(err)
	}
	created, err := rt.Create(t.Context(), kernel.CreateRequest{
		ExecutionRequired: true, ID: "raced-quarantine", Kind: "agent",
		Actor:  kernel.ActorRef{TenantID: "t", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "atomic race", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{
		RunID: created.Run.ID, OwnerID: "raced-writer", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := rt.WithExecution(lease)
	if err != nil {
		t.Fatal(err)
	}
	block := kernel.ExecutionQuarantine{RunID: created.Run.ID,
		Revision: created.Run.Revision, Reason: "suspected_poison"}
	start := make(chan struct{})
	var group sync.WaitGroup
	var blockErr, applyErr error
	group.Add(2)
	go func() {
		defer group.Done()
		<-start
		blockErr = rt.QuarantineExecution(t.Context(), block)
	}()
	go func() {
		defer group.Done()
		<-start
		_, applyErr = bound.Apply(t.Context(), created.Run.ID, created.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: json.RawMessage(`{"committed":true}`),
		})
	}()
	close(start)
	group.Wait()
	if blockErr == nil {
		if !errors.Is(applyErr, kernel.ErrExecutionQuarantined) &&
			!errors.Is(applyErr, kernel.ErrExecutionConflict) {
			t.Fatalf("writer bypassed quarantine: block=%v apply=%v", blockErr, applyErr)
		}
	} else if !errors.Is(blockErr, kernel.ErrConflict) || applyErr != nil {
		t.Fatalf("quarantine raced with commit unsafely: block=%v apply=%v", blockErr, applyErr)
	}
	stored, err := rt.Load(t.Context(), created.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blockErr == nil && stored.Run.Revision != created.Run.Revision {
		t.Fatalf("blocked Run was mutated by stale owner: %+v", stored.Run)
	}
	if blockErr != nil && stored.Run.Revision != created.Run.Revision+1 {
		t.Fatalf("committed work unexpectedly lost: %+v", stored.Run)
	}
}

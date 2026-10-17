package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

type poisonStatusResumer struct{}

func (poisonStatusResumer) Resume(context.Context, string, uint64) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, errors.New("poisoned Run must not enter a Resumer")
}

func TestRealPostgresCrossTenantChildRelationDoesNotWakeAnotherTenant(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("requires PostgreSQL 16")
	}
	db, _ := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	store := NewKernelStore(db)
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(NewRunRelationStore(db), nil)
	if err != nil {
		t.Fatal(err)
	}
	queue := queuecore.NewMemory(queuecore.Dependencies{})
	scheduler, err := continuation.NewScheduler(continuation.SchedulerDependencies{
		Outbox: store, Queue: queue, Relations: relations, Runs: runtime,
		ProjectorID: "tenant-scope-pg16",
	})
	if err != nil {
		t.Fatal(err)
	}
	create := func(id, tenant string) kernel.Snapshot {
		t.Helper()
		snapshot, createErr := runtime.Create(t.Context(), kernel.CreateRequest{
			ID: id, Kind: "test.tenant",
			Actor:  kernel.ActorRef{TenantID: tenant, ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
			Goal:   "scope", State: json.RawMessage("{}"),
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return snapshot
	}
	foreignParent, foreignChild := create("foreign-parent", "tenant-A"), create("foreign-child", "tenant-B")
	healthyParent, healthyChild := create("healthy-parent", "tenant-A"), create("healthy-child", "tenant-A")
	for _, pair := range []struct{ parent, child kernel.Snapshot }{
		{foreignParent, foreignChild}, {healthyParent, healthyChild},
	} {
		if _, err = relations.Ensure(t.Context(), runrelation.Draft{
			ParentRunID: pair.parent.Run.ID, ChildRunID: pair.child.Run.ID,
			Kind: runrelation.KindTeamMember, OwnerNodeID: "member",
		}); err != nil {
			t.Fatal(err)
		}
		_, err = runtime.Apply(t.Context(), pair.child.Run.ID, pair.child.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusCompleted, State: pair.child.State,
			Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage("true")},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, project := range []func(context.Context) error{scheduler.Project, scheduler.Reconcile} {
		if err = project(t.Context()); !errors.Is(err, continuation.ErrInvalidInput) {
			t.Fatalf("cross-tenant relation must be refused: %v", err)
		}
		jobs, listErr := queue.List(t.Context(), continuation.QueueName, "")
		if listErr != nil || len(jobs) != 1 {
			t.Fatalf("unrelated healthy Run was blocked or tenant boundary crossed: jobs=%+v err=%v", jobs, listErr)
		}
		var payload continuation.Payload
		if err = json.Unmarshal(jobs[0].Payload, &payload); err != nil ||
			payload.RunID != healthyParent.Run.ID || payload.SourceRunID != healthyChild.Run.ID {
			t.Fatalf("unexpected cross-tenant wakeup: payload=%+v err=%v", payload, err)
		}
	}
}

func TestRealPostgresUnknownRunStatusCannotACKRecovery(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("requires PostgreSQL 16")
	}
	db, _ := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	runtime, err := kernel.New(kernel.Dependencies{Store: NewKernelStore(db)})
	if err != nil {
		t.Fatal(err)
	}
	created, err := runtime.Create(t.Context(), kernel.CreateRequest{
		ID: "poisoned-status", Kind: "test.poison",
		Actor:  kernel.ActorRef{TenantID: "tenant-A", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "poison", State: json.RawMessage("{}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// A damaged physical row bypasses the public Kernel mutation validator.
	if err = db.Model(&models.KernelRunRecord{}).Where("run_id = ?", created.Run.ID).
		Update("status", "unknown_lifecycle").Error; err != nil {
		t.Fatal(err)
	}
	dispatcher, err := continuation.NewDispatcher(runtime,
		continuation.RegisterResumer(created.Run.Kind, poisonStatusResumer{}))
	if err != nil {
		t.Fatal(err)
	}
	if err = dispatcher.Dispatch(t.Context(), continuation.Payload{
		RunID: created.Run.ID, ExpectedRevision: created.Run.Revision,
		Trigger: continuation.TriggerRunReady, SourceRunID: created.Run.ID,
		SourceRevision: created.Run.Revision,
	}); !errors.Is(err, kernel.ErrInvalidInput) {
		t.Fatalf("poisoned Store status must NACK, got %v", err)
	}
	loaded, err := runtime.Load(t.Context(), created.Run.ID)
	if err != nil || loaded.Run.Status != "unknown_lifecycle" {
		t.Fatalf("poisoned Run was incorrectly mutated: %+v err=%v", loaded.Run, err)
	}
}

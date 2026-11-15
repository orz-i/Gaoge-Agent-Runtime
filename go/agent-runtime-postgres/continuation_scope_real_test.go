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
)

type poisonStatusResumer struct{}

func (poisonStatusResumer) Resume(context.Context, string, uint64) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, errors.New("poisoned Run must not enter a Resumer")
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
	created, err := runtime.Create(t.Context(), kernel.CreateRequest{ID: "poisoned-status", Kind: "test.poison", Actor: kernel.ActorRef{TenantID: "tenant-A", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "poison", State: json.RawMessage("{}")})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&models.KernelRunRecord{}).Where("run_id = ?", created.Run.ID).Update("status", "unknown_lifecycle").Error; err != nil {
		t.Fatal(err)
	}
	dispatcher, err := continuation.NewDispatcher(runtime, continuation.RegisterResumer(created.Run.Kind, poisonStatusResumer{}))
	if err != nil {
		t.Fatal(err)
	}
	if err = dispatcher.Dispatch(t.Context(), continuation.Payload{RunID: created.Run.ID, ExpectedRevision: created.Run.Revision, Trigger: continuation.TriggerRunReady, SourceRunID: created.Run.ID, SourceRevision: created.Run.Revision}); !errors.Is(err, kernel.ErrInvalidInput) {
		t.Fatalf("poisoned Store status must NACK, got %v", err)
	}
	loaded, err := runtime.Load(t.Context(), created.Run.ID)
	if err != nil || loaded.Run.Status != "unknown_lifecycle" {
		t.Fatalf("poisoned Run was incorrectly mutated: %+v err=%v", loaded.Run, err)
	}
}

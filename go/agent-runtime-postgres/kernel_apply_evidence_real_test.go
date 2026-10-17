package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var errSimulatedLostPGApplyReply = errors.New("test lost PostgreSQL Apply response after COMMIT")

type lostPGApplyResponseStore struct {
	kernel.Store
	once bool
}

func (store *lostPGApplyResponseStore) Apply(
	ctx context.Context, runID string, revision uint64, mutation kernel.StoreMutation,
) (kernel.Snapshot, error) {
	committed, err := store.Store.Apply(ctx, runID, revision, mutation)
	if err != nil {
		return committed, err
	}
	if !store.once {
		store.once = true
		return kernel.Snapshot{}, errSimulatedLostPGApplyReply
	}
	return committed, nil
}

// The caller learns nothing from a lost SQL response alone. A new PostgreSQL
// connection proves the exact operation from retained EventJournal rows even
// after a different CAS advanced or terminated the aggregate.
func TestRealPostgresIdentifiedApplyLostReplyAndNewerRevisionRecovery(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("requires a real PostgreSQL fixture")
	}
	db, schema := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	rt, err := kernel.New(kernel.Dependencies{
		Store: &lostPGApplyResponseStore{Store: NewKernelStore(db)},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "pg-identification", Kind: "test.evidence",
		Actor:  kernel.ActorRef{TenantID: "t", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "distinguish committed from lost response", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	intent := kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"committed":true}`)}
	_, err = rt.ApplyIdentified(t.Context(), created.Run.ID, created.Run.Revision, "pg-stable-operation", intent)
	if !errors.Is(err, errSimulatedLostPGApplyReply) {
		t.Fatalf("actual DB COMMIT lost-response fault not injected: %v", err)
	}
	written, err := rt.Load(t.Context(), created.Run.ID)
	if err != nil || written.Run.Revision != 2 {
		t.Fatalf("commit was not persisted: %+v err=%v", written.Run, err)
	}
	terminated, err := rt.Apply(t.Context(), written.Run.ID, written.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: json.RawMessage(`{"settled":true}`),
		Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage(`true`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	reconnected, err := kernel.New(kernel.Dependencies{
		Store: NewKernelStore(reopenRecoveryPostgres(t, schema)),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reconnected.VerifyIdentifiedApply(t.Context(), created.Run.ID, created.Run.Revision,
		"pg-stable-operation", intent)
	if err != nil || result.Status != kernel.ApplyEvidenceCommitted ||
		result.CommittedRevision != 2 || result.CurrentRevision != terminated.Run.Revision {
		t.Fatalf("new DB connection lost exact historical operation: %+v err=%v", result, err)
	}
	if _, err = reconnected.ApplyIdentified(t.Context(), created.Run.ID, created.Run.Revision,
		"pg-stable-operation", intent); !errors.Is(err, kernel.ErrApplyAlreadyCommitted) {
		t.Fatalf("retried operation was not deduplicated after process reconstruction: %v", err)
	}
}

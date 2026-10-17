package consumer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// These compile-time contracts must hold when the consumer is built with
// GOWORK=off, first against the local coordinated cohort and then against
// immutable published module tags with NO filesystem replace directives.
var (
	_ kernel.Store          = (*sqlite.Store)(nil)
	_ kernel.ExecutionStore = (*sqlite.Store)(nil)
	_ kernel.ControlStore   = (*sqlite.Store)(nil)
	_ runrelation.Store     = (*sqlite.RunRelationStore)(nil)
)

func TestStandaloneSQLiteConsumerPersistsSnapshotAndRelation(t *testing.T) {
	ctx := context.Background()
	privateDir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(privateDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(privateDir, "runtime.sqlite")
	store, err := sqlite.CreateNew(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	created, err := runtime.Create(ctx, kernel.CreateRequest{
		ID: "parent", Kind: kernel.RunKind("agent"),
		Actor:  kernel.ActorRef{TenantID: "local", ActorID: "profile"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "standalone module consumer", State: json.RawMessage(`{"value":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Run.Revision != 1 || string(created.State) != `{"value":1}` {
		t.Fatalf("unexpected Create snapshot: %+v", created)
	}
	relation := runrelation.Relation{
		ParentRunID: "parent", ChildRunID: "child", Kind: runrelation.KindCapability,
		OwnerNodeID: "step", CreatedAt: time.Now().UTC(),
	}
	first, reused, err := sqlite.NewRunRelationStore(store).Put(ctx, relation)
	if err != nil || reused {
		t.Fatalf("initial relation Put: reused=%t err=%v", reused, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = sqlite.OpenExisting(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	loaded, err := store.Load(ctx, created.Run.ID)
	if err != nil || loaded.Run.Revision != created.Run.Revision || !bytes.Equal(loaded.State, created.State) {
		t.Fatalf("standalone reopened Run: %+v err=%v", loaded, err)
	}
	storeRelations := sqlite.NewRunRelationStore(store)
	got, err := storeRelations.GetByChild(ctx, relation.ChildRunID)
	if err != nil || !runrelation.EqualIdentity(got, first) || !got.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("standalone reopened relation: %+v err=%v", got, err)
	}
	got, reused, err = storeRelations.Put(ctx, relation)
	if err != nil || !reused || !got.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("reused relation is not durable: %+v reused=%t err=%v", got, reused, err)
	}
	if _, err = storeRelations.GetByChild(ctx, "missing-child"); !errors.Is(err, runrelation.ErrNotFound) {
		t.Fatalf("absent child: %v", err)
	}
}

package sqlite

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestOpenStoreEscapedAbsolutePath(t *testing.T) {
	// On Windows this absolute path includes a drive letter. Spaces, # and %
	// must remain filename characters rather than changing the SQLite URI.
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "runtime #100%.sqlite")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	// Initialize an unpublished staging database, as CreateNew does. This
	// isolates URI handling from its separate directory-fsync requirement.
	store, err := openStore(t.Context(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	created, err := runtime.Create(t.Context(), kernel.CreateRequest{
		ID: "escaped-path", Kind: "agent",
		Actor:  kernel.ActorRef{TenantID: "test", ActorID: "test"},
		Thread: kernel.ThreadRef{Kind: "test", ID: "test"}, Goal: "preserve the database path",
		State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	runtime, err = kernel.New(kernel.Dependencies{Store: reopened})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := runtime.Load(t.Context(), created.Run.ID)
	if err != nil || loaded.Run.ID != created.Run.ID || loaded.Run.Revision != created.Run.Revision {
		t.Fatalf("reopened wrong database: snapshot=%+v err=%v", loaded, err)
	}
}

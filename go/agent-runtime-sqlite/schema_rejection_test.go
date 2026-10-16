package sqlite_test

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
)

// Rejecting a current-version database with the wrong table shape must not
// mutate its header/journal mode while reporting ErrUnsupportedSchema.
func TestIncompatibleV1SchemaRejectedWithoutChangingJournal(t *testing.T) {
	path := filepath.Join(privateDir(t), "unrecognized.sqlite")
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec("CREATE TABLE runtime_runs (unexpected TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err = raw.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "delete" {
		t.Fatalf("unexpected fixture journal_mode=%s", mode)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sqlite.OpenExisting(t.Context(), path); !errors.Is(err, sqlite.ErrUnsupportedSchema) {
		t.Fatalf("incompatible v1 schema was not rejected: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("rejected incompatible schema mutated the SQLite database header/content")
	}
	raw, err = sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if err = raw.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "delete" {
		t.Fatalf("rejected database journal mode changed to %s", mode)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err = os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("schema rejection created SQLite sidecar %s: %v", suffix, err)
		}
	}
}

// A compatible schema may legitimately enable WAL when opened.
func TestCompatibleV1SchemaMaySwitchBackToWAL(t *testing.T) {
	path := filepath.Join(privateDir(t), "compatible.sqlite")
	store, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	if err = raw.QueryRow("PRAGMA journal_mode=DELETE").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "delete" {
		t.Fatalf("could not set fixture to DELETE mode: %s", mode)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = sqlite.OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	raw, err = sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if err = raw.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("compatible database not brought to WAL: %s", mode)
	}
}

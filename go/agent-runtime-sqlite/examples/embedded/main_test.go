package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile/testprofile"
	_ "modernc.org/sqlite"
)

func TestEmbeddedExamplePersistsAndReopens(t *testing.T) {
	dir := testprofile.Dir(t)
	path := filepath.Join(dir, "example.sqlite")
	if err := run(context.Background(), path, true); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM runtime_runs WHERE status='completed' AND revision>1`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected two independently persisted completed Runs, found %d", count)
	}
}

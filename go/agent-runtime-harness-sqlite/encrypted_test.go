package harnesssqlite_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	harnesssqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-sqlite"
	kernelsqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile/encrypted"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile/testprofile"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestEncryptedStoresRetainOriginalAuthorities(t *testing.T) {
	ctx := t.Context()
	dir := testprofile.Dir(t)
	type store interface {
		ManagedRecoveryAuthority(context.Context) (kernel.ManagedAuthority, error)
		Close() error
	}
	for _, name := range []string{"Kernel #100%.sqlite", "Harness #100%.sqlite"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name)
			var key [32]byte
			rand.Read(key[:])
			defer clear(key[:])
			open := encrypted.WithKey(&key)
			var s store
			var err error
			if name[0] == 'K' {
				s, err = kernelsqlite.CreateNewWith(ctx, path, open)
			} else {
				s, err = harnesssqlite.CreateNewWith(ctx, path, open)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := s.ManagedRecoveryAuthority(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil || len(raw) < 4096 || bytes.Contains(raw, []byte("SQLite format 3")) || bytes.Contains(raw, []byte("CREATE TABLE")) {
				t.Fatal("encrypted database evidence", err)
			}
			if name[0] == 'K' {
				s, err = kernelsqlite.OpenExistingWith(ctx, path, open)
			} else {
				s, err = harnesssqlite.OpenExistingWith(ctx, path, open)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			after, err := s.ManagedRecoveryAuthority(ctx)
			if err != nil || before != after {
				t.Fatalf("reopen changed authority: %v", err)
			}
		})
	}
}

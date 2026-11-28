package harnesssqlite_test

import (
	"crypto/rand"
	"path/filepath"
	"testing"

	kernelsqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile/encrypted"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile/testprofile"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Re-open the encrypted native Kernel database and its co-located ledger,
// not merely a new Runner backed by the original handles.
func TestSettledToolOutcomeEncryptedKernelAndLedgerReopen(t *testing.T) {
	conformance.RunSettledToolRecovery(t, func(t *testing.T) (kernel.Store, kernel.Store, func() kernel.Store) {
		var key [32]byte
		if _, err := rand.Read(key[:]); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { clear(key[:]) })
		path := filepath.Join(testprofile.Dir(t), "kernel.sqlite")
		open := encrypted.WithKey(&key)
		original, err := kernelsqlite.CreateNewWith(t.Context(), path, open)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = original.Close() })
		peer, err := kernelsqlite.OpenExistingWith(t.Context(), path, open)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = peer.Close() })
		return original, peer, func() kernel.Store {
			if err := original.Close(); err != nil {
				t.Fatal(err)
			}
			if err := peer.Close(); err != nil {
				t.Fatal(err)
			}
			fresh, err := kernelsqlite.OpenExistingWith(t.Context(), path, open)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = fresh.Close() })
			return fresh
		}
	})
}

package sqlite_test

import (
	"path/filepath"
	"testing"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestEA1RootAgentControlPhasesNativeSQLite(t *testing.T) {
	conformance.RunEA1RootAgentControlPhases(t, func(t *testing.T) (kernel.Store, kernel.Store, func() kernel.Store) {
		path := filepath.Join(privateDir(t), "root-control.sqlite")
		first, e := sqlite.CreateNew(t.Context(), path)
		if e != nil {
			t.Fatal(e)
		}
		peer, e := sqlite.OpenExisting(t.Context(), path)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = first.Close(); _ = peer.Close() })
		return first, peer, func() kernel.Store {
			if e := first.Close(); e != nil {
				t.Fatal(e)
			}
			if e := peer.Close(); e != nil {
				t.Fatal(e)
			}
			fresh, e := sqlite.OpenExisting(t.Context(), path)
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { _ = fresh.Close() })
			return fresh
		}
	})
}

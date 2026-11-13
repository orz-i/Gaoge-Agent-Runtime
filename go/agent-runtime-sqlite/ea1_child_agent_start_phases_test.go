package sqlite_test

import (
	"path/filepath"
	"testing"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestEA1ChildAgentStartPhasesNativeSQLite(t *testing.T) {
	conformance.RunEA1ChildAgentStartPhases(t, func(t *testing.T) (kernel.Store, kernel.Store, func() kernel.Store) {
		path := filepath.Join(privateDir(t), "child-agent-start-phases.sqlite")
		first, err := sqlite.CreateNew(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = first.Close() })
		peer, err := sqlite.OpenExisting(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = peer.Close() })
		return first, peer, func() kernel.Store {
			// Close every live handle before the persistence recovery open.
			if err := peer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := sqlite.OpenExisting(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			return reopened
		}
	})
}

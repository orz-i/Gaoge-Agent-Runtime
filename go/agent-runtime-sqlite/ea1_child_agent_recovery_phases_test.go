package sqlite_test

import (
	"path/filepath"
	"testing"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestEA1ChildAgentRecoveryPhasesNativeSQLite(t *testing.T) {
	conformance.RunEA1ChildAgentRecoveryPhases(t, func(t *testing.T) (kernel.Store, func() (kernel.Store, kernel.Store)) {
		path := filepath.Join(privateDir(t), "child-recovery-phases.sqlite")
		first, err := sqlite.CreateNew(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = first.Close() })
		return first, func() (kernel.Store, kernel.Store) {
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := sqlite.OpenExisting(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			peer, err := sqlite.OpenExisting(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = peer.Close() })
			return reopened, peer
		}
	})
}

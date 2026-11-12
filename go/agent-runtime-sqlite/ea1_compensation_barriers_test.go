package sqlite_test

import (
	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"path/filepath"
	"testing"
)

func TestEA1CompensationBarriersSQLite(t *testing.T) {
	conformance.RunEA1CompensationBarriers(t, func(t *testing.T) (kernel.Store, kernel.Store) {
		path := filepath.Join(privateDir(t), "compensation-barriers.sqlite")
		first, e := sqlite.CreateNew(t.Context(), path)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = first.Close() })
		peer, e := sqlite.OpenExisting(t.Context(), path)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = peer.Close() })
		return first, peer
	})
}

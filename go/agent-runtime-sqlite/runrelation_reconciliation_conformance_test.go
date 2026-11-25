package sqlite_test

import (
	"path/filepath"
	"testing"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

func TestSQLiteRelationalReconciliation(t *testing.T) {
	path := filepath.Join(privateDir(t), "shared-reconciliation.sqlite")
	s, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	conformance.RunRelationalReconciliationSuite(t, s, sqlite.NewRunRelationStore(s), func() runrelation.Store {
		peer, e := sqlite.OpenExisting(t.Context(), path)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = peer.Close() })
		return sqlite.NewRunRelationStore(peer)
	})
}

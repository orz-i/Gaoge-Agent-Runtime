package postgres

import (
	"testing"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runrelation"
)

func TestRunRelationStoreConformance(t *testing.T) {
	t.Parallel()
	conformance.RunRunRelationStoreSuite(t, func(tb testing.TB) runrelation.Store {
		tb.Helper()
		db := openKernelStoreTestDB(tb)
		return NewRunRelationStore(db)
	})
}

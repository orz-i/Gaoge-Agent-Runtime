package postgres

import (
	"testing"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
)

func TestKernelStoreConformance(t *testing.T) {
	t.Parallel()
	conformance.RunKernelStoreSuite(t, func(testing.TB) kernel.Store {
		db := openConversationRepositoryTestDB(t)
		return NewKernelStore(db, StaticSessions(db))
	})
}

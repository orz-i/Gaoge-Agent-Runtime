package postgres

import (
	"testing"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/conformance"
)

func TestStoreConformance(t *testing.T) {
	conformance.RunStore(t, func(tb testing.TB) agentruntime.Store {
		t := tb.(*testing.T)
		db := openConversationRepositoryTestDB(t)
		return New(db, StaticSessions(db))
	})
}

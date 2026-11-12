package postgres

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestEA1WorkflowRecoveryBoundariesGORM(t *testing.T) {
	conformance.RunEA1WorkflowRecoveryBoundariesSuite(t, func(t *testing.T) (kernel.Store, func() (kernel.Store, kernel.Store)) {
		db := openKernelStoreTestDB(t)
		return NewKernelStore(db), func() (kernel.Store, kernel.Store) { return NewKernelStore(db), NewKernelStore(db) }
	})
}

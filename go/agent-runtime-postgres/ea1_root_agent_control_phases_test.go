package postgres

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestEA1RootAgentControlPhasesGORM(t *testing.T) {
	conformance.RunEA1RootAgentControlPhases(t, func(t *testing.T) (kernel.Store, kernel.Store, func() kernel.Store) {
		db := openKernelStoreTestDB(t)
		return NewKernelStore(db), NewKernelStore(db), func() kernel.Store { return NewKernelStore(db) }
	})
}

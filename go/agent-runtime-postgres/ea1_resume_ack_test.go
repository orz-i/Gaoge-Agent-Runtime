package postgres

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestEA1ResumeMarkAckGORM(t *testing.T) {
	conformance.RunEA1ResumeMarkAckSuite(t, func(t *testing.T) (kernel.Store, func() kernel.Store) {
		db := openKernelStoreTestDB(t)
		return NewKernelStore(db), func() kernel.Store { return NewKernelStore(db) }
	})
}

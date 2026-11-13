package postgres

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestEA1WorkflowStartBoundariesGORM(t *testing.T) {
	conformance.RunEA1WorkflowStartBoundaries(t, func(t *testing.T) (kernel.Store, kernel.Store) {
		s := NewKernelStore(openKernelStoreTestDB(t))
		return s, s
	})
}

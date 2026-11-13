package sqlite_test

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestEA1WaitBarriersSQLite(t *testing.T) {
	conformance.RunEA1WaitBarriers(t, func(t *testing.T) (kernel.Store, kernel.Store) { s := openStore(t); return s, s })
}

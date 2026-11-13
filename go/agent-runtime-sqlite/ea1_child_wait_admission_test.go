package sqlite_test

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestEA1ChildWaitAdmissionSQLite(t *testing.T) {
	conformance.RunEA1ChildWaitAdmissionSuite(t, func(t *testing.T) (kernel.Store, kernel.Store) { s := openStore(t); return s, s })
}

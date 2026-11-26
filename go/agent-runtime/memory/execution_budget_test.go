package memory

import (
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"testing"
)

func TestExecutionBudgetAtomic(t *testing.T) {
	conformance.RunExecutionBudgetSuite(t, func(*testing.T) conformance.ExecutionBudgetStore {
		s := NewStore()
		return conformance.ExecutionBudgetStore{Store: s, Ledger: s}
	})
}

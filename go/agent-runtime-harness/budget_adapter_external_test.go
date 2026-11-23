package harness_test

import (
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
)

func mustBudgetAdapter(t testing.TB, authority *harness.BudgetAuthority) *agent.BoundedBudgetAdapter {
	t.Helper()
	adapter, err := authority.Adapter()
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

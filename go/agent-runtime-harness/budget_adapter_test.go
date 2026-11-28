package harness

import (
	"errors"
	"sync"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
)

func mustBudgetAdapter(t testing.TB, authority *BudgetAuthority) *agent.BoundedBudgetAdapter {
	t.Helper()
	adapter, err := authority.Adapter()
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

// The authority is not a second executable middleware. All three callback
// roles receive the SAME native instance, even under concurrent Host setup.
func TestBudgetAuthorityHasOneNativeDriverAndNoLegacyPluginEngine(t *testing.T) {
	authority, err := NewBudgetAuthority(BudgetAuthorityDependencies{Store: NewMemoryStore(), Clock: hostedBudgetClock{}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := authority.Adapter()
	if err != nil || first == nil {
		t.Fatalf("native adapter missing: %v", err)
	}
	const n = 24
	var wg sync.WaitGroup
	outputs := make([]*agent.BoundedBudgetAdapter, n)
	for i := range outputs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			adapter, e := authority.Adapter()
			if e != nil {
				t.Errorf("parallel adapter creation: %v", e)
			}
			outputs[i] = adapter
		}(i)
	}
	wg.Wait()
	for _, adapter := range outputs {
		if adapter != first {
			t.Fatal("one Host authority exposed multiple callback ticket identities")
		}
	}
	var absent *BudgetAuthority
	if _, err := absent.Adapter(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("nil authority admitted an executor: %v", err)
	}
}

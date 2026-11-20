package budget_test

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

func TestCancelledRootRetryAdmitsOnlyItsProspectiveDescendants(t *testing.T) {
	c := newLedger(t, budget.Limits{MaxLLMCalls: 8, MaxChildRuns: 4})
	if _, err := c.RegisterRun(t.Context(), "turn", "old-child", budget.RunBudget{ParentRunID: "root"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CancelRoot(t.Context(), "turn", "root"); err != nil {
		t.Fatal(err)
	}
	if _, err := testAdmitRootRetry(c, t.Context(), "turn", "inv", "root", "root-2", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RegisterRun(t.Context(), "turn", "root-2", budget.RunBudget{}); err != nil {
		t.Fatal(err)
	}
	for _, binding := range []struct{ id, parent string }{{"new-child", "root-2"}, {"nested", "new-child"}} {
		if _, err := c.RegisterRun(t.Context(), "turn", binding.id, budget.RunBudget{ParentRunID: binding.parent}); err != nil {
			t.Fatalf("active prospective ancestry rejected: %s %v", binding.id, err)
		}
	}
	if _, err := c.CancelRun(t.Context(), "turn", "revoked-before-registration"); err != nil {
		t.Fatal(err)
	}
	for _, binding := range []struct{ id, parent string }{{"late-old-child", "root"}, {"late-old-grandchild", "old-child"}, {"foreign-root", ""}, {"revoked-before-registration", "root-2"}} {
		before, err := c.Store.LoadBudget(t.Context(), "turn")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = c.RegisterRun(t.Context(), "turn", binding.id, budget.RunBudget{ParentRunID: binding.parent}); !errors.Is(err, budget.ErrExhausted) {
			t.Fatalf("revoked ancestry admitted: %s %v", binding.id, err)
		}
		after, err := c.Store.LoadBudget(t.Context(), "turn")
		if err != nil || after.Revision != before.Revision || after.View("").Usage.ChildRuns != before.View("").Usage.ChildRuns {
			t.Fatalf("denied ancestry mutated accounting: %v", err)
		}
	}
}

func TestRootGenerationCancellationHasOneScopeAndRunMeaning(t *testing.T) {
	for _, closeOriginal := range []bool{false, true} {
		c := newLedger(t, budget.Limits{MaxLLMCalls: 8})
		if closeOriginal {
			if _, err := c.CancelRoot(t.Context(), "turn", "root"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := testAdmitRootRetry(c, t.Context(), "turn", "inv", "root", "root-2", 2); err != nil {
			t.Fatal(err)
		}
		if _, err := c.RegisterRun(t.Context(), "turn", "root-2", budget.RunBudget{}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.RegisterRun(t.Context(), "turn", "child-2", budget.RunBudget{ParentRunID: "root-2"}); err != nil {
			t.Fatal(err)
		}
		l, err := c.CancelRoot(t.Context(), "turn", "root-2")
		if err != nil || !l.RunCancelled("root-2") || !l.RunCancelled("child-2") || !l.View("").Cancelled || l.Cancelled != closeOriginal {
			t.Fatalf("divergent cancellation after original closure=%t: %+v %v", closeOriginal, l, err)
		}
		l, err = testAdmitRootRetry(c, t.Context(), "turn", "inv", "root-2", "root-3", 3)
		if err != nil || l.RunCancelled("root-3") || l.View("").Cancelled || !l.RunCancelled("root-2") {
			t.Fatalf("next grant changed prior closure: %+v %v", l, err)
		}
		l, err = c.Cancel(t.Context(), "turn")
		if err != nil || !l.RunCancelled("root-3") || !l.View("").Cancelled {
			t.Fatalf("whole-scope Cancel skipped current generation: %+v %v", l, err)
		}
	}
}

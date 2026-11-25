package conformance

import (
	"errors"
	"math"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// RunRelationalReconciliationSuite exercises the same indexed scan, eligibility,
// cursor and durable-watermark contract on both relational implementations.
func RunRelationalReconciliationSuite(t *testing.T, store kernel.Store, relations runrelation.Store, reopen func() runrelation.Store) {
	t.Helper()
	ctx := t.Context()
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	create := func(id string, terminal bool) kernel.Snapshot {
		t.Helper()
		s, e := rt.Create(ctx, kernel.CreateRequest{
			ID: id, Kind: "agent", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "reconcile", State: []byte("{}"),
		})
		if e != nil {
			t.Fatal(e)
		}
		if terminal {
			s, e = rt.Apply(ctx, id, s.Run.Revision, kernel.Mutation{Status: kernel.RunStatusCompleted, State: s.State,
				Result: &kernel.Result{ContentType: "text", Content: []byte(`"done"`)}})
			if e != nil {
				t.Fatal(e)
			}
		}
		return s
	}
	parent := create("parent", false)
	terminalParent := create("terminal-parent", true)
	create("child-01-running", false)
	create("child-02-terminal-parent", true)
	ready := create("child-03-ready", true)
	create("child-04-missing-parent", true)
	ready2 := create("child-05-ready", true)
	inputs := []runrelation.Relation{
		testRunRelation(parent.Run.ID, "child-00-missing", runrelation.KindDelegation, "owner-0", 0),
		testRunRelation(parent.Run.ID, "child-01-running", runrelation.KindDelegation, "owner-1", 1),
		testRunRelation(terminalParent.Run.ID, "child-02-terminal-parent", runrelation.KindDelegation, "owner-2", 2),
		testRunRelation(parent.Run.ID, ready.Run.ID, runrelation.KindDelegation, "owner-3", 3),
		testRunRelation("missing-parent", "child-04-missing-parent", runrelation.KindDelegation, "owner-4", 4),
		testRunRelation(parent.Run.ID, ready2.Run.ID, runrelation.KindDelegation, "owner-5", 5),
	}
	for _, r := range inputs {
		if _, reused, e := relations.Put(ctx, r); e != nil || reused {
			t.Fatalf("Put %s: %t %v", r.ChildRunID, reused, e)
		}
	}
	pager, ok := relations.(runrelation.ReconciliationPager)
	if !ok {
		t.Fatal("first-party relational Store lacks bounded pager")
	}
	check := func(after string, limit int, cursor string, more bool, expected ...runrelation.ReconciliationCandidate) {
		t.Helper()
		page, e := pager.ListReconcilePage(ctx, after, limit)
		if e != nil || page.NextCursor != cursor || page.HasMore != more || len(page.Candidates) != len(expected) {
			t.Fatalf("scan after %q: %+v %v", after, page, e)
		}
		for i := range expected {
			if page.Candidates[i] != expected[i] {
				t.Fatalf("candidate %d: got %+v want %+v", i, page.Candidates[i], expected[i])
			}
		}
	}
	check("", 2, inputs[1].ChildRunID, true)
	check(inputs[1].ChildRunID, 2, ready.Run.ID, true, runrelation.ReconciliationCandidate{Relation: inputs[3]})
	check(ready.Run.ID, 2, ready2.Run.ID, true, runrelation.ReconciliationCandidate{Relation: inputs[5]})
	check(ready2.Run.ID, 2, ready2.Run.ID, false)
	for _, limit := range []int{-1, 0, runrelation.MaxReconcilePageSize + 1} {
		if _, e := pager.ListReconcilePage(ctx, "", limit); !errors.Is(e, runrelation.ErrInvalidInput) {
			t.Fatalf("invalid page limit %d: %v", limit, e)
		}
	}
	for _, pair := range [][2]uint64{{0, 1}, {1, 0}, {math.MaxUint64, 1}, {1, math.MaxUint64}} {
		if e := pager.MarkEnqueued(ctx, ready.Run.ID, pair[0], pair[1]); !errors.Is(e, runrelation.ErrInvalidInput) {
			t.Fatalf("invalid watermark %v: %v", pair, e)
		}
	}
	if e := pager.MarkEnqueued(ctx, "absent-relation", 1, 1); !errors.Is(e, runrelation.ErrNotFound) {
		t.Fatalf("unknown relation watermark: %v", e)
	}
	if e := pager.MarkEnqueued(ctx, ready.Run.ID, parent.Run.Revision, ready.Run.Revision); e != nil {
		t.Fatal(e)
	}
	if e := pager.MarkObserved(ctx, ready.Run.ID, 1, 1); e != nil {
		t.Fatal(e)
	}
	relations = reopen()
	pager, ok = relations.(runrelation.ReconciliationPager)
	if !ok {
		t.Fatal("reopened Store lacks pager")
	}
	check(inputs[1].ChildRunID, 2, ready.Run.ID, true)
	// A later parent revision makes the same original child eligible again.
	next, e := rt.Apply(ctx, parent.Run.ID, parent.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: parent.State})
	if e != nil {
		t.Fatal(e)
	}
	check(inputs[1].ChildRunID, 2, ready.Run.ID, true, runrelation.ReconciliationCandidate{
		Relation: inputs[3], LastParentRevision: parent.Run.Revision, LastChildRevision: ready.Run.Revision,
	})
	if e = pager.MarkObserved(ctx, ready.Run.ID, next.Run.Revision, ready.Run.Revision); e != nil {
		t.Fatal(e)
	}
	if e = pager.MarkEnqueued(ctx, ready.Run.ID, parent.Run.Revision, 1); e != nil {
		t.Fatal(e)
	}
	check(inputs[1].ChildRunID, 2, ready.Run.ID, true)
	if original, e := relations.GetByChild(ctx, ready.Run.ID); e != nil || original != inputs[3] {
		t.Fatalf("progress changed immutable relation: %+v %v", original, e)
	}
}

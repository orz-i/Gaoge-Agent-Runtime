package sqlite_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile/encrypted"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

func TestSQLiteReconciliationScannedPagesAndDurableProgress(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(privateDir(t), "relations.sqlite")
	var key [32]byte
	key[0] = 93
	defer clear(key[:])
	open := encrypted.WithKey(&key)
	s, err := sqlite.CreateNewWith(ctx, path, open)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	registry, err := runrelation.New(sqlite.NewRunRelationStore(s), nil)
	if err != nil {
		t.Fatal(err)
	}
	pager, ok := registry.ReconciliationPager()
	if !ok {
		t.Fatal("missing bounded pager")
	}
	rt, err := kernel.New(kernel.Dependencies{Store: s})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := rt.Create(ctx, sampleCreate("parent"))
	if err != nil {
		t.Fatal(err)
	}
	// Entire first page has no matching child Run. Missing Runs are legal
	// ownership reservations and must not starve a later eligible child.
	for i := range runrelation.MaxReconcilePageSize {
		id := fmt.Sprintf("child-%03d", i)
		if _, err = registry.Ensure(ctx, runrelation.Draft{ParentRunID: parent.Run.ID, ChildRunID: id, OwnerNodeID: id, Kind: runrelation.KindDelegation}); err != nil {
			t.Fatal(err)
		}
	}
	child, err := rt.Create(ctx, sampleCreate("child-z"))
	if err != nil {
		t.Fatal(err)
	}
	relation, err := registry.Ensure(ctx, runrelation.Draft{ParentRunID: parent.Run.ID, ChildRunID: child.Run.ID, OwnerNodeID: "last", Kind: runrelation.KindDelegation})
	if err != nil {
		t.Fatal(err)
	}
	child, err = rt.Apply(ctx, child.Run.ID, child.Run.Revision, kernel.Mutation{Status: kernel.RunStatusCompleted, State: child.State, Result: &kernel.Result{ContentType: "text", Content: []byte(`"done"`)}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := pager.ListReconcilePage(ctx, "", runrelation.MaxReconcilePageSize)
	if err != nil || len(first.Candidates) != 0 || !first.HasMore || first.NextCursor != "child-127" {
		t.Fatalf("empty scanned page %+v %v", first, err)
	}
	last, err := pager.ListReconcilePage(ctx, first.NextCursor, runrelation.MaxReconcilePageSize)
	if err != nil || last.HasMore || last.NextCursor != child.Run.ID || len(last.Candidates) != 1 || last.Candidates[0].Relation != relation {
		t.Fatalf("later candidate %+v %v", last, err)
	}
	for _, limit := range []int{0, -1, runrelation.MaxReconcilePageSize + 1} {
		if _, err = pager.ListReconcilePage(ctx, "", limit); !errors.Is(err, runrelation.ErrInvalidInput) {
			t.Fatal("invalid limit", err)
		}
	}
	if err = pager.MarkEnqueued(ctx, "missing", 1, 1); !errors.Is(err, runrelation.ErrNotFound) {
		t.Fatal("unknown relation", err)
	}
	if err = pager.MarkEnqueued(ctx, child.Run.ID, math.MaxUint64, 1); !errors.Is(err, runrelation.ErrInvalidInput) {
		t.Fatal("integer overflow", err)
	}
	if err = pager.MarkEnqueued(ctx, child.Run.ID, parent.Run.Revision, child.Run.Revision); err != nil {
		t.Fatal(err)
	}
	if err = pager.MarkObserved(ctx, child.Run.ID, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = sqlite.OpenExistingWith(ctx, path, open)
	if err != nil {
		t.Fatal(err)
	}
	relations := sqlite.NewRunRelationStore(s)
	same, err := relations.GetByChild(ctx, child.Run.ID)
	if err != nil || same != relation {
		t.Fatal("immutable identity", err)
	}
	last, err = relations.ListReconcilePage(ctx, first.NextCursor, runrelation.MaxReconcilePageSize)
	if err != nil || len(last.Candidates) != 0 {
		t.Fatal("reopen lost watermarks", err, last)
	}
	rt, err = kernel.New(kernel.Dependencies{Store: s})
	if err != nil {
		t.Fatal(err)
	}
	parent, err = rt.Apply(ctx, parent.Run.ID, parent.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: parent.State})
	if err != nil {
		t.Fatal(err)
	}
	last, err = relations.ListReconcilePage(ctx, first.NextCursor, runrelation.MaxReconcilePageSize)
	if err != nil || len(last.Candidates) != 1 || last.Candidates[0].LastParentRevision != parent.Run.Revision-1 || last.Candidates[0].LastChildRevision != child.Run.Revision {
		t.Fatal("parent revision did not reopen candidate", err, last)
	}
}

type recoveryQueue struct {
	*queue.Memory
	fail bool
}

func (q *recoveryQueue) Enqueue(ctx context.Context, r queue.EnqueueRequest) (queue.EnqueueResult, error) {
	if q.fail {
		return queue.EnqueueResult{}, errors.New("fixture queue unavailable")
	}
	return q.Memory.Enqueue(ctx, r)
}

func TestSQLiteReconciliationExplicitCursorSurvivesRecreatedSchedulers(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(privateDir(t), "reconcile.sqlite")
	s, err := sqlite.CreateNew(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	rt, err := kernel.New(kernel.Dependencies{Store: s})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := runrelation.New(sqlite.NewRunRelationStore(s), nil)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := rt.Create(ctx, sampleCreate("parent"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range runrelation.MaxReconcilePageSize {
		id := fmt.Sprintf("a-%03d", i)
		if _, err = registry.Ensure(ctx, runrelation.Draft{ParentRunID: parent.Run.ID, ChildRunID: id, OwnerNodeID: id, Kind: runrelation.KindDelegation}); err != nil {
			t.Fatal(err)
		}
	}
	child, err := rt.Create(ctx, sampleCreate("z-child"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Ensure(ctx, runrelation.Draft{ParentRunID: parent.Run.ID, ChildRunID: child.Run.ID, OwnerNodeID: "last", Kind: runrelation.KindDelegation}); err != nil {
		t.Fatal(err)
	}
	child, err = rt.Apply(ctx, child.Run.ID, child.Run.Revision, kernel.Mutation{Status: kernel.RunStatusCompleted, State: child.State, Result: &kernel.Result{ContentType: "text", Content: []byte(`"done"`)}})
	if err != nil {
		t.Fatal(err)
	}
	q := &recoveryQueue{Memory: queue.NewMemory(queue.Dependencies{}), fail: true}
	newScheduler := func() *continuation.Scheduler {
		t.Helper()
		reg, err := runrelation.New(sqlite.NewRunRelationStore(s), nil)
		if err != nil {
			t.Fatal(err)
		}
		r, err := kernel.New(kernel.Dependencies{Store: s})
		if err != nil {
			t.Fatal(err)
		}
		scheduler, err := continuation.NewScheduler(continuation.SchedulerDependencies{Outbox: s, Queue: q, Relations: reg, Runs: r, ProjectorID: "short-lived"})
		if err != nil {
			t.Fatal(err)
		}
		return scheduler
	}
	cursor, err := newScheduler().ReconcilePage(ctx, "")
	if err != nil || cursor != "a-127" {
		t.Fatal("empty first page", cursor, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = sqlite.OpenExisting(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	// Failed enqueue is reported, no watermark advanced. Wrap reconsiders
	// the same original child without pinning all later IDs behind it.
	next, err := newScheduler().ReconcilePage(ctx, cursor)
	if err == nil || next != "" {
		t.Fatal("queue failure", next, err)
	}
	page, err := sqlite.NewRunRelationStore(s).ListReconcilePage(ctx, cursor, 128)
	if err != nil || len(page.Candidates) != 1 {
		t.Fatal("failed enqueue falsely acknowledged", page, err)
	}
	q.fail = false
	cursor, err = newScheduler().ReconcilePage(ctx, next)
	if err != nil || cursor == "" {
		t.Fatal(err)
	}
	if next, err = newScheduler().ReconcilePage(ctx, cursor); err != nil || next != "" {
		t.Fatal("second page", next, err)
	}
	jobs, err := q.List(ctx, continuation.QueueName, queue.StatusQueued)
	if err != nil || len(jobs) != 1 || !strings.Contains(jobs[0].ClientJobID, child.Run.ID) {
		t.Fatal("original wakeup", jobs, err)
	}
	// Simulated lost cursor ACK: scan the same page with a new scheduler.
	if _, err = newScheduler().ReconcilePage(ctx, cursor); err != nil {
		t.Fatal(err)
	}
	jobs, err = q.List(ctx, continuation.QueueName, queue.StatusQueued)
	if err != nil || len(jobs) != 1 {
		t.Fatal("duplicate logical wakeup", jobs, err)
	}
}

func TestSQLiteReconciliationNoCrossTenantOrTerminalParent(t *testing.T) {
	ctx := t.Context()
	s := openStore(t)
	relations, err := runrelation.New(sqlite.NewRunRelationStore(s), nil)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := kernel.New(kernel.Dependencies{Store: s})
	if err != nil {
		t.Fatal(err)
	}
	q := queue.NewMemory(queue.Dependencies{})
	scheduler, err := continuation.NewScheduler(continuation.SchedulerDependencies{Outbox: s, Queue: q, Relations: relations, Runs: rt})
	if err != nil {
		t.Fatal(err)
	}
	p, err := rt.Create(ctx, sampleCreate("parent"))
	if err != nil {
		t.Fatal(err)
	}
	r := sampleCreate("foreign-child")
	r.Actor.TenantID = "foreign"
	child, err := rt.Create(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	_, err = rt.Apply(ctx, child.Run.ID, child.Run.Revision, kernel.Mutation{Status: kernel.RunStatusCompleted, State: child.State, Result: &kernel.Result{ContentType: "text", Content: []byte(`"done"`)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = relations.Ensure(ctx, runrelation.Draft{ParentRunID: p.Run.ID, ChildRunID: child.Run.ID, OwnerNodeID: "member", Kind: runrelation.KindDelegation}); err != nil {
		t.Fatal(err)
	}
	if _, err = scheduler.ReconcilePage(ctx, ""); !errors.Is(err, continuation.ErrInvalidInput) {
		t.Fatal("cross-tenant", err)
	}
	jobs, err := q.List(ctx, continuation.QueueName, queue.StatusQueued)
	if err != nil || len(jobs) != 0 {
		t.Fatal("cross-tenant enqueued", err)
	}
	_, err = rt.Apply(ctx, p.Run.ID, p.Run.Revision, kernel.Mutation{Status: kernel.RunStatusCompleted, State: p.State, Result: &kernel.Result{ContentType: "text", Content: []byte(`"done"`)}})
	if err != nil {
		t.Fatal(err)
	}
	pager, _ := relations.ReconciliationPager()
	page, err := pager.ListReconcilePage(ctx, "", 1)
	if err != nil || len(page.Candidates) != 0 || !page.HasMore || page.NextCursor != child.Run.ID {
		t.Fatal("terminal parent", page, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = pager.ListReconcilePage(canceled, "", 1); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel", err)
	}
}

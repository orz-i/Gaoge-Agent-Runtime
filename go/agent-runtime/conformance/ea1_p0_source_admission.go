package conformance

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// EA-1 P0: negative identity and version matrix, via original Kernel + Store.
// Frozen Host Model/Tool/Context/Attempt origins remain separate cells.
func RunEA1P0SourceAdmissionSuite(t *testing.T, makeStore func(*testing.T) kernel.Store) {
	t.Helper()
	t.Run("foreign tenancy actor thread missing and terminal parent", func(t *testing.T) {
		rt, err := kernel.New(kernel.Dependencies{Store: makeStore(t)})
		if err != nil {
			t.Fatal(err)
		}
		actor := kernel.ActorRef{TenantID: "tenant-one", ActorID: "actor-one"}
		thread := kernel.ThreadRef{Kind: "conversation", ID: "original-thread"}
		parent, err := rt.Create(t.Context(), kernel.CreateRequest{
			ID: "original-parent", Kind: "workflow", Actor: actor, Thread: thread,
			Goal: "original parent", State: []byte(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		cases := []struct {
			label    string
			actor    kernel.ActorRef
			thread   kernel.ThreadRef
			parentID string
		}{
			{"foreign-tenant", kernel.ActorRef{TenantID: "other-tenant", ActorID: "actor-one"}, thread, parent.Run.ID},
			{"foreign-actor", kernel.ActorRef{TenantID: "tenant-one", ActorID: "other-actor"}, thread, parent.Run.ID},
			{"foreign-thread", actor, kernel.ThreadRef{Kind: "conversation", ID: "other-thread"}, parent.Run.ID},
			{"missing-parent", actor, thread, "does-not-exist"},
		}
		for _, c := range cases {
			t.Run(c.label, func(t *testing.T) {
				id := "denied-" + c.label
				_, createErr := rt.Create(t.Context(), kernel.CreateRequest{
					ID: id, Kind: "agent", ParentRunID: c.parentID, Actor: c.actor,
					Thread: c.thread, Goal: "foreign child origin", State: []byte(`{}`),
				})
				if createErr == nil {
					t.Fatal("foreign child origin accepted at Kernel Create")
				}
				if _, err = rt.Load(t.Context(), id); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("denied child persisted: %v", err)
				}
				if _, err = rt.LookupChildExecution(t.Context(), id); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("denied child acquired a physical Grant: %v", err)
				}
			})
		}
		current, err := rt.Load(t.Context(), parent.Run.ID)
		if err != nil || current.Run.Revision != parent.Run.Revision {
			t.Fatalf("foreign attempt modified original parent: %+v err=%v", current.Run, err)
		}
		if _, err = rt.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "terminal after invalid sources"); err != nil {
			t.Fatalf("foreign source attempts stranded parent Cancel: %v", err)
		}
		_, err = rt.Create(t.Context(), kernel.CreateRequest{
			ID: "late-child", Kind: "agent", ParentRunID: parent.Run.ID,
			Actor: actor, Thread: thread, Goal: "too late", State: []byte(`{}`),
		})
		if !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("terminal parent authorized new child: %v", err)
		}
		if _, err = rt.Load(t.Context(), "late-child"); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("late child persisted despite terminal parent: %v", err)
		}
	})
	t.Run("wrong kind duplicate run and stale/future expected Revision", func(t *testing.T) {
		rt, err := kernel.New(kernel.Dependencies{Store: makeStore(t)})
		if err != nil {
			t.Fatal(err)
		}
		actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
		thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
		root, err := rt.Create(t.Context(), kernel.CreateRequest{
			ID: "root", Kind: "workflow", Actor: actor, Thread: thread,
			Goal: "original owner", State: []byte(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = rt.Create(t.Context(), kernel.CreateRequest{
			ID: root.Run.ID, Kind: "workflow", Actor: actor, Thread: thread,
			Goal: "different original owner", State: []byte(`{}`),
		}); err == nil {
			t.Fatal("duplicate Run ID accepted a foreign source")
		}
		foreign, err := rt.Create(t.Context(), kernel.CreateRequest{
			ID: "unsupported", Kind: "unsupported.feature", Actor: actor, Thread: thread,
			Goal: "unsupported feature", State: []byte(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = rt.BeginChildExecution(t.Context(), foreign.Run.ID, foreign.Run.Revision); !errors.Is(err, kernel.ErrExecutionRequired) {
			t.Fatalf("unsupported kind acquired external physical authority: %v", err)
		}
		next, err := rt.Apply(t.Context(), root.Run.ID, root.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: root.State,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, rev := range []uint64{root.Run.Revision, next.Run.Revision + 1} {
			if _, err = rt.BeginChildExecution(t.Context(), root.Run.ID, rev); !errors.Is(err, kernel.ErrExecutionConflict) {
				t.Fatalf("stale/future expected Revision %d admitted physical Grant: %v", rev, err)
			}
		}
		if _, err = rt.BeginChildExecution(t.Context(), root.Run.ID, 0); !errors.Is(err, kernel.ErrInvalidInput) {
			t.Fatalf("zero expected Revision authorized physical Grant: %v", err)
		}
		if _, err = rt.LookupChildExecution(t.Context(), root.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("invalid admission left an original Grant: %v", err)
		}
		grant, err := rt.BeginChildExecution(t.Context(), root.Run.ID, next.Run.Revision)
		if err != nil {
			t.Fatalf("valid original Revision denied: %v", err)
		}
		if grant.ExpectedRevision != next.Run.Revision {
			t.Fatalf("original Revision not sealed: %+v", grant)
		}
		if _, err = rt.BeginChildExecution(t.Context(), root.Run.ID, next.Run.Revision); !errors.Is(err, kernel.ErrChildExecutionBusy) {
			t.Fatalf("duplicate original physical admission accepted: %v", err)
		}
	})
}

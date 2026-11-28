package continuatione2e

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	a2a "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-a2a"
	postgresadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres"
	sqliteadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// SQL integration checks the same two physical-control admission orders as
// the A2A MemoryStore race suite, using independent database connections.
// Suspending AFTER a committed Store Apply faithfully models a descheduled
// Host process: in-memory mutexes must not be the source of authorization.
type sqlA2AControlPause struct {
	kernel.Store
	event     string
	committed chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (s *sqlA2AControlPause) Commit(commitCtx context.Context, commitID string, commitRev uint64, commitMutation kernel.StoreMutation, commitAuth kernel.CommitAuthority) (kernel.Snapshot, bool, error) {
	if commitAuth.Lease == nil && commitAuth.Control == nil && commitAuth.Recovery == nil {
		snapshot, _, err := s.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
		if err != nil {
			return snapshot, false, err
		}
		for _, event := range commitMutation.Events {
			if event.Type != s.event {
				continue
			}
			s.once.Do(func() { close(s.committed) })
			select {
			case <-s.release:
			case <-commitCtx.Done():
				return snapshot, false, commitCtx.Err()
			}
		}
		return snapshot, false, nil
	}
	return s.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
}

type sqlA2AControlRemote struct {
	mu                            sync.Mutex
	sends, resumes, gets, cancels int
}

func (r *sqlA2AControlRemote) SendMessage(_ context.Context, _ a2a.Discovery, req a2a.SendRequest) (a2a.Interaction, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sends++
	if req.TaskID != "" {
		r.resumes++
	}
	state := "TASK_STATE_INPUT_REQUIRED"
	task := a2a.TaskSnapshot{ID: "sql-task", ContextID: "sql-context", State: state,
		Raw: json.RawMessage(`{"id":"sql-task","contextId":"sql-context","status":{"state":"TASK_STATE_INPUT_REQUIRED"}}`)}
	return a2a.Interaction{Task: &task, Raw: task.Raw}, nil
}
func (r *sqlA2AControlRemote) GetTask(context.Context, a2a.Discovery, string) (a2a.TaskSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gets++
	return a2a.TaskSnapshot{ID: "sql-task", ContextID: "sql-context", State: "TASK_STATE_INPUT_REQUIRED",
		Raw: json.RawMessage(`{"id":"sql-task","contextId":"sql-context","status":{"state":"TASK_STATE_INPUT_REQUIRED"}}`)}, nil
}
func (r *sqlA2AControlRemote) CancelTask(context.Context, a2a.Discovery, string) (a2a.TaskSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancels++
	return a2a.TaskSnapshot{ID: "sql-task", ContextID: "sql-context", State: "TASK_STATE_CANCELED", Terminal: true,
		Raw: json.RawMessage(`{"id":"sql-task","contextId":"sql-context","status":{"state":"TASK_STATE_CANCELED"}}`)}, nil
}
func (r *sqlA2AControlRemote) counts() (sends, resumes, gets, cancels int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sends, r.resumes, r.gets, r.cancels
}
func a2aSQLPlugin(t *testing.T, store kernel.Store, remote a2a.RemoteClient) (*a2a.Plugin, *kernel.Runtime) {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	plugin, err := a2a.NewPlugin(a2a.PluginDependencies{
		Runtime: runtime, AttestationKey: []byte("SQL-test-private-A2A-control-proof-key-2026"),
		Bindings: a2a.BindingResolverFunc(func(_ context.Context, target, revision string) (a2a.Binding, error) {
			return a2a.Binding{TargetID: target, Revision: "revision-1", Client: remote,
				Discovery: a2a.Discovery{Descriptor: a2a.RemoteAgentDescriptor{
					Name: "sql-a2a", PreferredURL: "https://sql-a2a.example/agent", ProtocolVersion: a2a.ProtocolVersion,
				}}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return plugin, runtime
}

func sqlA2AOriginalWait(t *testing.T, plugin *a2a.Plugin, id string) (handoff.ChildRunner, kernel.Snapshot) {
	t.Helper()
	child, err := plugin.ResolveChild(t.Context(), handoff.Delegation{MemberID: "a2a:remote", MemberRevision: "revision-1", ChildRunID: id})
	if err != nil {
		t.Fatal(err)
	}
	created, err := child.StartRun(t.Context(), agent.StartRequest{
		ID: id, Goal: "original SQL input challenge", Actor: kernel.ActorRef{TenantID: "test", ActorID: "agent"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "sql-control"},
	})
	if err != nil || created.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("SQL first remote wait failed: %+v %v", created.Run, err)
	}
	return child, created
}

func testSQLA2AControlFencing(t *testing.T, first, second kernel.Store) {
	for _, order := range []string{"resume-first", "cancel-first"} {
		t.Run(order, func(t *testing.T) {
			paused := &sqlA2AControlPause{Store: first, event: "a2a.remote_resume_requested",
				committed: make(chan struct{}), release: make(chan struct{})}
			if order == "cancel-first" {
				paused.event = "a2a.remote_cancel_requested"
			}
			var once sync.Once
			release := func() { once.Do(func() { close(paused.release) }) }
			defer release()
			remote := &sqlA2AControlRemote{}
			issuer, _ := a2aSQLPlugin(t, paused, remote)
			observer, rt := a2aSQLPlugin(t, second, remote)
			runID := "sql-control-" + order
			_, original := sqlA2AOriginalWait(t, issuer, runID)
			outcome := make(chan error, 1)
			go func() {
				if order == "resume-first" {
					_, err := issuer.ResumeRun(t.Context(), runID, original.Run.Revision, "approved")
					outcome <- err
				} else {
					_, err := issuer.Cancel(t.Context(), runID, original.Run.Revision, "stop")
					outcome <- err
				}
			}()
			select {
			case <-paused.committed:
			case <-time.After(5 * time.Second):
				t.Fatal("SQL execution intent did not commit")
			}
			persisted, err := rt.Load(t.Context(), runID)
			if err != nil || persisted.Run.Status != kernel.RunStatusRunning || persisted.Run.Revision != original.Run.Revision+1 {
				t.Fatalf("SQL CAS not visible on second connection: %+v %v", persisted.Run, err)
			}
			// A fresh Control on the current revision cannot bypass the other
			// process's committed physical send/cancel intent.
			if order == "resume-first" {
				_, err = observer.Cancel(t.Context(), runID, persisted.Run.Revision, "stop")
			} else {
				_, err = observer.ResumeRun(t.Context(), runID, persisted.Run.Revision, "late approval")
			}
			if !errors.Is(err, a2a.ErrRemoteOutcomeUnknown) {
				t.Fatalf("SQL cross-connection control bypassed pending admission: %v", err)
			}
			if sends, resumes, gets, cancels := remote.counts(); sends != 1 || resumes != 0 || cancels != 0 ||
				gets != map[string]int{"resume-first": 0, "cancel-first": 1}[order] {
				t.Fatalf("remote side effect before release: %d/%d/%d/%d", sends, resumes, gets, cancels)
			}
			release()
			select {
			case err := <-outcome:
				if err != nil {
					t.Fatalf("original SQL physical control failed: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("original SQL control did not finish")
			}
			if sends, resumes, _, cancels := remote.counts(); (order == "resume-first" && (sends != 2 || resumes != 1 || cancels != 0)) ||
				(order == "cancel-first" && (sends != 1 || resumes != 0 || cancels != 1)) {
				t.Fatalf("physical effects reordered by another SQL connection: %d/%d/%d", sends, resumes, cancels)
			}
		})
	}
}

// The integration runner uses real PostgreSQL for the same Store-serialized
// race that Memory and SQLite handle locally. No external A2A network is used.
func TestRealPostgresAndSQLiteA2AControlAdmissionAcrossConnections(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "a2a-control.sqlite")
		// SQLite's desktop Profile adapter deliberately rejects a world-
		// readable directory even in tests. testing.TempDir permissions vary
		// by platform, so tighten the exact profile directory explicitly.
		if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		first, err := sqliteadapter.CreateNew(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = first.Close() })
		second, err := sqliteadapter.OpenExisting(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = second.Close() })
		testSQLA2AControlFencing(t, first, second)
	})
	t.Run("postgres", func(t *testing.T) {
		dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
		if dsn == "" {
			t.Skip("requires disposable PostgreSQL test service")
		}
		firstDB, isolated := managedRecoveryDatabase(t, dsn)
		if err := postgresadapter.Migrate(firstDB); err != nil {
			t.Fatal(err)
		}
		secondDB := openManagedRecoveryDB(t, isolated)
		testSQLA2AControlFencing(t, postgresadapter.NewKernelStore(firstDB), postgresadapter.NewKernelStore(secondDB))
	})
}

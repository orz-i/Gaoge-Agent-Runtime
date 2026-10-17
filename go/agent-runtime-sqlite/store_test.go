package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func openStore(t *testing.T) *sqlite.Store {
	t.Helper()
	s, err := sqlite.CreateNew(t.Context(), filepath.Join(privateDir(t), "runtime.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestKernelControlStoreConformance(t *testing.T) {
	conformance.RunKernelControlSuite(t, func(child *testing.T) kernel.Store { return openStore(child) })
}

func TestKernelExecutionStoreConformance(t *testing.T) {
	conformance.RunKernelExecutionSuite(t, func(child *testing.T) kernel.Store {
		return openStore(child)
	})
}

func TestKernelQuarantineStoreConformance(t *testing.T) {
	conformance.RunKernelQuarantineSuite(t, func(child *testing.T) kernel.Store {
		return openStore(child)
	})
}

func TestKernelStoreConformance(t *testing.T) {
	conformance.RunKernelStoreSuite(t, func(tb testing.TB) kernel.Store {
		tb.Helper()
		// TB in conformance is *testing.T for this suite.
		child, ok := tb.(*testing.T)
		if !ok {
			tb.Fatal("unexpected TB")
		}
		return openStore(child)
	})
}

func sampleCreate(id string) kernel.CreateRequest {
	return kernel.CreateRequest{
		ID: id, Kind: kernel.RunKind("agent"),
		Actor:  kernel.ActorRef{TenantID: "local", ActorID: "profile-1"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread-1"},
		Goal:   "durable local agent", State: json.RawMessage(`{"messages":[{"role":"user","content":"hello"}]}`),
	}
}

func TestReopenSnapshotCheckpointEventAndTransition(t *testing.T) {
	path := filepath.Join(privateDir(t), "runtime.sqlite")
	ctx := t.Context()
	first, err := sqlite.CreateNew(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := kernel.New(kernel.Dependencies{Store: first})
	if err != nil {
		t.Fatal(err)
	}
	req := sampleCreate("reopen")
	req.Events = []kernel.EventDraft{{Type: "agent.started", Wakeup: true, WakeupGeneration: 13}}
	created, err := runtime.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	at := created.Run.UpdatedAt.Add(time.Hour)
	paused, err := runtime.Apply(ctx, req.ID, created.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusWaitingInput,
		State:  json.RawMessage(`{"pendingApproval":"tool-1"}`),
		Checkpoint: &kernel.Checkpoint{
			ID: "approval-1", Kind: "approval", Status: kernel.CheckpointPending,
			Payload: json.RawMessage(`{"callID":"tool-1"}`), CreatedAt: created.Run.UpdatedAt,
		},
		Events: []kernel.EventDraft{{Type: "approval.pending", Wakeup: true, WakeupAt: &at, WakeupGeneration: 14}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if paused.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("paused=%+v", paused.Run)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	second, err := kernel.New(kernel.Dependencies{Store: restored})
	if err != nil {
		t.Fatal(err)
	}
	state, err := second.Load(ctx, req.ID)
	if err != nil || state.Run.Revision != paused.Run.Revision ||
		state.Run.Status != kernel.RunStatusWaitingInput ||
		state.Checkpoint == nil || state.Checkpoint.ID != "approval-1" {
		t.Fatalf("reopen failed: state=%#v err=%v", state, err)
	}
	events, err := second.ListEvents(ctx, req.ID, 0, 20)
	if err != nil || len(events) != 3 {
		t.Fatalf("event history=%#v err=%v", events, err)
	}
	claims, err := restored.ClaimTransitions(ctx, kernel.TransitionClaimRequest{
		WorkerID: "local-activation", Limit: 8, LeaseDuration: 2 * time.Minute, Now: at,
	})
	if err != nil || len(claims) != 2 {
		t.Fatalf("restored ready=%#v err=%v", claims, err)
	}
	if claims[0].Transition.Events[1].WakeupGeneration != 13 ||
		claims[1].Transition.Events[0].WakeupGeneration != 14 ||
		claims[1].Transition.Events[0].WakeupAt == nil ||
		!claims[1].Transition.Events[0].WakeupAt.Equal(at) {
		t.Fatalf("projection metadata corrupted: %#v", claims)
	}
}

func TestAtomicRollbackWhenJournalReadyOrProofInsertFails(t *testing.T) {
	for _, failTable := range []string{"runtime_events", "runtime_ready_work", "runtime_commit_proofs"} {
		t.Run(failTable, func(t *testing.T) {
			path := filepath.Join(privateDir(t), "atomic.sqlite")
			s, err := sqlite.CreateNew(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			runtime, err := kernel.New(kernel.Dependencies{Store: s})
			if err != nil {
				t.Fatal(err)
			}
			req := sampleCreate("atomic")
			created, err := runtime.Create(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			rawDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rawDB.Close() }()
			_, err = rawDB.Exec("CREATE TRIGGER abort_apply BEFORE INSERT ON " + failTable +
				" BEGIN SELECT RAISE(ABORT, 'injected write failure'); END")
			if err != nil {
				t.Fatal(err)
			}
			_, err = runtime.Apply(t.Context(), created.Run.ID, created.Run.Revision, kernel.Mutation{
				Status: kernel.RunStatusRunning,
				State:  json.RawMessage(`{"next":"must-not-commit"}`),
				Events: []kernel.EventDraft{{Type: "agent.resume", Wakeup: true}},
			})
			if err == nil {
				t.Fatal("expected injected atomic commit failure")
			}
			loaded, loadErr := runtime.Load(t.Context(), created.Run.ID)
			if loadErr != nil || loaded.Run.Revision != created.Run.Revision || loaded.EventHead != 1 ||
				string(loaded.State) != string(req.State) {
				t.Fatalf("failed commit leaked state: snapshot=%#v err=%v", loaded, loadErr)
			}
			var ready, proofs int
			if err = rawDB.QueryRow("SELECT count(*) FROM runtime_ready_work WHERE run_id=?", created.Run.ID).Scan(&ready); err != nil {
				t.Fatal(err)
			}
			if err = rawDB.QueryRow("SELECT count(*) FROM runtime_commit_proofs WHERE run_id=?", created.Run.ID).Scan(&proofs); err != nil {
				t.Fatal(err)
			}
			if ready != 0 || proofs != 1 {
				t.Fatalf("failed mutation leaked ready=%d proofs=%d", ready, proofs)
			}
		})
	}
}

func TestRejectUnknownSchemaWithoutDataLoss(t *testing.T) {
	path := filepath.Join(privateDir(t), "foreign.sqlite")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE user_data(data TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO user_data VALUES('keep')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = sqlite.Open(context.Background(), path)
	if !errors.Is(err, sqlite.ErrUnsupportedSchema) {
		t.Fatalf("unversioned unknown database: %v", err)
	}
	db, err = sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM user_data`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("corrupt foreign database count=%d err=%v", count, err)
	}
}

func privateDir(tb testing.TB) string {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), "private-profile")
	if err := os.Mkdir(path, 0700); err != nil {
		tb.Fatal(err)
	}
	return path
}

package harnesspostgres_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type pr72RealContextClock struct{}

func (pr72RealContextClock) Now() time.Time { return time.Now().UTC() }

type pr72RealContextModel struct{ calls *atomic.Int32 }

func (m pr72RealContextModel) Generate(context.Context, model.Request) (model.Response, error) {
	if m.calls != nil {
		m.calls.Add(1)
	}
	return model.Response{Content: "answered"}, nil
}

var errPR72RealContextTransient = errors.New("audit: second connection Context Store read failed before dispatch")

type pr72RealContextFirstStore struct {
	harness.Store
	baseID  string
	blocked atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (s *pr72RealContextFirstStore) GetContextCheckpoint(ctx context.Context, id string) (runtimecontext.Checkpoint, error) {
	if id == s.baseID && s.blocked.CompareAndSwap(false, true) {
		close(s.entered)
		<-s.release
	}
	return s.Store.GetContextCheckpoint(ctx, id)
}

type pr72RealContextReplayFaultStore struct {
	harness.Store
	baseID string
	once   atomic.Bool
}

func (s *pr72RealContextReplayFaultStore) GetContextCheckpoint(ctx context.Context, id string) (runtimecontext.Checkpoint, error) {
	if id == s.baseID && s.once.CompareAndSwap(false, true) {
		return runtimecontext.Checkpoint{}, errPR72RealContextTransient
	}
	return s.Store.GetContextCheckpoint(ctx, id)
}

func TestRealPostgresPR72ConcurrentStartContextFailureDoesNotTerminalizeOriginal(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN required")
	}
	db, isolated := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	firstStore, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(isolated)
	if err != nil {
		t.Fatal(err)
	}
	secondDB, err := gorm.Open(postgres.Open(parsed.String()), realHarnessPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := secondDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	secondStore, err := harnesspostgres.New(secondDB)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	newRunner := func(store harness.Store) *harness.Runner {
		t.Helper()
		ag, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: pr72RealContextModel{calls: &calls}})
		if e != nil {
			t.Fatal(e)
		}
		runner, e := harness.NewRunner(harness.Dependencies{
			Runtime: rt, Agent: ag, Store: store, Clock: pr72RealContextClock{},
			Context: runtimecontext.NewManager(runtimecontext.Dependencies{}),
		})
		if e != nil {
			t.Fatal(e)
		}
		return runner
	}
	req := harness.StartRequest{
		HostThread: harness.HostRef{Kind: "conversation", ID: "pr72-postgres-context-thread"},
		HostTurn:   harness.HostRef{Kind: "conversation_turn", ID: "pr72-postgres-context-turn"},
		Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread:     kernel.ThreadRef{Kind: "conversation", ID: "pr72-postgres-context-thread"},
		Goal:       "answer directly",
		Config:     harness.ConfigSnapshot{Model: "model", Environment: harness.VersionRef{ID: "general", Revision: 1}},
	}
	// Active Context selection now precedes admission. Establish a source and
	// inject at its frozen read instead, preserving the original post-admission,
	// pre-dispatch race tested by both SQL connections.
	source := req
	source.HostTurn.ID += "-source"
	source.Goal = "prior source"
	source.Context = &harness.ContextSeed{
		SourcePath: []string{"pr72-source"},
		Entries: []runtimecontext.Entry{{
			ID: "pr72-entry", SourceID: "pr72-source", TurnID: "pr72-source-turn",
			Message: model.Message{Role: model.RoleUser, Content: model.TextParts("prior source")},
		}},
	}
	prior, err := newRunner(firstStore).StartAgentTurn(t.Context(), source)
	if err != nil || prior.Turn.Status != harness.TurnCompleted || prior.Turn.ContextCheckpointID == "" {
		t.Fatalf("Context source setup failed: status=%s err=%v", prior.Turn.Status, err)
	}
	req.Context = &harness.ContextSeed{
		SourcePath: []string{"pr72-source", "pr72-current"},
		Entries: append(runtimecontext.CloneEntries(source.Context.Entries), runtimecontext.Entry{
			ID: "pr72-current-entry", SourceID: "pr72-current", TurnID: "pr72-current-turn", Required: true,
			Message: model.Message{Role: model.RoleUser, Content: model.TextParts(req.Goal)},
		}),
	}
	blocking := &pr72RealContextFirstStore{
		Store: firstStore, baseID: prior.Turn.ContextCheckpointID,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	release := sync.OnceFunc(func() { close(blocking.release) })
	defer release()
	first := newRunner(blocking)
	second := newRunner(&pr72RealContextReplayFaultStore{Store: secondStore, baseID: blocking.baseID})
	type outcome struct {
		snapshot harness.Snapshot
		err      error
	}
	pending := make(chan outcome, 1)
	go func() { s, e := first.StartAgentTurn(context.Background(), req); pending <- outcome{s, e} }()
	select {
	case <-blocking.entered:
	case <-time.After(8 * time.Second):
		t.Fatal("original Start did not reach its frozen Context source read")
	}
	sid, err := harness.SessionID(req.HostThread)
	if err != nil {
		t.Fatal(err)
	}
	tid, err := harness.TurnID(sid, req.HostTurn)
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := secondStore.GetTurn(t.Context(), tid)
	if err != nil {
		t.Fatal(err)
	}
	if admitted.Status != harness.TurnAccepted || admitted.ContextCheckpointID != "" || calls.Load() != 1 {
		t.Fatalf("fault was not at the admitted pre-dispatch boundary: turn=%+v calls=%d", admitted, calls.Load())
	}
	_, replayErr := second.StartAgentTurn(t.Context(), req)
	if !errors.Is(replayErr, errPR72RealContextTransient) {
		t.Errorf("second connection Context fault lost: %v", replayErr)
	}
	before, err := secondStore.GetTurn(t.Context(), tid)
	if err != nil {
		t.Fatal(err)
	}
	if before != admitted || calls.Load() != 1 {
		t.Errorf("second SQL connection changed the admitted owner: before=%+v after=%+v calls=%d", admitted, before, calls.Load())
	}
	release()
	select {
	case result := <-pending:
		if result.err != nil || result.snapshot.Turn.Status != harness.TurnCompleted {
			t.Errorf("original SQL-backed Start failed: %s %v", result.snapshot.Turn.Status, result.err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("original SQL-backed Start hung")
	}
	after, err := secondStore.GetTurn(t.Context(), tid)
	if err != nil || after.Status != harness.TurnCompleted {
		t.Fatalf("final Turn=%s err=%v", after.Status, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected one source and one business call, got %d", calls.Load())
	}
}

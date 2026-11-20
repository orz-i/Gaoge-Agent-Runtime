package harness_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

var errPR72TransientContextRead = errors.New("audit: transient Context Store read failure")

type pr72ReplayContextReadStore struct {
	harness.Store
	reads   atomic.Int32
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (s *pr72ReplayContextReadStore) GetContextCheckpoint(ctx context.Context, id string) (runtimecontext.Checkpoint, error) {
	if !s.armed.Load() {
		return s.Store.GetContextCheckpoint(ctx, id)
	}
	switch s.reads.Add(1) {
	case 1:
		close(s.entered)
		<-s.release
	case 2:
		return runtimecontext.Checkpoint{}, errPR72TransientContextRead
	}
	return s.Store.GetContextCheckpoint(ctx, id)
}
func TestPR72DirectStartContextReplayReadFailureCannotTerminalizeOriginal(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: directModel{}})
	if err != nil {
		t.Fatal(err)
	}
	backing := harness.NewMemoryStore()
	store := &pr72ReplayContextReadStore{Store: backing, entered: make(chan struct{}), release: make(chan struct{})}
	release := sync.OnceFunc(func() {
		close(store.release)
	})
	defer release()
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: direct, Store: store, Clock: fixedClock{}, Context: runtimecontext.NewManager(runtimecontext.Dependencies{})})
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	base := pr72PrepareContextSource(t, runner, request, "frozen instructions")
	store.armed.Store(true)
	request.Context = contextSeed("frozen instructions", base, contextEntry("pr72-current", "pr72-turn", model.RoleUser, "answer directly", true))
	type outcome struct {
		snapshot harness.Snapshot
		err      error
	}
	first := make(chan outcome, 1)
	go func() {
		snapshot, e := runner.StartAgentTurn(context.Background(), request)
		first <- outcome{snapshot, e}
	}()
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("original Start never reached its frozen Context base read")
	}
	_, replayErr := runner.StartAgentTurn(t.Context(), request)
	if !errors.Is(replayErr, errPR72TransientContextRead) {
		t.Errorf("replay did not surface read failure: %v", replayErr)
	}
	sessionID, err := harness.SessionID(request.HostThread)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := harness.TurnID(sessionID, request.HostTurn)
	if err != nil {
		t.Fatal(err)
	}
	before, err := backing.GetTurn(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if before.Status != harness.TurnAccepted {
		t.Errorf("replay terminalized in-flight owner before physical dispatch: %s", before.Status)
	}
	select {
	case original := <-first:
		if original.err != nil || original.snapshot.Turn.Status != harness.TurnCompleted {
			t.Errorf("original Start failed: status=%s err=%v", original.snapshot.Turn.Status, original.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original Start hung after conflicting replay")
	}
}

type pr72FirstContextReadFailsStore struct {
	harness.Store
	once  atomic.Bool
	armed atomic.Bool
}

func (s *pr72FirstContextReadFailsStore) GetContextCheckpoint(ctx context.Context, id string) (runtimecontext.Checkpoint, error) {
	if s.armed.Load() && s.once.CompareAndSwap(false, true) {
		return runtimecontext.Checkpoint{}, errPR72TransientContextRead
	}
	return s.Store.GetContextCheckpoint(ctx, id)
}
func TestPR72OriginalContextReadFailureLeavesRetryableAdmission(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: directModel{}})
	if err != nil {
		t.Fatal(err)
	}
	backing := harness.NewMemoryStore()
	store := &pr72FirstContextReadFailsStore{Store: backing}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: direct, Store: store, Clock: fixedClock{}, Context: runtimecontext.NewManager(runtimecontext.Dependencies{})})
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	base := pr72PrepareContextSource(t, runner, request, "instructions")
	store.armed.Store(true)
	request.Context = contextSeed("instructions", base, contextEntry("pr72-newcurrent", "pr72-newturn", model.RoleUser, "answer directly", true))
	_, err = runner.StartAgentTurn(t.Context(), request)
	if !errors.Is(err, errPR72TransientContextRead) {
		t.Fatalf("failed to inject first Context read: %v", err)
	}
	sessionID, err := harness.SessionID(request.HostThread)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := harness.TurnID(sessionID, request.HostTurn)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := backing.GetTurn(t.Context(), turnID)
	if err != nil || accepted.Status != harness.TurnAccepted {
		t.Fatalf("initial read failure made Turn irrecoverable: status=%s err=%v", accepted.Status, err)
	}
	recovered, err := runner.StartAgentTurn(t.Context(), request)
	if err != nil || recovered.Turn.Status != harness.TurnCompleted {
		t.Fatalf("idempotent new Start recovery failed: status=%s err=%v", recovered.Turn.Status, err)
	}
}

// Context selection now precedes admission. Inject the original post-admission
// fault at the frozen base read, keeping these recovery/terminalization tests
// at their original physical boundary instead of failing an earlier preflight.
func pr72PrepareContextSource(t *testing.T, runner *harness.Runner, request harness.StartRequest, instructions string) runtimecontext.Entry {
	t.Helper()
	base := contextEntry("pr72-source", "pr72-source", model.RoleUser, "prior source", false)
	prior := request
	prior.HostTurn.ID += "-source"
	prior.Goal = "prior source"
	prior.Context = contextSeed(instructions, base)
	if result, err := runner.StartAgentTurn(t.Context(), prior); err != nil || result.Turn.Status != harness.TurnCompleted {
		t.Fatalf("Context source setup failed: %v", err)
	}
	return base
}

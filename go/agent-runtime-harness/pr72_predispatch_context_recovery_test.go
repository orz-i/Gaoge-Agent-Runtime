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

// The first request has created its immutable Turn but has not finished
// preparing Context. The second, identical replay must never terminalize the
// shared Turn when its independent read-only preflight fails.
type pr72ReplayContextReadStore struct {
	harness.Store
	reads   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (s *pr72ReplayContextReadStore) GetActiveContextCheckpoint(ctx context.Context, scope string) (runtimecontext.Checkpoint, error) {
	switch s.reads.Add(1) {
	case 1:
		close(s.entered)
		<-s.release
	case 2:
		return runtimecontext.Checkpoint{}, errPR72TransientContextRead
	}
	return s.Store.GetActiveContextCheckpoint(ctx, scope)
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
	release := sync.OnceFunc(func() { close(store.release) })
	defer release()
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: rt, Agent: direct, Store: store, Clock: fixedClock{},
		Context: runtimecontext.NewManager(runtimecontext.Dependencies{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	request.Context = contextSeed("frozen instructions",
		contextEntry("pr72-current", "pr72-turn", model.RoleUser, "answer directly", true))
	type outcome struct {
		snapshot harness.Snapshot
		err      error
	}
	first := make(chan outcome, 1)
	go func() { snapshot, e := runner.Start(context.Background(), request); first <- outcome{snapshot, e} }()
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("original Start never reached Context preflight")
	}
	_, replayErr := runner.Start(t.Context(), request)
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

func TestPR72WorkflowStartContextReplayReadFailureCannotTerminalizeOriginal(t *testing.T) {
	rt := newFeatureInvocationRuntime(t)
	backing := harness.NewMemoryStore()
	store := &pr72ReplayContextReadStore{Store: backing, entered: make(chan struct{}), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(store.release) })
	defer release()
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: rt, Agent: unusedFeatureAgent{}, Store: store, Clock: featureInvocationClock{},
		Context:   runtimecontext.NewManager(runtimecontext.Dependencies{}),
		Workflows: completedWorkflowFeature{runtime: rt},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := blockedWorkflowTurnRequest("pr72-context-replay")
	request.Context = contextSeed("frozen instructions",
		contextEntry("pr72-workflow-current", "pr72-workflow-turn", model.RoleUser, request.Goal, true))
	type outcome struct {
		snapshot harness.Snapshot
		err      error
	}
	first := make(chan outcome, 1)
	go func() {
		snapshot, e := runner.StartWorkflowTurn(context.Background(), request)
		first <- outcome{snapshot, e}
	}()
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("original Workflow Start never reached Context preflight")
	}
	_, replayErr := runner.StartWorkflowTurn(t.Context(), request)
	if !errors.Is(replayErr, errPR72TransientContextRead) {
		t.Errorf("replay did not surface read failure: %v", replayErr)
	}
	turnID := workflowTurnID(t, request)
	before, err := backing.GetTurn(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if before.Status != harness.TurnAccepted {
		t.Errorf("Workflow replay terminalized in-flight owner: %s", before.Status)
	}
	select {
	case original := <-first:
		if original.err != nil || original.snapshot.Turn.Status != harness.TurnCompleted {
			t.Errorf("original Workflow Start failed: status=%s err=%v", original.snapshot.Turn.Status, original.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original Workflow Start hung")
	}
}

// Even the original Start may hit a transient read error after Turn creation.
// Its absence of execution authority is not permission to make the shared Turn
// terminal: a subsequent identical request can safely complete the admission.
type pr72FirstContextReadFailsStore struct {
	harness.Store
	once atomic.Bool
}

func (s *pr72FirstContextReadFailsStore) GetActiveContextCheckpoint(ctx context.Context, scope string) (runtimecontext.Checkpoint, error) {
	if s.once.CompareAndSwap(false, true) {
		return runtimecontext.Checkpoint{}, errPR72TransientContextRead
	}
	return s.Store.GetActiveContextCheckpoint(ctx, scope)
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
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: rt, Agent: direct, Store: &pr72FirstContextReadFailsStore{Store: backing}, Clock: fixedClock{},
		Context: runtimecontext.NewManager(runtimecontext.Dependencies{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	request.Context = contextSeed("instructions",
		contextEntry("pr72-newcurrent", "pr72-newturn", model.RoleUser, "answer directly", true))
	_, err = runner.Start(t.Context(), request)
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
	recovered, err := runner.Start(t.Context(), request)
	if err != nil || recovered.Turn.Status != harness.TurnCompleted {
		t.Fatalf("idempotent new Start recovery failed: status=%s err=%v", recovered.Turn.Status, err)
	}
}

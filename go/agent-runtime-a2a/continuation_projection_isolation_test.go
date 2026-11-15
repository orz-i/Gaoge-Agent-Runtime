package a2a

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// A2A GetTask may block on network I/O while the reconciliation pager checks
// a remote SubAgent child. That remote poll is not authority to stall the
// separate durable outbox or an unrelated local Agent continuation.
type heldA2ARemote struct {
	*shadowRemote
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (remote *heldA2ARemote) GetTask(ctx context.Context, discovery Discovery, taskID string) (TaskSnapshot, error) {
	remote.once.Do(func() { close(remote.started) })
	select {
	case <-remote.release:
		return remote.shadowRemote.GetTask(ctx, discovery, taskID)
	case <-ctx.Done():
		return TaskSnapshot{}, ctx.Err()
	}
}

type a2aContinuationHandler struct{ delivered chan continuation.Payload }

func (handler a2aContinuationHandler) Dispatch(ctx context.Context, payload continuation.Payload) error {
	select {
	case handler.delivered <- payload:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestBlockedA2AShadowGetTaskCannotStarveUnrelatedContinuationProjection(t *testing.T) {
	store := memory.NewStore()
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	remote := &heldA2ARemote{shadowRemote: &shadowRemote{task: workingTask()}, started: make(chan struct{}), release: make(chan struct{})}
	shadow, err := NewShadowRunner(ShadowDependencies{Runtime: runtime, Client: remote,
		Discovery: Discovery{Descriptor: RemoteAgentDescriptor{Name: "remote-agent", PreferredURL: testShadowRemoteURL, ProtocolVersion: ProtocolVersion}},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := newShadowParent(t, runtime)
	child, err := shadow.StartRun(t.Context(), shadowStartRequest("remote-subagent"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = relations.Ensure(t.Context(), runrelation.Draft{
		ParentRunID: parent.Run.ID, ChildRunID: child.Run.ID, Kind: runrelation.KindDelegation, OwnerNodeID: "remote-subagent-owner",
	}); err != nil {
		t.Fatal(err)
	}

	queue := queuecore.NewMemory(queuecore.Dependencies{})
	scheduler, err := continuation.NewScheduler(continuation.SchedulerDependencies{
		Outbox: store, Queue: queue, Relations: relations,
		Runs: continuation.LoaderFunc(func(ctx context.Context, runID string) (kernel.Snapshot, error) {
			if runID == child.Run.ID {
				return shadow.LoadRun(ctx, runID)
			}
			return runtime.Load(ctx, runID)
		}),
		ProjectorID: "blocked-a2a-projection",
	})
	if err != nil {
		t.Fatal(err)
	}
	delivered := make(chan continuation.Payload, 4)
	worker, err := continuation.NewWorker(queue, a2aContinuationHandler{delivered: delivered}, continuation.WorkerOptions{
		Projector: scheduler, Reconciler: scheduler, PollInterval: 5 * time.Millisecond,
		ReconcileInterval: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		close(remote.release)
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer closeCancel()
		if err := worker.Close(closeCtx); err != nil {
			t.Errorf("close worker: %v", err)
		}
	})
	if err = worker.Start(workerCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-remote.started: // The reconciliation pager has begun real Shadow.LoadRun -> GetTask.
	case <-time.After(2 * time.Second):
		t.Fatal("remote A2A reconciliation did not enter GetTask")
	}
	unrelated, err := runtime.Create(t.Context(), kernel.CreateRequest{
		ID: "local-independent-agent", Kind: agent.RunKind,
		Actor:  kernel.ActorRef{TenantID: testShadowTenantID, ActorID: testShadowActorID},
		Thread: kernel.ThreadRef{Kind: testShadowThreadKind, ID: testShadowThreadID},
		Goal:   "unrelated local Agent continuation", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	committed, err := runtime.Apply(t.Context(), unrelated.Run.ID, unrelated.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: json.RawMessage(`{}`),
		Events: []kernel.EventDraft{{Type: "agent.unrelated_ready", Wakeup: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case item := <-delivered:
			if item.RunID == unrelated.Run.ID {
				if item.ExpectedRevision != committed.Run.Revision || item.Trigger != continuation.TriggerRunReady ||
					item.SourceRunID != unrelated.Run.ID || item.SourceRevision != committed.Run.Revision {
					t.Fatalf("unrelated continuation lost committed authority: %+v", item)
				}
				// A2A GetTask remains blocked at the point this unrelated
				// outbox Job was scheduled and actually dispatched.
				select {
				case <-remote.release:
					t.Fatal("A2A remote was not blocked during projection")
				default:
				}
				return
			}
		case <-deadline.C:
			t.Fatal("unrelated Agent committed transition stalled behind remote A2A GetTask")
		}
	}
}

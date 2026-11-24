package a2a

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

type independentA2ACreateAckFence struct {
	kernel.Store
	created chan kernel.Snapshot
	release chan struct{}
}

func (s *independentA2ACreateAckFence) Create(ctx context.Context, r kernel.Record, events []kernel.EventDraft) (kernel.Snapshot, error) {
	snap, err := s.Store.Create(ctx, r, events)
	if err == nil && snap.Run.Kind == RunKind {
		s.created <- snap
		<-s.release
	}
	return snap, err
}
func TestPR151A2AFirstSendAfterCallerCancellation(t *testing.T) {
	store := &independentA2ACreateAckFence{Store: memory.NewStore(), created: make(chan kernel.Snapshot, 1), release: make(chan struct{})}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	remote := &shadowRemote{task: workingTask()}
	runner := newShadowRunner(t, rt, remote)
	ctx, cancel := context.WithCancel(t.Context())
	type outcome struct {
		snap kernel.Snapshot
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		snap, e := runner.StartRun(ctx, shadowStartRequest("a2a-create-ack-after-ctx-cancel"))
		done <- outcome{snap, e}
	}()
	<-store.created
	cancel()
	close(store.release)
	result := <-done
	actual, err := rt.Load(t.Context(), "a2a-create-ack-after-ctx-cancel")
	if err != nil {
		t.Fatal(err)
	}
	state, err := runner.decodeState(actual)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("caller cancellation before Create acknowledgement: physical A2A SendMessage=%q, status=%s, state TaskID=%q, StartErr=%v", remote.sent.MessageID, actual.Run.Status, state.RemoteTaskID, result.err)
	if remote.sent.MessageID != "" {
		t.Fatal("A2A first remote SendMessage dispatched after original caller authorization expired")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("fixture context not cancelled")
	}
}

// A Store response can be delayed after the first-dispatch claim CAS,
// not just after the initial Create. Caller revocation must also fence this.
type independentA2AClaimAckFence struct {
	kernel.Store
	committed chan kernel.Snapshot
	release   chan struct{}
}

func (s *independentA2AClaimAckFence) Apply(ctx context.Context, id string, revision uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	snap, err := s.Store.Apply(ctx, id, revision, m)
	if err == nil && revision == 1 && len(m.Events) != 0 &&
		m.Events[0].Type == "a2a.remote_first_dispatch_admitted" {
		s.committed <- snap
		<-s.release
	}
	return snap, err
}

func TestPR151A2AFirstDispatchClaimAckAfterCallerCancellation(t *testing.T) {
	store := &independentA2AClaimAckFence{Store: memory.NewStore(), committed: make(chan kernel.Snapshot, 1), release: make(chan struct{})}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	remote := &shadowRemote{task: workingTask()}
	runner := newShadowRunner(t, rt, remote)
	ctx, cancel := context.WithCancel(t.Context())
	type outcome struct {
		snap kernel.Snapshot
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		snap, startErr := runner.StartRun(ctx, shadowStartRequest("a2a-first-dispatch-ack-after-cancel"))
		done <- outcome{snap, startErr}
	}()
	<-store.committed
	cancel()
	close(store.release)
	result := <-done
	if remote.sent.MessageID != "" || !errors.Is(result.err, context.Canceled) {
		t.Fatalf("caller cancellation did not fence delayed A2A dispatch CAS acknowledgement: send=%q err=%v",
			remote.sent.MessageID, result.err)
	}
}

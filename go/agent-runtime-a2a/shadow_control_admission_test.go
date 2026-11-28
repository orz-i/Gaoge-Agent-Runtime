package a2a

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

// Pause only AFTER the real Store atomically committed the A2A send intent.
// This reproduces a process descheduled between admission and external IO,
// rather than injecting a fake Kernel snapshot or a local-only mutex.
type pausedA2AAdmissionStore struct {
	kernel.Store
	pauseEvent string
	admitted   chan struct{}
	release    chan struct{}
	once       sync.Once
}

func (s *pausedA2AAdmissionStore) Commit(commitCtx context.Context, commitID string, commitRev uint64, commitMutation kernel.StoreMutation, commitAuth kernel.CommitAuthority) (kernel.Snapshot, bool, error) {
	if commitAuth.Lease == nil && commitAuth.Control == nil && commitAuth.Recovery == nil {
		committed, _, err := s.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
		if err == nil {
			for _, event := range commitMutation.Events {
				if event.Type == s.pauseEvent {
					s.once.Do(func() { close(s.admitted) })
					select {
					case <-s.release:
					case <-commitCtx.Done():
						return committed, false, commitCtx.Err()
					}
				}
			}
		}
		return committed, false, err
	}
	return s.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
}

func a2aControlFixture(t *testing.T, store kernel.Store, client RemoteClient, resolverBlock *atomic.Bool, resolverGate, resolverRelease chan struct{}) (
	*kernel.Runtime, *Plugin, *ShadowRunner, kernel.Snapshot,
) {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	binding := BindingResolverFunc(func(_ context.Context, target, revision string) (Binding, error) {
		if resolverBlock != nil && resolverBlock.CompareAndSwap(true, false) {
			close(resolverGate)
			<-resolverRelease
		}
		return Binding{TargetID: target, Revision: "revision-1", Client: client,
			Discovery: Discovery{Descriptor: RemoteAgentDescriptor{
				Name: "remote-agent", PreferredURL: testShadowRemoteURL, ProtocolVersion: ProtocolVersion,
			}}}, nil
	})
	plugin, err := NewPlugin(PluginDependencies{Runtime: runtime, AttestationKey: testA2AShadowKey(), Bindings: binding})
	if err != nil {
		t.Fatal(err)
	}
	delegation := handoff.Delegation{Remote: true, MemberID: "a2a:remote", MemberRevision: "revision-1", ChildRunID: "control-admission-child"}
	childInterface, err := plugin.ResolveChild(t.Context(), delegation)
	if err != nil {
		t.Fatal(err)
	}
	child := childInterface.(*ShadowRunner)
	original, err := child.StartRun(t.Context(), agent.StartRequest{
		ID: delegation.ChildRunID, Goal: "remote goal", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
	})
	if err != nil || original.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("original remote wait not persisted: %+v err=%v", original.Run, err)
	}
	return runtime, plugin, child, original
}

func waitA2AAdmitted(t *testing.T, admitted <-chan struct{}) {
	t.Helper()
	select {
	case <-admitted:
	case <-time.After(4 * time.Second):
		t.Fatal("physical control did not commit its first Store CAS")
	}
}

func testA2ACloseOnce(channel chan struct{}) func() {
	var once sync.Once
	return func() { once.Do(func() { close(channel) }) }
}

// Reproduces P1: Cancel ACK must not race a Resume send whose first Store
// CAS committed but which has not actually reached the remote network.
// Another Runtime shares the Store and the private signing key to simulate
// a separate, independently reconstructed Host process.
func TestPR113ControlResumeIntentFencesCancelACKAcrossRuntime(t *testing.T) {
	backing := &pausedA2AAdmissionStore{Store: memory.NewStore(), pauseEvent: "a2a.remote_resume_requested",
		admitted: make(chan struct{}), release: make(chan struct{})}
	release := testA2ACloseOnce(backing.release)
	defer release()
	remote := newUncertainResumeClient()
	rt, _, child, original := a2aControlFixture(t, backing, remote, nil, nil, nil)
	outcome := make(chan error, 1)
	go func() { _, err := child.ResumeRun(t.Context(), original.Run.ID, "approved"); outcome <- err }()
	waitA2AAdmitted(t, backing.admitted)
	other, err := kernel.New(kernel.Dependencies{Store: backing})
	if err != nil {
		t.Fatal(err)
	}
	recoveredPlugin, err := NewPlugin(PluginDependencies{Runtime: other, AttestationKey: testA2AShadowKey(),
		Bindings: BindingResolverFunc(func(_ context.Context, target, rev string) (Binding, error) {
			return Binding{TargetID: target, Revision: rev, Client: remote,
				Discovery: Discovery{Descriptor: RemoteAgentDescriptor{
					Name: "remote-agent", PreferredURL: testShadowRemoteURL, ProtocolVersion: ProtocolVersion,
				}}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	recoveredChild, err := recoveredPlugin.ResolveChild(t.Context(), handoff.Delegation{Remote: true,
		MemberID: "a2a:remote", MemberRevision: "revision-1", ChildRunID: original.Run.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	observer := recoveredChild.(*ShadowRunner)
	pending, err := observer.LoadRun(t.Context(), original.Run.ID)
	if !errors.Is(err, ErrRemoteOutcomeUnknown) || pending.Run.Status != kernel.RunStatusRunning ||
		pending.Run.Revision != original.Run.Revision+1 {
		t.Fatalf("in-flight resume was projected terminal: %+v %v", pending.Run, err)
	}
	_, err = observer.CancelRun(t.Context(), original.Run.ID)
	if !errors.Is(err, ErrRemoteOutcomeUnknown) {
		t.Fatalf("Cancel acknowledged a remote task while Resume could still send: %v", err)
	}
	if sends, resumes, gets, cancels := remote.counts(); sends != 1 || resumes != 0 || gets != 0 || cancels != 0 {
		t.Fatalf("pending control performed remote IO: %d/%d/%d/%d", sends, resumes, gets, cancels)
	}
	release()
	select {
	case err := <-outcome:
		if err != nil {
			t.Fatalf("original resumed Send could not finish: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("original resumed Send hung")
	}
	// Only once the original callback returned may a subsequent cancellation
	// acquire a new independent, revision-scoped physical Control grant.
	cancelled, err := observer.CancelRun(t.Context(), original.Run.ID)
	if err != nil || cancelled.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("post-return cancellation not admitted: %+v %v", cancelled.Run, err)
	}
	if sends, resumes, _, cancels := remote.counts(); sends != 2 || resumes != 1 || cancels != 1 {
		t.Fatalf("physical calls after ACK crossed order: %d/%d/%d", sends, resumes, cancels)
	}
	_ = rt
}

// Reproduces the opposite ordering: the cancel intent may commit before its
// CancelTask reaches the network. Neither a resumed input nor GET can steal
// the right to declare terminal until that original callback has returned.
func TestPR113ControlCancelIntentFencesResumeAndObserver(t *testing.T) {
	backing := &pausedA2AAdmissionStore{Store: memory.NewStore(), pauseEvent: "a2a.remote_cancel_requested",
		admitted: make(chan struct{}), release: make(chan struct{})}
	release := testA2ACloseOnce(backing.release)
	defer release()
	remote := newUncertainResumeClient()
	rt, plugin, child, original := a2aControlFixture(t, backing, remote, nil, nil, nil)
	outcome := make(chan error, 1)
	go func() {
		_, err := plugin.Cancel(t.Context(), original.Run.ID, original.Run.Revision, "stop")
		outcome <- err
	}()
	waitA2AAdmitted(t, backing.admitted)
	if _, err := child.ResumeRun(t.Context(), original.Run.ID, "late approval"); !errors.Is(err, ErrRemoteOutcomeUnknown) {
		t.Fatalf("in-flight cancel admitted a new input Send: %v", err)
	}
	if _, err := child.LoadRun(t.Context(), original.Run.ID); !errors.Is(err, ErrRemoteOutcomeUnknown) {
		t.Fatalf("GET projected terminal before original cancel callback returned: %v", err)
	}
	if sends, resumes, gets, cancels := remote.counts(); sends != 1 || resumes != 0 || gets != 1 || cancels != 0 {
		// The single GetTask is the read-only preflight before Cancel admission.
		t.Fatalf("in-flight Cancel issued additional remote calls: %d/%d/%d/%d", sends, resumes, gets, cancels)
	}
	release()
	select {
	case err := <-outcome:
		if err != nil {
			t.Fatalf("original cancellation not acknowledged: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("original cancellation blocked")
	}
	if sends, resumes, _, cancels := remote.counts(); sends != 1 || resumes != 0 || cancels != 1 {
		t.Fatalf("post-ACK remote side effects: %d/%d/%d", sends, resumes, cancels)
	}
	if stored, err := rt.Load(t.Context(), original.Run.ID); err != nil || stored.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("cancelled original Run not durable: %+v %v", stored.Run, err)
	}
}

func TestPR113ControlStaleRevisionsDoNotReachRemote(t *testing.T) {
	for _, control := range []string{"resume", "cancel"} {
		t.Run(control, func(t *testing.T) {
			var block atomic.Bool
			resolverGate, resolverRelease := make(chan struct{}), make(chan struct{})
			release := testA2ACloseOnce(resolverRelease)
			defer release()
			remote := newUncertainResumeClient()
			_, plugin, child, original := a2aControlFixture(t, memory.NewStore(), remote, &block, resolverGate, resolverRelease)
			block.Store(true)
			outcome := make(chan error, 1)
			go func() {
				if control == "resume" {
					_, err := plugin.ResumeRun(t.Context(), original.Run.ID, original.Run.Revision, "OLD prompt input")
					outcome <- err
				} else {
					_, err := plugin.Cancel(t.Context(), original.Run.ID, original.Run.Revision, "OLD cancellation")
					outcome <- err
				}
			}()
			waitA2AAdmitted(t, resolverGate)
			remote.setTask(authRequiredTask())
			newer, err := child.LoadRun(t.Context(), original.Run.ID)
			if err != nil || newer.Run.Revision <= original.Run.Revision || newer.Run.Status != kernel.RunStatusWaitingInput {
				t.Fatalf("next remote input challenge not persisted: %+v %v", newer.Run, err)
			}
			release()
			select {
			case err := <-outcome:
				if !errors.Is(err, kernel.ErrConflict) {
					t.Fatalf("stale %s authorized newer prompt at revisions %d -> %d: %v", control, original.Run.Revision, newer.Run.Revision, err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("stale control blocked")
			}
			if sends, resumes, _, cancels := remote.counts(); sends != 1 || resumes != 0 || cancels != 0 {
				t.Fatalf("stale %s performed remote effect: %d/%d/%d", control, sends, resumes, cancels)
			}
		})
	}
}

func TestPR113ControlConcurrentResumeCancelCASGrantsOnePhysicalEffect(t *testing.T) {
	remote := newUncertainResumeClient()
	_, plugin, _, original := a2aControlFixture(t, memory.NewStore(), remote, nil, nil, nil)
	const contenders = 24
	var wg sync.WaitGroup
	start := make(chan struct{})
	failures := make(chan error, contenders)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			var err error
			if i%2 == 0 {
				_, err = plugin.ResumeRun(t.Context(), original.Run.ID, original.Run.Revision, "approved")
			} else {
				_, err = plugin.Cancel(t.Context(), original.Run.ID, original.Run.Revision, "stop")
			}
			if err != nil && !errors.Is(err, kernel.ErrConflict) && !errors.Is(err, ErrRemoteOutcomeUnknown) &&
				!errors.Is(err, ErrRemoteCancellationPending) {
				failures <- err
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("one of competing controls escaped admission: %v", err)
	}
	if sends, resumes, _, cancels := remote.counts(); sends != 1+resumes || resumes+cancels != 1 {
		t.Fatalf("independent controls both performed physical IO: sends=%d resumed=%d cancelled=%d", sends, resumes, cancels)
	}
}

// An Apply may commit the operation intent but lose its reply to the SDK.
// A recovered process must not interpret that lost ACK as no physical effect.
type a2aControlLostStoreACK struct {
	kernel.Store
	event string
	hits  atomic.Int32
}

var errA2AControlLostStoreACK = errors.New("test: Store write committed but reply lost")

func (s *a2aControlLostStoreACK) Commit(commitCtx context.Context, commitID string, commitRev uint64, commitMutation kernel.StoreMutation, commitAuth kernel.CommitAuthority) (kernel.Snapshot, bool, error) {
	if commitAuth.Lease == nil && commitAuth.Control == nil && commitAuth.Recovery == nil {
		committed, _, err := s.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
		if err != nil {
			return committed, false, err
		}
		for _, event := range commitMutation.Events {
			if event.Type == s.event && s.hits.Add(1) == 1 {
				return committed, false, errA2AControlLostStoreACK
			}
		}
		return committed, false, nil
	}
	return s.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
}

func TestPR113ControlStoreCommittedLostACKPreservesPhysicalFence(t *testing.T) {
	for _, op := range []string{"resume", "cancel"} {
		for _, phase := range []string{"admission", "callback-return"} {
			t.Run(op+"/"+phase, func(t *testing.T) {
				event := "a2a.remote_" + op + "_requested"
				if phase == "callback-return" {
					event = "a2a.remote_" + op + "_returned"
				}
				store := &a2aControlLostStoreACK{Store: memory.NewStore(), event: event}
				remote := newUncertainResumeClient()
				rt, plugin, child, original := a2aControlFixture(t, store, remote, nil, nil, nil)
				var err error
				if op == "resume" {
					_, err = plugin.ResumeRun(t.Context(), original.Run.ID, original.Run.Revision, "approved")
				} else {
					_, err = plugin.Cancel(t.Context(), original.Run.ID, original.Run.Revision, "stop")
				}
				if !errors.Is(err, errA2AControlLostStoreACK) || store.hits.Load() != 1 {
					t.Fatalf("failure was not the injected lost Store ACK: %v", err)
				}
				actual, err := rt.Load(t.Context(), original.Run.ID)
				if err != nil || actual.Run.Status != kernel.RunStatusRunning {
					t.Fatalf("Store lost authoritative unresolved intent: %+v %v", actual.Run, err)
				}
				state, err := child.decodeState(actual)
				if err != nil || (op == "resume" && state.PendingMessageID == "") ||
					(op == "cancel" && !state.PendingCancel) {
					t.Fatalf("lost ACK did not preserve first-party pending control proof: %+v %v", state, err)
				}
				if op == "resume" {
					if _, err := child.ResumeRun(t.Context(), original.Run.ID, "approved"); !errors.Is(err, ErrRemoteOutcomeUnknown) {
						t.Fatalf("lost resume ACK reopened SendMessage: %v", err)
					}
					if _, err := child.CancelRun(t.Context(), original.Run.ID); phase == "admission" && !errors.Is(err, ErrRemoteOutcomeUnknown) {
						t.Fatalf("lost resume admission allowed cancel ACK: %v", err)
					}
				} else {
					if _, err := child.CancelRun(t.Context(), original.Run.ID); !errors.Is(err, ErrRemoteOutcomeUnknown) {
						t.Fatalf("lost Cancel ACK reopened CancelTask: %v", err)
					}
				}
				_, resumes, _, cancels := remote.counts()
				wantEffects := 0
				if phase == "callback-return" {
					wantEffects = 1
				}
				if op == "resume" && resumes != wantEffects || op == "cancel" && cancels != wantEffects {
					t.Fatalf("original effect count escaped lost-ACK fence: resumed=%d cancelled=%d expected=%d", resumes, cancels, wantEffects)
				}
			})
		}
	}
}

type a2aSecretGetTaskClient struct{ *uncertainResumeClient }

func (*a2aSecretGetTaskClient) GetTask(context.Context, Discovery, string) (TaskSnapshot, error) {
	return TaskSnapshot{}, errors.New("HTTP request Bearer private provider credential")
}
func TestPR113ControlReadFailureDoesNotLeakProviderErrorOrCancel(t *testing.T) {
	rt := newShadowRuntime(t)
	remote := &a2aSecretGetTaskClient{uncertainResumeClient: newUncertainResumeClient()}
	runner := newIdentityTestShadow(t, rt, remote)
	original, err := runner.StartRun(t.Context(), shadowStartRequest("pr113-get-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.LoadRun(t.Context(), original.Run.ID); !errors.Is(err, ErrRemoteOutcomeUnknown) ||
		strings.Contains(err.Error(), "private provider credential") {
		t.Fatalf("read-only GET returned sensitive provider error: %v", err)
	}
	if _, err := runner.CancelRun(t.Context(), original.Run.ID); !errors.Is(err, ErrRemoteOutcomeUnknown) ||
		strings.Contains(err.Error(), "private provider credential") {
		t.Fatalf("Cancel proceeded despite unverified GET: %v", err)
	}
	if sends, resumes, _, cancels := remote.counts(); sends != 1 || resumes != 0 || cancels != 0 {
		t.Fatalf("unverified GET dispatched remote physical work: %d/%d/%d", sends, resumes, cancels)
	}
}

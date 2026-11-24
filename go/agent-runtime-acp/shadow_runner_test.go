package acp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

type fakeClient struct {
	mu                      sync.Mutex
	opens, prompts, cancels int
	abandons                int
	openErr, promptErr      error
	stop                    string
	openStarted             chan struct{}
	openRelease             chan struct{}
	started                 chan struct{}
	release                 chan struct{}
	cancelStarted           chan struct{}
	cancelRelease           chan struct{}
}

func (f *fakeClient) OpenSession(context.Context, string, string) (string, error) {
	f.mu.Lock()
	f.opens++
	err := f.openErr
	started, release := f.openStarted, f.openRelease
	f.mu.Unlock()
	if started != nil {
		close(started)
	}
	if release != nil {
		<-release
	}
	if err != nil {
		return "", err
	}
	return "session-1", nil
}
func (f *fakeClient) AbandonRun(string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.abandons++
}
func (f *fakeClient) Prompt(_ context.Context, runID, sessionID, goal string, onChunk func(string) error) (string, error) {
	f.mu.Lock()
	f.prompts++
	err := f.promptErr
	stop := f.stop
	started, release := f.started, f.release
	f.mu.Unlock()
	if started != nil {
		close(started)
	}
	if release != nil {
		<-release
	}
	if err != nil {
		return "", err
	}
	if e := onChunk("first "); e != nil {
		return "", e
	}
	if e := onChunk("reply"); e != nil {
		return "", e
	}
	if stop == "" {
		stop = "end_turn"
	}
	return stop, nil
}
func (f *fakeClient) CancelSession(context.Context, string, string) error {
	f.mu.Lock()
	f.cancels++
	started, release := f.cancelStarted, f.cancelRelease
	f.mu.Unlock()
	if started != nil {
		close(started)
	}
	if release != nil {
		<-release
	}
	return nil
}
func (f *fakeClient) counts() (int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens, f.prompts, f.cancels
}
func fixture(t *testing.T, f *fakeClient) (*kernel.Runtime, *ShadowRunner) {
	t.Helper()
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewShadowRunner(ShadowDependencies{Runtime: rt, AttestationKey: []byte(strings.Repeat("K", 32)), Binding: Binding{
		TargetID: "remote", Revision: "r1", Cwd: "/workspace", Client: f,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return rt, runner
}
func startRequest(id string) agent.StartRequest {
	return agent.StartRequest{ID: id, Goal: "remote goal", RequestID: "request-" + id,
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}}
}
func TestACPHandoffNativeProofAndRemoteSession(t *testing.T) {
	rt, shadow := fixture(t, &fakeClient{})
	parent, err := rt.Create(t.Context(), kernel.CreateRequest{ID: "parent", Kind: "test.parent",
		Actor: startRequest("child").Actor, Thread: startRequest("child").Thread,
		Goal: "supervise", State: json.RawMessage(`{"state":"parent"}`)})
	if err != nil {
		t.Fatal(err)
	}
	c, err := handoff.New(shadow)
	if err != nil {
		t.Fatal(err)
	}
	d := handoff.Delegation{Remote: true, ID: "delegate", MemberID: "acp:remote", MemberRevision: "r1",
		ChildRunID: "child", Goal: "remote goal", Status: handoff.StatusQueued}
	got, err := c.StartOrLoad(t.Context(), parent, d)
	if err != nil || got.Status != handoff.StatusCompleted {
		t.Fatalf("handoff result %+v %v", got, err)
	}
	var result struct{ Text, StopReason string }
	if json.Unmarshal(got.Result, &result) != nil || result.Text != "first reply" || result.StopReason != "end_turn" {
		t.Fatalf("remote result %s", got.Result)
	}
	snap, err := shadow.LoadRun(t.Context(), "child")
	if err != nil || snap.Run.Kind != RunKind || !shadow.ObservesFrozenRemoteShadow(rt, d, snap) {
		t.Fatalf("signed child unverified %+v %v", snap.Run, err)
	}
	again, err := c.Refresh(t.Context(), parent, got)
	if err != nil || again.Status != handoff.StatusCompleted {
		t.Fatalf("refresh %+v %v", again, err)
	}
	twin, err := NewShadowRunner(ShadowDependencies{Runtime: rt, AttestationKey: []byte(strings.Repeat("X", 32)),
		Binding: shadow.binding})
	if err != nil {
		t.Fatal(err)
	}
	if twin.ObservesFrozenRemoteShadow(rt, d, snap) {
		t.Fatal("wrong-key shadow accepted")
	}
	d.MemberRevision = "other"
	if shadow.ObservesFrozenRemoteShadow(rt, d, snap) {
		t.Fatal("changed version accepted")
	}
}
func TestACPUnsupportedExecutionDeniedBeforeAnySideEffect(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*agent.StartRequest)
	}{
		{"model", func(r *agent.StartRequest) { r.Model = "other" }},
		{"model_options", func(r *agent.StartRequest) { r.ModelOptions = json.RawMessage(`{"temperature":0.3}`) }},
		{"instructions", func(r *agent.StartRequest) { r.Instructions = "override" }},
		{"tools", func(r *agent.StartRequest) { r.ToolKeys = []string{"shell"} }},
		{"initial_tools", func(r *agent.StartRequest) { r.InitialToolKeys = []string{"shell"} }},
		{"hosted_tools", func(r *agent.StartRequest) { r.HostedToolGrants = []agent.HostedToolGrant{{Key: "search"}} }},
		{"limits", func(r *agent.StartRequest) { r.Limits.MaxLLMCalls = 1 }},
		{"output", func(r *agent.StartRequest) { r.MaxModelOutputTokens = 100 }},
		{"required", func(r *agent.StartRequest) { r.RequiredToolKeys = []string{"shell"} }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			f := &fakeClient{}
			rt, runner := fixture(t, f)
			req := startRequest("denied")
			test.mutate(&req)
			_, err := runner.StartRun(t.Context(), req)
			if !errors.Is(err, ErrInvalidRunner) {
				t.Fatalf("accepted capability: %v", err)
			}
			if opens, prompts, _ := f.counts(); opens != 0 || prompts != 0 {
				t.Fatal("external effect before capability admission")
			}
			if _, err = rt.Load(t.Context(), req.ID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("shadow created: %v", err)
			}
		})
	}
}
func TestACPUnknownFirstSessionOrPromptNeverReplayed(t *testing.T) {
	for _, test := range []struct {
		name         string
		f            *fakeClient
		open, prompt int
	}{
		{"open", &fakeClient{openErr: errors.New("credentials secret")}, 1, 0},
		{"prompt", &fakeClient{promptErr: errors.New("credentials secret")}, 1, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			rt, runner := fixture(t, test.f)
			snap, err := runner.StartRun(t.Context(), startRequest("unknown"))
			if !errors.Is(err, ErrOutcomeUnknown) || snap.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("lost effect settled %+v %v", snap.Run, err)
			}
			if _, err = runner.LoadRun(t.Context(), "unknown"); !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("lost effect reconciled: %v", err)
			}
			if _, err = runner.StartRun(t.Context(), startRequest("unknown")); err == nil {
				t.Fatal("duplicate physical admission")
			}
			o, p, _ := test.f.counts()
			if o != test.open || p != test.prompt {
				t.Fatalf("replayed open/prompt: %d %d", o, p)
			}
			raw, _ := json.Marshal(snap)
			if strings.Contains(string(raw), "credentials secret") {
				t.Fatal("credential from transport leaked into Kernel")
			}
			rebuilt, _ := NewShadowRunner(ShadowDependencies{Runtime: rt, Binding: runner.binding, AttestationKey: []byte(strings.Repeat("K", 32))})
			if _, err = rebuilt.LoadRun(t.Context(), "unknown"); !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("restart re-admitted: %v", err)
			}
		})
	}
}
func TestACPCancelCannotAcknowledgeOriginalInFlightPrompt(t *testing.T) {
	fake := &fakeClient{started: make(chan struct{}), release: make(chan struct{}), stop: "cancelled"}
	rt, runner := fixture(t, fake)
	type ret struct {
		snap kernel.Snapshot
		err  error
	}
	done := make(chan ret, 1)
	go func() { s, e := runner.StartRun(context.Background(), startRequest("cancel")); done <- ret{s, e} }()
	<-fake.started
	pending, err := rt.Load(t.Context(), "cancel")
	if err != nil || pending.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("missing admitted prompt %v", err)
	}
	_, err = runner.CancelRun(t.Context(), "cancel", pending.Run.Revision)
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("cancel notification was mistaken for terminal ACK: %v", err)
	}
	if _, err = runner.LoadRun(t.Context(), "cancel"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("prematurely settled: %v", err)
	}
	close(fake.release)
	finished := <-done
	if finished.err != nil || finished.snap.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("original prompt did not settle cancellation %+v %v", finished.snap.Run, finished.err)
	}
	_, _, cancelCount := fake.counts()
	if cancelCount != 1 {
		t.Fatalf("original ACP cancel notification was not sent once: %d", cancelCount)
	}
}
func TestACPChangedSessionStateOrSameIDForgeryDenied(t *testing.T) {
	rt, runner := fixture(t, &fakeClient{openErr: errors.New("no session")})
	snap, err := runner.StartRun(t.Context(), startRequest("proof"))
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	var st remoteState
	if json.Unmarshal(snap.State, &st) != nil {
		t.Fatal("decode")
	}
	st.SessionID = "other-session"
	raw, _ := json.Marshal(st)
	_, err = rt.Apply(t.Context(), snap.Run.ID, snap.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.LoadRun(t.Context(), "proof"); !errors.Is(err, ErrBindingChanged) {
		t.Fatalf("forged terminal accepted %v", err)
	}
}

// sessionApplyFenceStore delays the response for one ALREADY COMMITTED
// session/new receipt. It exposes the exact original CAS gap without a
// scheduler-dependent sleep or a mock Kernel that weakens admission checks.
type sessionApplyFenceStore struct {
	kernel.Store
	committed chan kernel.Snapshot
	release   chan struct{}
}

func (s *sessionApplyFenceStore) Apply(ctx context.Context, id string, revision uint64, mutation kernel.StoreMutation) (kernel.Snapshot, error) {
	snap, err := s.Store.Apply(ctx, id, revision, mutation)
	if err == nil && revision == 2 && strings.Contains(string(mutation.Record.State), `"sessionID":"session-1"`) {
		s.committed <- snap
		<-s.release
	}
	return snap, err
}

func TestACPCancelBeatsPromptAdmissionWithoutLatePhysicalSend(t *testing.T) {
	fake := &fakeClient{}
	store := &sessionApplyFenceStore{Store: memory.NewStore(), committed: make(chan kernel.Snapshot, 1), release: make(chan struct{})}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewShadowRunner(ShadowDependencies{Runtime: rt, AttestationKey: []byte(strings.Repeat("K", 32)), Binding: Binding{
		TargetID: "remote", Revision: "r1", Cwd: "/workspace", Client: fake,
	}})
	if err != nil {
		t.Fatal(err)
	}
	type ret struct {
		snap kernel.Snapshot
		err  error
	}
	done := make(chan ret, 1)
	go func() { s, e := runner.StartRun(context.Background(), startRequest("early-cancel")); done <- ret{s, e} }()
	committed := <-store.committed
	if committed.Run.Revision != 3 {
		t.Fatalf("wrong committed session revision %d", committed.Run.Revision)
	}
	cancelled, cancelErr := runner.CancelRun(t.Context(), committed.Run.ID, committed.Run.Revision)
	if !errors.Is(cancelErr, ErrOutcomeUnknown) || cancelled.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("cancel invented a terminal receipt: %+v %v", cancelled.Run, cancelErr)
	}
	close(store.release)
	started := <-done
	if !errors.Is(started.err, kernel.ErrConflict) && !errors.Is(started.err, ErrOutcomeUnknown) {
		t.Fatalf("stale Prompt CAS still admitted: %+v %v", started.snap.Run, started.err)
	}
	opens, prompts, cancels := fake.counts()
	if opens != 1 || prompts != 0 || cancels != 1 {
		t.Fatalf("external call escaped the original CAS: open=%d prompt=%d cancel=%d", opens, prompts, cancels)
	}
	if _, err = runner.LoadRun(t.Context(), "early-cancel"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("unknown cancellation projected as terminal: %v", err)
	}
}

func TestACPCancelAfterOriginalPromptCallbackReturnedDoesNotRetryPrompt(t *testing.T) {
	fake := &fakeClient{promptErr: errors.New("lost prompt reply")}
	_, runner := fixture(t, fake)
	original, err := runner.StartRun(t.Context(), startRequest("late-cancel"))
	if !errors.Is(err, ErrOutcomeUnknown) || original.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("lost prompt was settled %+v %v", original.Run, err)
	}
	changed, err := runner.CancelRun(t.Context(), original.Run.ID, original.Run.Revision)
	if !errors.Is(err, ErrOutcomeUnknown) || changed.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("cancel notification claimed terminal: %+v %v", changed.Run, err)
	}
	if _, err = runner.CancelRun(t.Context(), changed.Run.ID, changed.Run.Revision); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("duplicate Cancel was accepted: %v", err)
	}
	opens, prompts, cancels := fake.counts()
	if opens != 1 || prompts != 1 || cancels != 1 {
		t.Fatalf("duplicate remote effect: open=%d prompt=%d cancel=%d", opens, prompts, cancels)
	}
}

// The Prompt response is not a terminal receipt while the separately
// admitted cancel notification may still dispatch/return. The original Prompt
// result is sealed as running evidence; only after BOTH original callbacks
// return may the terminal status be projected.
func TestACPPromptReturnsBeforeCancelCallbackCannotPrematurelySettle(t *testing.T) {
	fake := &fakeClient{
		started: make(chan struct{}), release: make(chan struct{}),
		cancelStarted: make(chan struct{}), cancelRelease: make(chan struct{}),
	}
	rt, runner := fixture(t, fake)
	type ret struct {
		snap kernel.Snapshot
		err  error
	}
	promptDone := make(chan ret, 1)
	go func() {
		s, e := runner.StartRun(context.Background(), startRequest("dual-effects"))
		promptDone <- ret{s, e}
	}()
	<-fake.started
	snap, err := rt.Load(t.Context(), "dual-effects")
	if err != nil {
		t.Fatal(err)
	}
	cancelDone := make(chan ret, 1)
	go func() {
		s, e := runner.CancelRun(context.Background(), snap.Run.ID, snap.Run.Revision)
		cancelDone <- ret{s, e}
	}()
	<-fake.cancelStarted
	close(fake.release)
	prompt := <-promptDone
	if !errors.Is(prompt.err, ErrOutcomeUnknown) || prompt.snap.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("Prompt settled while Cancel callback was live: %+v %v", prompt.snap.Run, prompt.err)
	}
	state, err := runner.decodeState(prompt.snap)
	if err != nil || !state.PromptReturned || state.CancelReturned || state.PromptOutput != "first reply" {
		t.Fatalf("original Prompt result was not sealed as pending evidence: %+v %v", state, err)
	}
	if observed, loadErr := runner.LoadRun(t.Context(), "dual-effects"); !errors.Is(loadErr, ErrOutcomeUnknown) ||
		observed.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("prematurely settled while Cancel was live: %+v %v", observed.Run, loadErr)
	}
	close(fake.cancelRelease)
	cancelResult := <-cancelDone
	if cancelResult.err != nil || cancelResult.snap.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("returned Prompt receipt was not finalized after both returns: %+v %v", cancelResult.snap.Run, cancelResult.err)
	}
	var result struct{ Text, StopReason string }
	if json.Unmarshal(cancelResult.snap.Result.Content, &result) != nil || result.Text != "first reply" || result.StopReason != "end_turn" {
		t.Fatalf("lost original output: %s", cancelResult.snap.Result.Content)
	}
	if _, err = runner.LoadRun(t.Context(), "dual-effects"); err != nil {
		t.Fatalf("verified terminal lost on reload: %v", err)
	}
	opens, prompts, cancels := fake.counts()
	if opens != 1 || prompts != 1 || cancels != 1 {
		t.Fatalf("duplicated external effects: %d %d %d", opens, prompts, cancels)
	}
}

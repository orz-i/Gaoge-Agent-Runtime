package acp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

type auditRejectCancelledStore struct{ kernel.Store }

func (s auditRejectCancelledStore) Load(ctx context.Context, id string) (kernel.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return kernel.Snapshot{}, err
	}
	return s.Store.Load(ctx, id)
}
func (s auditRejectCancelledStore) Apply(ctx context.Context, id string, rev uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return kernel.Snapshot{}, err
	}
	return s.Store.Apply(ctx, id, rev, m)
}

type auditCancelCtxACPClient struct {
	*fakeClient
	cancel context.CancelFunc
}

func (c *auditCancelCtxACPClient) CancelSession(ctx context.Context, id, session string) error {
	c.cancel()
	return ctx.Err()
}
func TestPR151ACPCancelCtxRetainsPhysicalReturn(t *testing.T) {
	fake := &fakeClient{started: make(chan struct{}), release: make(chan struct{})}
	store := auditRejectCancelledStore{Store: memory.NewStore()}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	client := &auditCancelCtxACPClient{fakeClient: fake, cancel: cancel}
	runner, err := NewShadowRunner(ShadowDependencies{Runtime: rt, AttestationKey: []byte(strings.Repeat("K", 32)), Binding: Binding{TargetID: "remote", Revision: "r1", Cwd: "/workspace", Client: client}})
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		snap kernel.Snapshot
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		snap, e := runner.StartRun(context.Background(), startRequest("ctx-receipt"))
		done <- outcome{snap, e}
	}()
	<-fake.started
	before, err := rt.Load(t.Context(), "ctx-receipt")
	if err != nil {
		t.Fatal(err)
	}
	_, cancelErr := runner.CancelRun(ctx, before.Run.ID, before.Run.Revision)
	t.Logf("CancelRun returned err=%v, context=%v", cancelErr, ctx.Err())
	close(fake.release)
	after := <-done
	stateSnap, err := rt.Load(t.Context(), "ctx-receipt")
	if err != nil {
		t.Fatal(err)
	}
	state, err := runner.decodeState(stateSnap)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("original physical callbacks returned: prompt=%t cancel=%t, state PromptReturned=%t CancelPending=%t CancelReturned=%t status=%s StartErr=%v", true, true, state.PromptReturned, state.CancelPending, state.CancelReturned, stateSnap.Run.Status, after.err)
	observed, obsErr := runner.LoadRun(t.Context(), stateSnap.Run.ID)
	t.Logf("read-only recovery status=%s err=%v", observed.Run.Status, obsErr)
	if !errors.Is(cancelErr, ErrOutcomeUnknown) || !state.CancelPending || !state.CancelReturned ||
		!state.PromptReturned || stateSnap.Run.Status != kernel.RunStatusCompleted || after.err != nil ||
		obsErr != nil || observed.Run.Status != kernel.RunStatusCompleted {
		t.Fatal("original physical callback return facts were not durably settled despite cancelled caller context")
	}
}

type auditCancelAfterPromptACP struct {
	*fakeClient
	cancel context.CancelFunc
}

func (c *auditCancelAfterPromptACP) Prompt(ctx context.Context, id, session, goal string, emit func(string) error) (string, error) {
	stop, err := c.fakeClient.Prompt(ctx, id, session, goal, emit)
	c.cancel()
	return stop, err
}
func TestPR151ACPPromptReturnedOnCallerCancellation(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: auditRejectCancelledStore{Store: memory.NewStore()}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	client := &auditCancelAfterPromptACP{fakeClient: &fakeClient{}, cancel: cancel}
	runner, err := NewShadowRunner(ShadowDependencies{Runtime: rt, AttestationKey: []byte(strings.Repeat("K", 32)), Binding: Binding{TargetID: "remote", Revision: "r1", Cwd: "/workspace", Client: client}})
	if err != nil {
		t.Fatal(err)
	}
	_, startErr := runner.StartRun(ctx, startRequest("prompt-completed-before-cancel"))
	snap, err := rt.Load(t.Context(), "prompt-completed-before-cancel")
	if err != nil {
		t.Fatal(err)
	}
	state, err := runner.decodeState(snap)
	if err != nil {
		t.Fatal(err)
	}
	observed, obsErr := runner.LoadRun(t.Context(), snap.Run.ID)
	t.Logf("Prompt callback returned stopReason=end_turn; durable status=%s PromptReturned=%t Pending=%t StartErr=%v ReadErr=%v", snap.Run.Status, state.PromptReturned, state.PromptPending, startErr, obsErr)
	if startErr != nil || obsErr != nil || snap.Run.Status != kernel.RunStatusCompleted ||
		!state.PromptReturned || state.PromptUnknown || observed.Run.Status != kernel.RunStatusCompleted {
		t.Fatal("ACP completed physical Prompt lost signed terminal result solely because caller context expired")
	}
}

// A late session/new reply can still freeze its original Session identity,
// but must never grant a new physical Prompt when the caller has cancelled.
type auditCancelAfterOpenACP struct {
	*fakeClient
	cancel context.CancelFunc
}

func (c *auditCancelAfterOpenACP) OpenSession(ctx context.Context, runID, cwd string) (string, error) {
	id, err := c.fakeClient.OpenSession(ctx, runID, cwd)
	c.cancel()
	return id, err
}

func TestPR151ACPOpenSessionReturnProofWithoutLatePrompt(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{
		Store: auditRejectCancelledStore{Store: memory.NewStore()},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	client := &auditCancelAfterOpenACP{fakeClient: &fakeClient{}, cancel: cancel}
	runner, err := NewShadowRunner(ShadowDependencies{
		Runtime: rt, AttestationKey: []byte(strings.Repeat("K", 32)),
		Binding: Binding{TargetID: "remote", Revision: "r1", Cwd: "/workspace", Client: client},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, startErr := runner.StartRun(ctx, startRequest("open-return-before-cancel"))
	if !errors.Is(startErr, ErrOutcomeUnknown) {
		t.Fatalf("cancelled session/new caused success/new Prompt: %v", startErr)
	}
	snap, err := rt.Load(t.Context(), "open-return-before-cancel")
	if err != nil {
		t.Fatal(err)
	}
	state, err := runner.decodeState(snap)
	if err != nil || !state.OpenReturned || state.SessionID != "session-1" ||
		state.PromptPending || state.PromptReturned {
		t.Fatalf("original opened Session proof lost or late Prompt admitted: %+v err=%v", state, err)
	}
	opens, prompts, cancels := client.counts()
	if opens != 1 || prompts != 0 || cancels != 0 || client.abandons != 1 {
		t.Fatalf("caller cancelled before Prompt; physical calls=%d/%d/%d abandoned=%d",
			opens, prompts, cancels, client.abandons)
	}
}

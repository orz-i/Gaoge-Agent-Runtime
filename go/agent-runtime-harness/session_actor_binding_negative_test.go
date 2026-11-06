package harness_test

import (
	"context"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type auditSwitchSessionActorStore struct {
	harness.Store
	reads  int
	other  kernel.ActorRef
	always bool
}

func (s *auditSwitchSessionActorStore) GetSession(ctx context.Context, id string) (harness.Session, error) {
	session, err := s.Store.GetSession(ctx, id)
	if err != nil {
		return session, err
	}
	s.reads++
	if s.reads == 1 || s.always {
		session.Actor = s.other
	}
	return session, nil
}

// A stale or adversarial Store response may not reassign a durable Session's
// owner at the first read and then leak another Actor's committed Turn.
func TestSessionFacadeFrozenOwnerRejectsInconsistentSessionReads(t *testing.T) {
	hostThread := harness.HostRef{Kind: "conversation", ID: "audit-tenant-owned-conversation"}
	foreign := kernel.ActorRef{TenantID: "tenant-b", ActorID: "owner"}
	other := kernel.ActorRef{TenantID: "tenant-a", ActorID: "other"}
	kstore := memory.NewStore()
	hstore := harness.NewMemoryStore()
	runtime, err := kernel.New(kernel.Dependencies{Store: kstore})
	if err != nil {
		t.Fatal(err)
	}
	prep, err := harness.NewContextRunMiddleware(hstore, nil)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: &multiContextCaptureModel{}, RunMiddleware: []plugin.RunMiddleware{prep}, PrepareModelRequest: harness.MaterializeContextWindowRequest})
	if err != nil {
		t.Fatal(err)
	}
	ctxman := runtimecontext.NewManager(runtimecontext.Dependencies{})
	baseRunner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: direct, Store: hstore, Clock: contextHarnessClock{}, Context: ctxman})
	if err != nil {
		t.Fatal(err)
	}
	opts := harness.SessionOptions{HostThread: hostThread, Actor: foreign,
		Thread: kernel.ThreadRef{Kind: "conversation", ID: hostThread.ID}, Config: harness.ConfigSnapshot{Model: "fixture"},
		Transcript: harness.TranscriptSourceFunc(func(_ context.Context, _, _ harness.HostRef, prompt string) (*harness.ContextSeed, error) {
			return contextSeed("", contextEntry("m1", "first", model.RoleUser, prompt, true)), nil
		}),
	}
	sessionB, err := baseRunner.OpenSession(opts)
	if err != nil {
		t.Fatal(err)
	}
	original, err := sessionB.Prompt(t.Context(), "first", "private question")
	if err != nil || original.Output == nil {
		t.Fatalf("fixture failed: %v %+v", err, original)
	}
	// This mimics a defective/malicious Store with a stale/retargeted first
	// owner read but genuine immutable persisted Session on later reads.
	store := &auditSwitchSessionActorStore{Store: hstore, other: other}
	reader, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: direct, Store: store, Clock: contextHarnessClock{}, Context: ctxman})
	if err != nil {
		t.Fatal(err)
	}
	opts.Actor = other
	opts.Transcript = nil
	sessionA, err := reader.OpenSession(opts)
	if err != nil {
		t.Fatal(err)
	}
	exposed, err := sessionA.Load(t.Context(), "first")
	if !errors.Is(err, harness.ErrConflict) || exposed.Turn.ID != "" || exposed.Output != nil {
		t.Fatalf("Load crossed the foreign owner boundary: err=%v snapshot=%+v reads=%d", err, exposed.Turn, store.reads)
	}
	store.reads = 0
	state, err := sessionA.LoadState(t.Context(), "first")
	if !errors.Is(err, harness.ErrConflict) || state.Turn.ID != "" {
		t.Errorf("LoadState crossed foreign owner boundary: err=%v snapshot=%+v reads=%d", err, state.Turn, store.reads)
	}
	store.reads = 0
	items, err := sessionA.Timeline(t.Context(), "first", 0, 50)
	if !errors.Is(err, harness.ErrConflict) || len(items) != 0 {
		t.Errorf("Timeline crossed foreign owner boundary: err=%v items=%d reads=%d", err, len(items), store.reads)
	}
	store.reads = 0
	updated, err := sessionA.Refresh(t.Context(), "first")
	if !errors.Is(err, harness.ErrConflict) || updated.Turn.ID != "" || updated.Output != nil {
		t.Errorf("Refresh crossed foreign owner boundary: err=%v snapshot=%+v reads=%d", err, updated.Turn, store.reads)
	}
	store.reads = 0
	continued, err := sessionA.Resume(t.Context(), "first")
	if !errors.Is(err, harness.ErrConflict) || continued.Turn.ID != "" || continued.Output != nil {
		t.Errorf("Resume crossed foreign owner boundary: err=%v snapshot=%+v reads=%d", err, continued.Turn, store.reads)
	}
	// An always-rebound Store Session should not be able to claim the other
	// Actor's original Run/Invocation through the same Turn ID either.
	store.always = true
	store.reads = 0
	reflected, err := sessionA.Load(t.Context(), "first")
	if !errors.Is(err, harness.ErrConflict) || reflected.Turn.ID != "" || reflected.Output != nil {
		t.Errorf("persistent Store retarget leaked foreign Turn: err=%v owner=%+v", err, reflected.Session.Actor)
	}
	store.reads = 0
	state2, err := sessionA.LoadState(t.Context(), "first")
	if !errors.Is(err, harness.ErrConflict) || state2.Turn.ID != "" {
		t.Errorf("persistent Store retarget leaked foreign state: err=%v state=%+v", err, state2.Turn)
	}
	store.reads = 0
	items2, err := sessionA.Timeline(t.Context(), "first", 0, 50)
	if !errors.Is(err, harness.ErrConflict) || len(items2) != 0 {
		t.Errorf("persistent Store retarget leaked %d Items: err=%v", len(items2), err)
	}
}

// Physical recovery must be controlled by the original frozen Actor even
// when the first Store identity read presents a different Actor.
func TestSessionFacadeFrozenOwnerBlocksForeignPendingModelDispatch(t *testing.T) {
	opts := resumeTurnSessionOptions()
	originalActor := opts.Actor
	other := kernel.ActorRef{TenantID: "intruder-tenant", ActorID: "unrelated-user"}
	base := memory.NewStore()
	faulty := &resumeTurnFaultStore{Store: base}
	hstore := harness.NewMemoryStore()
	clock := newResumeTurnClock()
	firstProvider := &multiContextCaptureModel{}
	initial, _ := newResumeTurnHarness(t, faulty, hstore, firstProvider, clock, nil)
	owner := mustResumeSession(t, initial, opts)
	_, err := owner.Prompt(t.Context(), "turn", "private work")
	if !errors.Is(err, errResumeTurnInterrupted) || len(firstProvider.requests) != 0 {
		t.Fatalf("failed to seed original committed undispatched intent: %v calls=%d", err, len(firstProvider.requests))
	}
	routed := &multiContextCaptureModel{}
	swapped := &auditSwitchSessionActorStore{Store: hstore, other: other}
	second, _ := newResumeTurnHarness(t, base, swapped, routed, clock, nil)
	opts.Actor = other
	opts.Transcript = nil
	foreign := mustResumeSession(t, second, opts)
	result, err := foreign.Resume(t.Context(), "turn")
	if !errors.Is(err, harness.ErrConflict) || result.Turn.ID != "" || len(routed.requests) != 0 {
		t.Fatalf("foreign Actor resumed owner's pending model: original=%+v current=%+v calls=%d result=%+v err=%v", originalActor, other, len(routed.requests), result.Turn, err)
	}
	// A rejected foreign recovery cannot consume or mutate the legitimate
	// pending ModelInvocation. Its owner may still continue the same Run.
	legitimate, _ := newResumeTurnHarness(t, base, hstore, routed, clock, nil)
	opts.Actor = originalActor
	ownerAfter := mustResumeSession(t, legitimate, opts)
	finished, err := ownerAfter.Resume(t.Context(), "turn")
	if err != nil || finished.Turn.Status != harness.TurnCompleted || len(routed.requests) != 1 {
		t.Fatalf("original owner could not resume untouched pending invocation: err=%v turn=%+v calls=%d", err, finished.Turn, len(routed.requests))
	}
}

package harness_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func TestSessionFacadeRestartsAcrossTurnsWithFrozenContextProjection(t *testing.T) {
	kernelStore := memory.NewStore()
	harnessStore := harness.NewMemoryStore()
	var sourceCalls int
	transcript := harness.TranscriptSourceFunc(func(_ context.Context, thread, turn harness.HostRef, prompt string) (*harness.ContextSeed, error) {
		sourceCalls++
		if thread.ID != "session-conversation" || turn.Kind != "conversation_turn" {
			t.Fatal("incorrect host identities")
		}
		switch turn.ID {
		case "first":
			return contextSeed("workspace instruction", contextEntry("m1", "first", model.RoleUser, prompt, true)), nil
		case "second":
			return contextSeed("workspace instruction",
				contextEntry("m1", "first", model.RoleUser, "first prompt", false),
				contextEntry("a1", "first", model.RoleAssistant, "answer", false),
				contextEntry("m2", "second", model.RoleUser, prompt, true)), nil
		default:
			return nil, harness.ErrNotFound
		}
	})
	options := harness.SessionOptions{
		HostThread: harness.HostRef{Kind: "conversation", ID: "session-conversation"},
		Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "operator"},
		Thread:     kernel.ThreadRef{Kind: "conversation", ID: "session-conversation"},
		Config:     harness.ConfigSnapshot{Model: "fixture", Instructions: "answer accurately"},
		Transcript: transcript,
	}
	makeSession := func(provider *multiContextCaptureModel) (*harness.SessionClient, *kernel.Runtime) {
		runtime, err := kernel.New(kernel.Dependencies{Store: kernelStore})
		if err != nil {
			t.Fatal(err)
		}
		rehydrate, err := harness.NewContextRunMiddleware(harnessStore, nil)
		if err != nil {
			t.Fatal(err)
		}
		direct, err := agent.NewRunner(agent.Dependencies{
			Runtime: runtime, Model: provider, RunMiddleware: []plugin.RunMiddleware{rehydrate},
			PrepareModelRequest: harness.MaterializeContextWindowRequest,
			ModelMiddleware:     []plugin.ModelMiddleware{harness.NewContextWindowMiddleware()},
		})
		if err != nil {
			t.Fatal(err)
		}
		runner, err := harness.NewRunner(harness.Dependencies{
			Runtime: runtime, Agent: direct, Store: harnessStore, Clock: contextHarnessClock{},
			Context: runtimecontext.NewManager(runtimecontext.Dependencies{}),
		})
		if err != nil {
			t.Fatal(err)
		}
		session, err := runner.OpenSession(options)
		if err != nil {
			t.Fatal(err)
		}
		return session, runtime
	}
	firstModel := &multiContextCaptureModel{}
	first, firstRuntime := makeSession(firstModel)
	one, err := first.Prompt(t.Context(), "first", "first prompt")
	if err != nil || one.Turn.Status != harness.TurnCompleted || len(firstModel.requests) != 1 {
		t.Fatalf("first prompt: status=%s err=%v requests=%d", one.Turn.Status, err, len(firstModel.requests))
	}
	assertPersistedModelHash(t, firstRuntime, one, firstModel.requests[0])
	if len(firstModel.requests[0].Messages) != 2 || firstModel.requests[0].Messages[0].Content != "answer accurately\n\nworkspace instruction" {
		t.Fatalf("frozen first window: %+v", firstModel.requests[0].Messages)
	}
	secondModel := &multiContextCaptureModel{}
	restarted, secondRuntime := makeSession(secondModel)
	two, err := restarted.Prompt(t.Context(), "second", "second prompt")
	if err != nil || two.Turn.Status != harness.TurnCompleted || len(secondModel.requests) != 1 {
		t.Fatalf("second prompt after restart: status=%s err=%v requests=%d", two.Turn.Status, err, len(secondModel.requests))
	}
	assertPersistedModelHash(t, secondRuntime, two, secondModel.requests[0])
	messages := secondModel.requests[0].Messages
	if len(messages) != 4 || messages[1].Content != "first prompt" || messages[2].Content != "answer" || messages[3].Content != "second prompt" {
		t.Fatalf("continued conversation was not projected: %+v", messages)
	}
	// Context source prefixes advance without a new generation; no second
	// Session state authority or duplicate model delivery appears on replay.
	if one.Turn.ContextRef.Generation != two.Turn.ContextRef.Generation || two.Turn.ContextRef.Revision <= one.Turn.ContextRef.Revision {
		t.Fatalf("context lineage broke: first=%+v second=%+v", one.Turn.ContextRef, two.Turn.ContextRef)
	}
	replay, err := restarted.Prompt(t.Context(), "second", "second prompt")
	if err != nil || replay.Turn.ID != two.Turn.ID || len(secondModel.requests) != 1 || sourceCalls != 3 {
		t.Fatalf("replay called provider or changed identity: turn=%s err=%v requests=%d source=%d", replay.Turn.ID, err, len(secondModel.requests), sourceCalls)
	}
	loaded, err := restarted.Load(t.Context(), "first")
	if err != nil || loaded.Turn.ID != one.Turn.ID {
		t.Fatalf("load across restart: %+v err=%v", loaded.Turn, err)
	}
	refreshed, err := restarted.Refresh(t.Context(), "second")
	if err != nil || refreshed.Turn.ID != two.Turn.ID {
		t.Fatalf("refresh: %+v err=%v", refreshed.Turn, err)
	}
}

func TestSessionFacadeRejectsUntrustedOrStaleContext(t *testing.T) {
	store := harness.NewMemoryStore()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	provider := &multiContextCaptureModel{}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: provider, PrepareModelRequest: harness.MaterializeContextWindowRequest})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: direct, Store: store, Clock: contextHarnessClock{}, Context: runtimecontext.NewManager(runtimecontext.Dependencies{})})
	if err != nil {
		t.Fatal(err)
	}
	options := harness.SessionOptions{
		HostThread: harness.HostRef{Kind: "conversation", ID: "secure"},
		Actor:      kernel.ActorRef{TenantID: "t1", ActorID: "a"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "secure"},
		Transcript: harness.TranscriptSourceFunc(func(_ context.Context, _, _ harness.HostRef, _ string) (*harness.ContextSeed, error) {
			return contextSeed("", contextEntry("m1", "turn", model.RoleAssistant, "wrong role", false)), nil
		}),
	}
	session, err := runner.OpenSession(options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.Prompt(t.Context(), "turn", "real prompt")
	if !errors.Is(err, harness.ErrInvalidRequest) || len(provider.requests) != 0 {
		t.Fatalf("stale/missing current user prompt dispatched: %v", err)
	}
	transcriptReads := 0
	options.Transcript = harness.TranscriptSourceFunc(func(_ context.Context, _, _ harness.HostRef, prompt string) (*harness.ContextSeed, error) {
		transcriptReads++
		return contextSeed("", contextEntry("m1", "turn", model.RoleUser, prompt, true)), nil
	})
	session, err = runner.OpenSession(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.Prompt(t.Context(), "turn", "real prompt"); err != nil {
		t.Fatal(err)
	}
	options.Actor = kernel.ActorRef{TenantID: "t2", ActorID: "another"}
	foreign, err := runner.OpenSession(options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = foreign.Load(t.Context(), "turn")
	if !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("cross-actor load: %v", err)
	}
	_, err = foreign.Refresh(t.Context(), "turn")
	if !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("cross-actor refresh: %v", err)
	}
	_, err = foreign.Prompt(t.Context(), "another-turn", "a prompt")
	if !errors.Is(err, harness.ErrConflict) || transcriptReads != 1 {
		t.Fatalf("cross-actor prompt or unauthorized transcript load: err=%v reads=%d", err, transcriptReads)
	}
	options.Actor = kernel.ActorRef{TenantID: "t1", ActorID: "a"}
	options.Transcript = harness.TranscriptSourceFunc(nil)
	emptySource, err := runner.OpenSession(options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = emptySource.Prompt(t.Context(), "other-turn", "another prompt")
	if !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("nil TranscriptSourceFunc was not rejected: %v", err)
	}
}

func assertPersistedModelHash(t *testing.T, runtime *kernel.Runtime, snapshot harness.Snapshot, sent model.Request) {
	t.Helper()
	invocation, ok := harness.TopLevelInvocation(snapshot)
	if !ok {
		t.Fatal("missing root invocation")
	}
	stored, err := runtime.Load(t.Context(), invocation.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := agent.ViewState(stored)
	if err != nil || len(view.ModelInvocations) != 1 {
		t.Fatalf("durable receipt missing: %+v err=%v", view, err)
	}
	sent.InvocationID = ""
	encoded, err := json.Marshal(sent)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	if view.ModelInvocations[0].RequestHash != hex.EncodeToString(digest[:]) {
		t.Fatalf("model saw a request different from frozen intent: received=%s frozen=%s", hex.EncodeToString(digest[:]), view.ModelInvocations[0].RequestHash)
	}
}

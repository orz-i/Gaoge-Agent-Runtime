package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

var errResumeTurnInterrupted = errors.New("interrupted after committed model intent")

type resumeTurnModelFunc func(context.Context, model.Request) (model.Response, error)

func (fn resumeTurnModelFunc) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	return fn(ctx, request)
}

type resumeTurnClock struct{ now time.Time }

func (clock *resumeTurnClock) Now() time.Time { return clock.now }

func newResumeTurnClock() *resumeTurnClock {
	return &resumeTurnClock{now: time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)}
}

type resumeTurnFaultStore struct {
	kernel.Store
	failed atomic.Bool
}

func (store *resumeTurnFaultStore) Apply(ctx context.Context, id string, revision uint64, mutation kernel.StoreMutation) (kernel.Snapshot, error) {
	for _, event := range mutation.Events {
		if event.Type == "agent.model_invocation.pending" && store.failed.CompareAndSwap(false, true) {
			if _, err := store.Store.Apply(ctx, id, revision, mutation); err != nil {
				return kernel.Snapshot{}, err
			}
			return kernel.Snapshot{}, errResumeTurnInterrupted
		}
	}
	return store.Store.Apply(ctx, id, revision, mutation)
}

func newResumeTurnHarness(t *testing.T, store kernel.Store, sessionStore harness.Store, provider model.Client, clock *resumeTurnClock, configure func(*agent.Dependencies)) (*harness.Runner, *kernel.Runtime) {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: store, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	dependencies := agent.Dependencies{Runtime: runtime, Model: provider, Clock: clock, PrepareModelRequest: harness.MaterializeContextWindowRequest}
	if configure != nil {
		configure(&dependencies)
	}
	direct, err := agent.NewRunner(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately omit ContextRunMiddleware: ResumeTurn must restore its own
	// durable root binding, even after the original execution context is lost.
	host, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: direct, Store: sessionStore, Clock: clock,
		Context: runtimecontext.NewManager(runtimecontext.Dependencies{}), Catalog: dependencies.Catalog,
	})
	if err != nil {
		t.Fatal(err)
	}
	return host, runtime
}

func resumeTurnSessionOptions() harness.SessionOptions {
	return harness.SessionOptions{
		HostThread: harness.HostRef{Kind: "conversation", ID: "resume-session"},
		Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "owner"},
		Thread:     kernel.ThreadRef{Kind: "conversation", ID: "resume-session"},
		Config:     harness.ConfigSnapshot{Model: "fixture", Instructions: "frozen session instructions"},
		Transcript: harness.TranscriptSourceFunc(func(_ context.Context, _, turn harness.HostRef, prompt string) (*harness.ContextSeed, error) {
			return contextSeed("frozen host evidence", contextEntry("source-"+turn.ID, turn.ID, model.RoleUser, prompt, true)), nil
		}),
	}
}

func mustResumeSession(t *testing.T, host *harness.Runner, options harness.SessionOptions) *harness.SessionClient {
	t.Helper()
	session, err := host.OpenSession(options)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestSessionResumeReusesFrozenIntentWithoutTranscriptOrPreparation(t *testing.T) {
	t.Parallel()
	base := memory.NewStore()
	store := harness.NewMemoryStore()
	clock := newResumeTurnClock()
	faulty := &resumeTurnFaultStore{Store: base}
	firstProvider := &multiContextCaptureModel{}
	first, _ := newResumeTurnHarness(t, faulty, store, firstProvider, clock, nil)
	options := resumeTurnSessionOptions()
	session := mustResumeSession(t, first, options)
	_, err := session.Prompt(t.Context(), "turn", "finish the original task")
	if !errors.Is(err, errResumeTurnInterrupted) || len(firstProvider.requests) != 0 {
		t.Fatalf("expected committed intent before dispatch: error=%v calls=%d", err, len(firstProvider.requests))
	}
	pending, err := session.Load(t.Context(), "turn")
	if err != nil || pending.Turn.Status != harness.TurnRunning {
		t.Fatalf("pending Turn unavailable: %+v error=%v", pending.Turn, err)
	}
	root, ok := harness.TopLevelInvocation(pending)
	if !ok {
		t.Fatal("missing root invocation")
	}
	preparationCalls := 0
	provider := &multiContextCaptureModel{}
	restarted, runtime := newResumeTurnHarness(t, base, store, provider, clock, func(dependencies *agent.Dependencies) {
		dependencies.PrepareModelRequest = func(context.Context, model.Request) (model.Request, error) {
			preparationCalls++
			return model.Request{}, errors.New("pending request must not be prepared again")
		}
	})
	options.Transcript = nil
	options.Config.Instructions = "changed settings must not replace the frozen request"
	reopened := mustResumeSession(t, restarted, options)
	done, err := reopened.Resume(t.Context(), "turn")
	if err != nil || done.Turn.Status != harness.TurnCompleted || len(provider.requests) != 1 || preparationCalls != 0 {
		t.Fatalf("resume: status=%s error=%v calls=%d prepare=%d", done.Turn.Status, err, len(provider.requests), preparationCalls)
	}
	completedRoot, ok := harness.TopLevelInvocation(done)
	if !ok || completedRoot.ID != root.ID || completedRoot.ExecutionRefID != root.ExecutionRefID || completedRoot.Attempt != 1 || completedRoot.Status != harness.InvocationCompleted {
		t.Fatalf("root identity or projection changed: before=%+v after=%+v", root, completedRoot)
	}
	if done.Output == nil || !strings.Contains(string(done.Output.Content), "answer") {
		t.Fatalf("missing root terminal output: %+v", done.Output)
	}
	assertPersistedModelHash(t, runtime, done, provider.requests[0])
	again, err := reopened.Resume(t.Context(), "turn")
	if err != nil || again.Turn.Revision != done.Turn.Revision || len(provider.requests) != 1 {
		t.Fatalf("terminal resume was not idempotent: turn=%+v error=%v calls=%d", again.Turn, err, len(provider.requests))
	}
}

func TestSessionResumeRestoresContextAndChecksOwnerWithoutTranscript(t *testing.T) {
	t.Parallel()
	base := memory.NewStore()
	store := harness.NewMemoryStore()
	clock := newResumeTurnClock()
	first, _ := newResumeTurnHarness(t, base, store, resumeTurnModelFunc(func(context.Context, model.Request) (model.Response, error) {
		return model.Response{Content: "checkpoint reached", Continue: true}, nil
	}), clock, nil)
	options := resumeTurnSessionOptions()
	pending, err := mustResumeSession(t, first, options).Prompt(t.Context(), "turn", "preserve the evidence")
	if err != nil || pending.Turn.Status != harness.TurnRunning {
		t.Fatalf("expected running pause: %+v error=%v", pending.Turn, err)
	}
	provider := &multiContextCaptureModel{}
	restarted, _ := newResumeTurnHarness(t, base, store, provider, clock, nil)
	options.Transcript = nil
	for _, actor := range []kernel.ActorRef{{TenantID: "other", ActorID: "owner"}, {TenantID: "tenant", ActorID: "other"}} {
		foreignOptions := options
		foreignOptions.Actor = actor
		_, err = mustResumeSession(t, restarted, foreignOptions).Resume(t.Context(), "turn")
		if !errors.Is(err, harness.ErrConflict) || len(provider.requests) != 0 {
			t.Fatalf("foreign actor resumed Turn: actor=%+v error=%v calls=%d", actor, err, len(provider.requests))
		}
	}
	reopened := mustResumeSession(t, restarted, options)
	if _, err = reopened.Load(t.Context(), "turn"); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Refresh(t.Context(), "turn"); err != nil || len(provider.requests) != 0 {
		t.Fatalf("refresh dispatched: error=%v calls=%d", err, len(provider.requests))
	}
	if _, err = reopened.Prompt(t.Context(), "new-turn", "cannot start without a source"); !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("missing transcript was accepted for Prompt: %v", err)
	}
	done, err := reopened.Resume(t.Context(), "turn")
	if err != nil || done.Turn.Status != harness.TurnCompleted || len(provider.requests) != 1 {
		t.Fatalf("resume after pause: status=%s error=%v calls=%d", done.Turn.Status, err, len(provider.requests))
	}
	messages := provider.requests[0].Messages
	if len(messages) < 2 || !strings.Contains(model.TextValue(messages[0].Content), "frozen session instructions") || !strings.Contains(model.TextValue(messages[0].Content), "frozen host evidence") {
		t.Fatalf("persisted root Context was not restored: %+v", messages)
	}
}

func TestResumeTurnDoesNotInventUnstartedRuntime(t *testing.T) {
	t.Parallel()
	for _, hasRoot := range []bool{false, true} {
		name := "before-root"
		if hasRoot {
			name = "before-runtime"
		}
		t.Run(name, func(t *testing.T) {
			store := harness.NewMemoryStore()
			clock := newResumeTurnClock()
			provider := &multiContextCaptureModel{}
			host, runtime := newResumeTurnHarness(t, memory.NewStore(), store, provider, clock, nil)
			turn := seedAcceptedResumeTurn(t, store, clock, hasRoot, harness.ExecutionAgent)
			resumed, err := host.ResumeTurn(t.Context(), turn.ID)
			if !errors.Is(err, harness.ErrNotFound) || resumed.Turn.ID != turn.ID || resumed.Turn.Status != harness.TurnAccepted || resumed.Turn.Revision != turn.Revision || len(provider.requests) != 0 {
				t.Fatalf("unstarted admission was reconstructed: turn=%+v error=%v calls=%d", resumed.Turn, err, len(provider.requests))
			}
			if hasRoot {
				root, ok := harness.TopLevelInvocation(resumed)
				if !ok {
					t.Fatal("missing accepted root")
				}
				if _, err = runtime.Load(t.Context(), root.ExecutionRefID); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("Resume created a Runtime Run: %v", err)
				}
			}
		})
	}
}

func seedAcceptedResumeTurn(t *testing.T, store harness.Store, clock *resumeTurnClock, hasRoot bool, class harness.ExecutionClass) harness.Turn {
	t.Helper()
	options := resumeTurnSessionOptions()
	sessionID, err := harness.SessionID(options.HostThread)
	if err != nil {
		t.Fatal(err)
	}
	hostTurn := harness.HostRef{Kind: "conversation_turn", ID: "admitted"}
	turnID, err := harness.TurnID(sessionID, hostTurn)
	if err != nil {
		t.Fatal(err)
	}
	config, err := harness.SealConfigSnapshot(turnID, options.Config, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CreateSession(t.Context(), harness.Session{ID: sessionID, HostThread: options.HostThread, Actor: options.Actor, Revision: 1, CreatedAt: clock.Now(), UpdatedAt: clock.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutConfigSnapshot(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	turn := harness.Turn{ID: turnID, SessionID: sessionID, HostTurn: hostTurn, ConfigSnapshotID: config.ID, Status: harness.TurnAccepted, Revision: 1, CreatedAt: clock.Now(), UpdatedAt: clock.Now()}
	if _, _, err = store.CreateTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if hasRoot {
		key := harness.CapabilityAgent
		if class == harness.ExecutionTeam {
			key = harness.CapabilityTeam
		}
		id, idErr := harness.InvocationID(turnID, "", key, "admitted")
		if idErr != nil {
			t.Fatal(idErr)
		}
		if _, _, err = store.CreateInvocation(t.Context(), harness.Invocation{
			ID: id, TurnID: turnID, CapabilityKey: key, DefinitionVersion: harness.RuntimeCapabilityVersion,
			ExecutionClass: class, ExecutionRefID: harness.AgentExecutionRefID(id), Status: harness.InvocationAccepted,
			Attempt: 1, Revision: 1, CreatedAt: clock.Now(), UpdatedAt: clock.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	return turn
}

func TestResumeTurnRejectsOtherRootCapabilities(t *testing.T) {
	t.Parallel()
	store := harness.NewMemoryStore()
	clock := newResumeTurnClock()
	provider := &multiContextCaptureModel{}
	host, _ := newResumeTurnHarness(t, memory.NewStore(), store, provider, clock, nil)
	turn := seedAcceptedResumeTurn(t, store, clock, true, harness.ExecutionTeam)
	_, err := host.ResumeTurn(t.Context(), turn.ID)
	if !errors.Is(err, harness.ErrInvalidRequest) || len(provider.requests) != 0 {
		t.Fatalf("non-Agent root was driven: error=%v calls=%d", err, len(provider.requests))
	}
}

func TestResumeTurnRejectsMismatchedRuntimeIdentityBeforeDispatch(t *testing.T) {
	t.Parallel()
	options := resumeTurnSessionOptions()
	for _, candidate := range []struct {
		name  string
		kind  kernel.RunKind
		actor kernel.ActorRef
	}{
		{name: "kind", kind: "other-feature", actor: options.Actor},
		{name: "tenant", kind: agent.RunKind, actor: kernel.ActorRef{TenantID: "other", ActorID: options.Actor.ActorID}},
		{name: "actor", kind: agent.RunKind, actor: kernel.ActorRef{TenantID: options.Actor.TenantID, ActorID: "other"}},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			store := harness.NewMemoryStore()
			clock := newResumeTurnClock()
			provider := &multiContextCaptureModel{}
			host, runtime := newResumeTurnHarness(t, memory.NewStore(), store, provider, clock, nil)
			turn := seedAcceptedResumeTurn(t, store, clock, true, harness.ExecutionAgent)
			invocations, err := store.ListInvocations(t.Context(), turn.ID)
			if err != nil || len(invocations) != 1 {
				t.Fatalf("accepted root: %+v error=%v", invocations, err)
			}
			if _, err = runtime.Create(t.Context(), kernel.CreateRequest{
				ID: invocations[0].ExecutionRefID, Kind: candidate.kind, Actor: candidate.actor, Thread: options.Thread,
				Goal: "unrelated execution", State: json.RawMessage(`{}`),
			}); err != nil {
				t.Fatal(err)
			}
			snapshot, err := host.ResumeTurn(t.Context(), turn.ID)
			if !errors.Is(err, harness.ErrConflict) || snapshot.Output != nil || len(provider.requests) != 0 {
				t.Fatalf("mismatched runtime reached Agent: error=%v calls=%d output=%+v", err, len(provider.requests), snapshot.Output)
			}
		})
	}
}

type resumeTurnErrorAgent struct {
	unusedFeatureAgent
	runtime *kernel.Runtime
	cause   error
	zero    bool
	calls   int
}

func (resumer *resumeTurnErrorAgent) Resume(ctx context.Context, runID string, _ uint64) (kernel.Snapshot, error) {
	resumer.calls++
	if resumer.zero {
		return kernel.Snapshot{}, resumer.cause
	}
	snapshot, err := resumer.runtime.Load(ctx, runID)
	return snapshot, errors.Join(resumer.cause, err)
}

func TestResumeTurnPreservesExecutionErrorAndReadableSnapshot(t *testing.T) {
	t.Parallel()
	for _, zero := range []bool{false, true} {
		name := "snapshot"
		if zero {
			name = "zero-snapshot"
		}
		t.Run(name, func(t *testing.T) {
			clock := newResumeTurnClock()
			store := harness.NewMemoryStore()
			host, runtime := newResumeTurnHarness(t, memory.NewStore(), store, resumeTurnModelFunc(func(context.Context, model.Request) (model.Response, error) {
				return model.Response{Content: "pause", Continue: true}, nil
			}), clock, nil)
			pending, err := host.StartAgentTurn(t.Context(), testStartRequest())
			if err != nil || pending.Turn.Status != harness.TurnRunning {
				t.Fatalf("start: turn=%+v error=%v", pending.Turn, err)
			}
			cause := errors.Join(kernel.ErrConflict, errors.New("execution ownership changed"))
			resumer := &resumeTurnErrorAgent{runtime: runtime, cause: cause, zero: zero}
			restarted, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: resumer, Store: store, Clock: clock})
			if err != nil {
				t.Fatal(err)
			}
			observed, err := restarted.ResumeTurn(t.Context(), pending.Turn.ID)
			if !errors.Is(err, cause) || !errors.Is(err, kernel.ErrConflict) || observed.Turn.ID != pending.Turn.ID || observed.Turn.Status != harness.TurnRunning || resumer.calls != 1 {
				t.Fatalf("execution error was retried, hidden, or lost state: turn=%+v error=%v calls=%d", observed.Turn, err, resumer.calls)
			}
		})
	}
}

func TestResumeTurnDoesNotResolveApprovalOrGrantBudget(t *testing.T) {
	t.Parallel()
	t.Run("approval", func(t *testing.T) {
		host, config := newApprovalHarness(t)
		waiting := startApprovalHarness(t, host, config)
		checkpoint := latestApprovalCheckpoint(t, waiting)
		again, err := host.ResumeTurn(t.Context(), waiting.Turn.ID)
		if err != nil || again.Turn.Revision != waiting.Turn.Revision || latestApprovalCheckpoint(t, again) != checkpoint {
			t.Fatalf("approval changed during resume: turn=%+v error=%v", again.Turn, err)
		}
		assertHarnessApprovalWait(t, again, 1)
	})
	t.Run("budget", func(t *testing.T) {
		host, runtime, direct, provider := testAutoHarness(t, 1)
		request := testStartRequest()
		request.CallAllowance = &agent.CallAllowance{LLMCalls: 1}
		paused, err := host.StartAgentTurn(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		paused = resumeAutoSegments(t, host, runtime, direct, paused)
		if paused.Turn.Status != harness.TurnPausedBudget {
			t.Fatalf("expected exhausted allowance: %+v", paused.Turn)
		}
		again, err := host.ResumeTurn(t.Context(), paused.Turn.ID)
		if err != nil || again.Turn.Status != harness.TurnPausedBudget || again.Turn.Revision != paused.Turn.Revision || provider.calls != 1 {
			t.Fatalf("resume widened budget: turn=%+v error=%v calls=%d", again.Turn, err, provider.calls)
		}
	})
}

func TestResumeTurnDrivesCommittedControlDespiteStaleProjection(t *testing.T) {
	t.Parallel()
	t.Run("approval", func(t *testing.T) {
		store := harness.NewMemoryStore()
		clock := newResumeTurnClock()
		provider := &approvalTestModel{}
		var controller *agent.Runner
		host, runtime := newResumeTurnHarness(t, memory.NewStore(), store, provider, clock, func(dependencies *agent.Dependencies) {
			approvals, err := interaction.New(dependencies.Runtime)
			if err != nil {
				t.Fatal(err)
			}
			policy, err := harness.NewFrozenApprovalPolicy(store)
			if err != nil {
				t.Fatal(err)
			}
			registry := newApprovalTestRegistry(t)
			dependencies.Catalog, dependencies.Executor = registry, registry
			dependencies.Approvals = interactionadapter.New(approvals)
			dependencies.ApprovalPolicies = []plugin.ApprovalPolicy{policy}
			dependencies.DeferResumption = true
			controller, err = agent.NewRunner(*dependencies)
			if err != nil {
				t.Fatal(err)
			}
		})
		waiting := startApprovalHarness(t, host, harness.ConfigSnapshot{
			ToolKeys: []string{approvalTestToolKey},
			ToolPolicies: []harness.ToolPolicySnapshot{{
				Key: approvalTestToolKey, DefinitionVersion: "v1",
				ApprovalCapability: approvalTestCapabilityPerCall, ApprovalMode: "always",
			}},
		})
		checkpoint := latestApprovalCheckpoint(t, waiting)
		root, _ := harness.TopLevelInvocation(waiting)
		current, err := runtime.Load(t.Context(), root.ExecutionRefID)
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := controller.ResolveApproval(t.Context(), current.Run.ID, current.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
		if err != nil || resolved.Run.Status != kernel.RunStatusRunning || provider.calls != 1 {
			t.Fatalf("deferred approval dispatched: run=%+v error=%v calls=%d", resolved.Run, err, provider.calls)
		}
		stale, err := host.Load(t.Context(), waiting.Turn.ID)
		if err != nil || stale.Turn.Status != harness.TurnWaitingInput {
			t.Fatalf("expected stale waiting projection: turn=%+v error=%v", stale.Turn, err)
		}
		next, err := host.ResumeTurn(t.Context(), waiting.Turn.ID)
		if err != nil || next.Turn.Status != harness.TurnWaitingInput || provider.calls != 2 || latestApprovalCheckpoint(t, next) == checkpoint {
			t.Fatalf("first Resume did not consume the resolved approval: turn=%+v error=%v calls=%d", next.Turn, err, provider.calls)
		}
	})
	t.Run("allowance", func(t *testing.T) {
		host, runtime, direct, provider := testAutoHarness(t, 1)
		request := testStartRequest()
		request.CallAllowance = &agent.CallAllowance{LLMCalls: 1}
		paused, err := host.StartAgentTurn(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		paused = resumeAutoSegments(t, host, runtime, direct, paused)
		root, _ := harness.TopLevelInvocation(paused)
		current, err := runtime.Load(t.Context(), root.ExecutionRefID)
		if err != nil {
			t.Fatal(err)
		}
		granted, err := direct.GrantCallAllowance(t.Context(), current.Run.ID, current.Run.Revision, agent.CallAllowance{LLMCalls: 1})
		if err != nil || granted.Run.Status != kernel.RunStatusRunning || provider.calls != 1 {
			t.Fatalf("allowance grant dispatched: run=%+v error=%v calls=%d", granted.Run, err, provider.calls)
		}
		stale, err := host.Load(t.Context(), paused.Turn.ID)
		if err != nil || stale.Turn.Status != harness.TurnPausedBudget {
			t.Fatalf("expected stale allowance projection: turn=%+v error=%v", stale.Turn, err)
		}
		done, err := host.ResumeTurn(t.Context(), paused.Turn.ID)
		if err != nil || done.Turn.Status != harness.TurnCompleted || provider.calls != 2 {
			t.Fatalf("first Resume did not consume the committed allowance: turn=%+v error=%v calls=%d", done.Turn, err, provider.calls)
		}
	})
}

type resumeTurnRetryError struct{}

func (resumeTurnRetryError) Error() string   { return "retryable provider failure" }
func (resumeTurnRetryError) Retryable() bool { return true }

func TestResumeTurnHonorsDurableModelRetryAfter(t *testing.T) {
	t.Parallel()
	clock := newResumeTurnClock()
	base := memory.NewStore()
	store := harness.NewMemoryStore()
	calls := 0
	provider := resumeTurnModelFunc(func(context.Context, model.Request) (model.Response, error) {
		calls++
		if calls == 1 {
			return model.Response{}, resumeTurnRetryError{}
		}
		return model.Response{Content: "recovered"}, nil
	})
	host, runtime := newResumeTurnHarness(t, base, store, provider, clock, nil)
	pending, _ := host.StartAgentTurn(t.Context(), testStartRequest())
	if pending.Turn.Status != harness.TurnRunning || calls != 1 {
		t.Fatalf("retry was not left running: turn=%+v calls=%d", pending.Turn, calls)
	}
	root, _ := harness.TopLevelInvocation(pending)
	before, err := runtime.Load(t.Context(), root.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := agent.ViewState(before)
	if err != nil || len(view.ModelInvocations) != 1 || view.ModelInvocations[0].RetryAfter == nil {
		t.Fatalf("missing RetryAfter: view=%+v error=%v", view, err)
	}
	restarted, _ := newResumeTurnHarness(t, base, store, provider, clock, nil)
	blocked, err := restarted.ResumeTurn(t.Context(), pending.Turn.ID)
	if !errors.Is(err, agent.ErrModelInvocationBusy) || blocked.Turn.ID != pending.Turn.ID || blocked.Turn.Status != harness.TurnRunning || calls != 1 {
		t.Fatalf("premature resume bypassed due time: turn=%+v error=%v calls=%d", blocked.Turn, err, calls)
	}
	after, err := runtime.Load(t.Context(), root.ExecutionRefID)
	if err != nil || after.Run.Revision != before.Run.Revision {
		t.Fatalf("blocked retry mutated Runtime: run=%+v error=%v", after.Run, err)
	}
	clock.now = view.ModelInvocations[0].RetryAfter.Add(time.Millisecond)
	done, err := restarted.ResumeTurn(t.Context(), pending.Turn.ID)
	if err != nil || done.Turn.Status != harness.TurnCompleted || calls != 2 {
		t.Fatalf("due retry did not finish: turn=%+v error=%v calls=%d", done.Turn, err, calls)
	}
}

func TestResumeTurnDoesNotReplayUnknownNativeToolOutcome(t *testing.T) {
	t.Parallel()
	clock := newResumeTurnClock()
	base := memory.NewStoreWithExecutionClock(clock)
	store := harness.NewMemoryStore()
	toolCalls := 0
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: "external", Name: "external", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			toolCalls++
			return tools.ExecutionResult{Content: json.RawMessage(`{"started":true}`), Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: tools.ReceiptDispositionPending}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	provider := resumeTurnModelFunc(func(context.Context, model.Request) (model.Response, error) {
		return model.Response{ToolCalls: []tools.Call{{ID: "external-call", ToolKey: "external", Arguments: json.RawMessage(`{}`)}}}, nil
	})
	makeHost := func(owner string) *harness.Runner {
		host, _ := newResumeTurnHarness(t, base, store, provider, clock, func(dependencies *agent.Dependencies) {
			dependencies.Catalog, dependencies.Executor = registry, registry
			dependencies.RunExecution = &agent.RunExecution{OwnerID: owner, LeaseDuration: 5 * time.Minute}
		})
		return host
	}
	first := makeHost("first-owner")
	request := testStartRequest()
	request.Config.ToolKeys = []string{"external"}
	pending, err := first.StartAgentTurn(t.Context(), request)
	if err != nil || pending.Turn.Status != harness.TurnRunning || toolCalls != 1 {
		t.Fatalf("external call did not pause: turn=%+v error=%v calls=%d", pending.Turn, err, toolCalls)
	}
	root, _ := harness.TopLevelInvocation(pending)
	blocked, err := makeHost("fresh-owner").ResumeTurn(t.Context(), pending.Turn.ID)
	if !errors.Is(err, agent.ErrToolOutcomeUnknown) || blocked.Turn.Status != harness.TurnRunning || toolCalls != 1 {
		t.Fatalf("unknown external effect was replayed: turn=%+v error=%v calls=%d", blocked.Turn, err, toolCalls)
	}
	current, _ := harness.TopLevelInvocation(blocked)
	if current.ID != root.ID || current.ExecutionRefID != root.ExecutionRefID || current.Attempt != root.Attempt {
		t.Fatalf("unknown outcome caused a new attempt: before=%+v after=%+v", root, current)
	}
}

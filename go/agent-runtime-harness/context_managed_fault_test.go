package harness_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

var errBeforeModelIntentCommit = errors.New("injected failure before model intent commit")

type beforeModelIntentStore struct {
	kernel.Store
	failed atomic.Bool
}

func (store *beforeModelIntentStore) Apply(
	ctx context.Context,
	id string,
	revision uint64,
	mutation kernel.StoreMutation,
) (kernel.Snapshot, error) {
	for _, event := range mutation.Events {
		if event.Type == "agent.model_invocation.pending" && store.failed.CompareAndSwap(false, true) {
			return kernel.Snapshot{}, errBeforeModelIntentCommit
		}
	}
	return store.Store.Apply(ctx, id, revision, mutation)
}

func TestManagedContextResumeAfterRolloverBeforeModelIntentCommit(t *testing.T) {
	base := memory.NewStore()
	faulty := &beforeModelIntentStore{Store: base}
	harnessStore := harness.NewMemoryStore()
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	boundedContext, err := harness.NewBoundedContextAdapter(harnessStore, manager, func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
		return runtimecontext.ModelWindow{ContextTokens: 2000, ReservedOutputTokens: 128}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{
			Key: harness.ContextArtifactToolKey, Name: "read_context_artifact",
			InputSchema: []byte(`{"type":"object","properties":{"artifactID":{"type":"string"}},"required":["artifactID"]}`),
		},
		Handler: harness.NewContextArtifactToolHandler(harnessStore, nil),
	}})
	if err != nil {
		t.Fatal(err)
	}
	create := func(store kernel.Store, provider *multiContextCaptureModel) (*harness.Runner, *agent.Runner, *kernel.Runtime) {
		t.Helper()
		rt, err := kernel.New(kernel.Dependencies{Store: store})
		if err != nil {
			t.Fatal(err)
		}
		direct, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: provider, Catalog: registry, Executor: registry, BoundedContext: boundedContext})
		if err != nil {
			t.Fatal(err)
		}
		host, err := harness.NewRunner(harness.Dependencies{
			Runtime: rt, Agent: direct, Store: harnessStore, Clock: contextHarnessClock{},
			Context: manager, Catalog: registry,
		})
		if err != nil {
			t.Fatal(err)
		}
		return host, direct, rt
	}
	provider := &multiContextCaptureModel{}
	host, _, _ := create(faulty, provider)
	original := strings.Repeat("archived original verifiable details ", 350)
	request := harness.StartRequest{
		HostThread: harness.HostRef{Kind: "conversation", ID: "fault-rollover"},
		HostTurn:   harness.HostRef{Kind: "conversation_turn", ID: "turn"},
		Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "operator"},
		Thread:     kernel.ThreadRef{Kind: "conversation", ID: "fault-rollover"},
		Goal:       "current question",
		Config: harness.ConfigSnapshot{

			ContextPolicy: runtimecontext.Policy{PreserveRecentTurns: 1, MaxCompactionTokens: 80}, AgentSpec: harness.AgentSpec{Model: "fixture", ToolKeys: []string{harness.ContextArtifactToolKey}},
		},
		Context: contextSeed("durable guidance",
			contextEntry("m1", "old", model.RoleUser, "old question", false),
			contextEntry("a1", "old", model.RoleAssistant, original, false),
			contextEntry("m2", "current", model.RoleUser, "current question", true),
		),
	}
	_, startErr := host.StartAgentTurn(t.Context(), request)
	if !errors.Is(startErr, errBeforeModelIntentCommit) || !faulty.failed.Load() || len(provider.requests) != 0 {
		t.Fatalf("expected crash after archival but before dispatch: %v, failed=%v model=%d", startErr, faulty.failed.Load(), len(provider.requests))
	}
	sessionID, err := harness.SessionID(request.HostThread)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := harness.TurnID(sessionID, request.HostTurn)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := harnessStore.GetTurn(t.Context(), turnID)
	if err != nil || turn.ContextRef.Generation != 2 {
		t.Fatalf("compaction checkpoint not committed: %+v err=%v", turn, err)
	}
	firstCheckpoint := turn.ContextCheckpointID
	invocationID, err := harness.AgentInvocationID(turnID, "", turnID)
	if err != nil {
		t.Fatal(err)
	}
	runID := harness.AgentExecutionRefID(invocationID)
	pending, err := base.Load(t.Context(), runID)
	if err != nil || pending.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("pending run unavailable: %+v err=%v", pending.Run, err)
	}
	freshModel := &multiContextCaptureModel{}
	resumedHost, direct, rt := create(base, freshModel)
	completed, err := direct.Resume(t.Context(), runID, pending.Run.Revision)

	if err != nil || completed.Run.Status != kernel.RunStatusCompleted || len(freshModel.requests) != 1 {
		t.Fatalf("resume failed: status=%s err=%v calls=%d", completed.Run.Status, err, len(freshModel.requests))
	}
	for _, msg := range freshModel.requests[0].Messages {
		if strings.Contains(model.TextValue(msg.Content), original) {
			t.Fatal("committed rollover re-expanded archived history after crash")
		}
	}
	synced, err := resumedHost.Refresh(t.Context(), turnID)
	if err != nil || synced.Turn.Status != harness.TurnCompleted || synced.Turn.ContextCheckpointID != firstCheckpoint {
		t.Fatalf("resume changed context ownership: status=%s id=%s err=%v", synced.Turn.Status, synced.Turn.ContextCheckpointID, err)
	}
	assertPersistedModelHash(t, rt, synced, freshModel.requests[0])
}

func TestManagedPreparationPreservesCommittedContextRollover(t *testing.T) {
	for _, forged := range []bool{false, true} {
		name := "exact"
		if forged {
			name = "forged"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			clock := &managedObligationClock{now: time.Now().UTC()}
			base := memory.NewStoreWithExecutionClock(clock)
			cut := &managedObligationStore{Store: base, event: "agent.model_invocation.pending", count: 1, before: true, allEvents: true}
			if forged {
				cut.event = "agent.native_preparation.ready"
				cut.before = false
			}
			host := harness.NewMemoryStore()
			client := &agentSessionModel{}
			opts := agentSessionOptions(t, cut, host, client)
			rt, err := kernel.New(kernel.Dependencies{Store: cut, ManagedRecoveryHost: host})
			if err != nil {
				t.Fatal(err)
			}
			opts.Runtime = rt
			opts.Context.ManagedRecovery = true
			opts.Context.ReadArtifacts = true
			opts.Session.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 2}
			opts.Session.Config.ContextPolicy = runtimecontext.Policy{PreserveRecentTurns: 1, MinimumRecentTurns: 1, MaxCompactionTokens: 80}
			opts.Context.ResolveWindow = func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
				return runtimecontext.ModelWindow{ContextTokens: 2000, ReservedOutputTokens: 128}, nil
			}
			originalText := strings.Repeat("archived original verifiable details ", 350)
			opts.Session.Transcript = harness.TranscriptSourceFunc(func(context.Context, harness.HostRef, harness.HostRef, string) (*harness.ContextSeed, error) {
				return contextSeed("durable guidance", contextEntry("m1", "old", model.RoleUser, "old question", false), contextEntry("a1", "old", model.RoleAssistant, originalText, false), contextEntry("m2", "current", model.RoleUser, "current question", true)), nil
			})
			session, err := harness.OpenAgentSession(opts)
			if err != nil {
				t.Fatal(err)
			}
			crashManagedObligation(t, func() { _, _ = session.Prompt(ctx, "rollover", "current question") })
			pending, err := session.Load(ctx, "rollover")
			if err != nil {
				t.Fatal(err)
			}
			root, ok := harness.TopLevelInvocation(pending)
			if !ok {
				t.Fatal("missing root")
			}
			before, err := base.Load(ctx, root.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			point, err := rt.LoadManagedRecovery(ctx, root.ExecutionRefID)
			proof, valid := point.SafePoint.AgentObligations()
			if err != nil || !valid || proof.Phase != "prepare" || proof.PreparationContext == nil || !kernel.ManagedSourceMatches(point.SafePoint, before) || len(client.requests) != 0 {
				t.Fatalf("wrong original frontier: %v %+v", err, proof)
			}
			turn := pending.Turn
			if forged {
				previous, e := host.GetContextCheckpoint(ctx, proof.PreparationContext.CheckpointID)
				if e != nil {
					t.Fatal(e)
				}
				window := runtimecontext.CloneWindow(previous.Window)
				window.Entries[len(window.Entries)-1].Message.Content = model.TextParts("substituted user question")
				changed, e := runtimecontext.NewManager(runtimecontext.Dependencies{}).Rollover(ctx, runtimecontext.RolloverRequest{Previous: previous, Window: window, Reason: "model_window_pressure", ModelWindowFingerprint: runtimecontext.ModelWindowFingerprint(runtimecontext.ModelWindow{ContextTokens: 2000, ReservedOutputTokens: 128})})
				if e != nil {
					t.Fatal(e)
				}
				turn, e = host.CommitContextCheckpoint(ctx, harness.ContextCheckpointCommit{TurnID: turn.ID, ExpectedTurnRevision: turn.Revision, ExpectedTurnCheckpointID: previous.ID, ExpectedHeadCheckpointID: previous.ID, Checkpoint: changed, UpdatedAt: time.Now().UTC()})
				if e != nil {
					t.Fatal(e)
				}
			} else if turn.ContextRef.Generation != 2 || turn.ContextCheckpointID == proof.PreparationContext.CheckpointID {
				t.Fatalf("native rollover not committed: %+v", turn.ContextRef)
			}
			opts.Session.Transcript = nil
			opts.Runtime, err = kernel.New(kernel.Dependencies{Store: base, ManagedRecoveryHost: host})
			if err != nil {
				t.Fatal(err)
			}
			session, err = harness.OpenAgentSession(opts)
			if err != nil {
				t.Fatal(err)
			}
			clock.now = clock.now.Add(25 * time.Hour)
			done, err := session.Resume(ctx, "rollover")
			if forged {
				current, e := base.Load(ctx, root.ExecutionRefID)
				if err == nil || len(client.requests) != 0 || e != nil || !reflect.DeepEqual(current, before) {
					t.Fatalf("forged context enabled recovery: %v", err)
				}
				same, e := opts.Runtime.LoadManagedRecovery(ctx, root.ExecutionRefID)
				if e != nil || !same.Valid() {
					t.Fatalf("forged context acquired selection: %v %s", e, same.Selection.Mode)
				}
				return
			}
			if err != nil || done.Turn.Status != harness.TurnCompleted || len(client.requests) != 1 || done.Turn.ContextCheckpointID != turn.ContextCheckpointID {
				t.Fatalf("original rollover recovery failed: %v %s calls=%d context=%s", err, done.Turn.Status, len(client.requests), done.Turn.ContextCheckpointID)
			}
			if strings.Contains(requestTextManaged(client.requests[0]), originalText) {
				t.Fatal("re-expanded original archive")
			}
			assertPersistedModelHash(t, opts.Runtime, done, client.requests[0])
		})
	}
}

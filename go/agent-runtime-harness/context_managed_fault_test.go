package harness_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
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
	archive, err := harness.NewManagedContextPreparer(harnessStore, manager, func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
		return runtimecontext.ModelWindow{ContextTokens: 2000, ReservedOutputTokens: 128}, nil
	})
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
		hydrate, err := harness.NewContextRunMiddleware(harnessStore, nil)
		if err != nil {
			t.Fatal(err)
		}
		direct, err := agent.NewRunner(agent.Dependencies{
			Runtime: rt, Model: provider, Catalog: registry, Executor: registry,
			PrepareModelRequest: archive, RunMiddleware: []plugin.RunMiddleware{hydrate},
		})
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
			Model: "fixture", ToolKeys: []string{harness.ContextArtifactToolKey},
			ContextPolicy: runtimecontext.Policy{PreserveRecentTurns: 1, MaxCompactionTokens: 80},
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
	invocationID, err := harness.AgentInvocationID(turnID, turnID)
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

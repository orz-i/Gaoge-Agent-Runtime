package harness_test

import (
	"context"
	"errors"
	"strings"
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

func TestManagedContextCompactsLongSessionAndPersistsExactArtifacts(t *testing.T) {
	kernelStore := memory.NewStore()
	harnessStore := harness.NewMemoryStore()
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	prepare, err := harness.NewManagedContextPreparer(harnessStore, manager, func(_ context.Context, request model.Request) (runtimecontext.ModelWindow, error) {
		if request.Model != "fixture" {
			t.Fatalf("wrong model window request: %s", request.Model)
		}
		return runtimecontext.ModelWindow{ContextTokens: 2000, ReservedOutputTokens: 128}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{
			Key: harness.ContextArtifactToolKey, Name: "read_context_artifact", Description: "Read archived Context evidence",
			InputSchema: []byte(`{"type":"object","properties":{"artifactID":{"type":"string"}},"required":["artifactID"]}`),
		},
		Handler: harness.NewContextArtifactToolHandler(harnessStore, nil),
	}})
	if err != nil {
		t.Fatal(err)
	}
	oldEvidence := strings.Repeat("old exact verified evidence ", 350)
	entries := []runtimecontext.Entry{
		contextEntry("m1", "old1", model.RoleUser, "earlier topic", false),
		contextEntry("a1", "old1", model.RoleAssistant, oldEvidence, false),
		contextEntry("m2", "old2", model.RoleUser, "earlier followup", false),
		contextEntry("a2", "old2", model.RoleAssistant, "earlier answer", false),
		contextEntry("m3", "current", model.RoleUser, "current question", true),
	}
	provider := &multiContextCaptureModel{}
	makeRunner := func(provider *multiContextCaptureModel) *harness.Runner {
		rt, err := kernel.New(kernel.Dependencies{Store: kernelStore})
		if err != nil {
			t.Fatal(err)
		}
		direct, err := agent.NewRunner(agent.Dependencies{
			Runtime: rt, Model: provider, Catalog: reg, Executor: reg, PrepareModelRequest: prepare,
			ModelMiddleware: []plugin.ModelMiddleware{harness.NewContextWindowMiddleware()},
		})
		if err != nil {
			t.Fatal(err)
		}
		runner, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: direct, Store: harnessStore, Clock: contextHarnessClock{}, Context: manager, Catalog: reg})
		if err != nil {
			t.Fatal(err)
		}
		return runner
	}
	runner := makeRunner(provider)
	request := harness.StartRequest{
		HostThread: harness.HostRef{Kind: "conversation", ID: "managed-long"},
		HostTurn:   harness.HostRef{Kind: "conversation_turn", ID: "current"},
		Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "operator"},
		Thread:     kernel.ThreadRef{Kind: "conversation", ID: "managed-long"},
		Goal:       "current question",
		Config: harness.ConfigSnapshot{Model: "fixture", ToolKeys: []string{harness.ContextArtifactToolKey},
			ContextPolicy: runtimecontext.Policy{PreserveRecentTurns: 1, MinimumRecentTurns: 1, MaxCompactionTokens: 80}},
		Context: contextSeed("session policy", entries...),
	}
	snapshot, err := runner.Start(t.Context(), request)
	if err != nil || snapshot.Turn.Status != harness.TurnCompleted {
		t.Fatalf("managed start: %v, status=%s", err, snapshot.Turn.Status)
	}
	if snapshot.Turn.ContextRef.Generation != 2 || snapshot.Turn.ContextCheckpointID == "" {
		t.Fatalf("expected durable rollover: %+v", snapshot.Turn.ContextRef)
	}
	checkpoint, err := harnessStore.GetContextCheckpoint(t.Context(), snapshot.Turn.ContextCheckpointID)
	if err != nil || len(checkpoint.ArtifactIDs) == 0 || checkpoint.Trace.Reason != "model_window_pressure" {
		t.Fatalf("missing archived context: %+v err=%v", checkpoint, err)
	}
	archived := false
	for _, artifactID := range checkpoint.ArtifactIDs {
		artifact, err := harnessStore.GetContextArtifact(t.Context(), artifactID)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(artifact.Content, oldEvidence) || strings.Contains(string(artifact.ContentJSON), "old exact verified evidence") {
			archived = true
		}
	}
	if !archived {
		t.Fatal("the original removed transcript was not stored")
	}
	if len(provider.requests) != 1 || len(provider.requests[0].Messages) == 0 {
		t.Fatalf("model was not sent the compacted request: %+v", provider.requests)
	}
	for _, message := range provider.requests[0].Messages {
		if strings.Contains(message.Content, oldEvidence) {
			t.Fatal("uncompacted evidence leaked back into model request")
		}
	}
	// Recreating the Runner does not lose the committed Context revision or
	// archived facts. Host source still owns the complete original transcript.
	reopened := makeRunner(&multiContextCaptureModel{})
	reloaded, err := reopened.Load(t.Context(), snapshot.Turn.ID)
	if err != nil || reloaded.Turn.ContextRef != snapshot.Turn.ContextRef {
		t.Fatalf("restart lost context lineage: %+v err=%v", reloaded.Turn.ContextRef, err)
	}
	// The next host Turn must reuse the archived checkpoint rather than
	// reintroducing the original oversized source after process restart.
	followupProvider := &multiContextCaptureModel{}
	followupRunner := makeRunner(followupProvider)
	followup := request
	followup.HostTurn.ID = "followup"
	followup.Goal = "next question"
	followup.Context = contextSeed("session policy", append(
		append([]runtimecontext.Entry(nil), entries...),
		contextEntry("a3", "current", model.RoleAssistant, "answer", false),
		contextEntry("m4", "followup", model.RoleUser, "next question", true),
	)...)
	followupSnapshot, err := followupRunner.Start(t.Context(), followup)
	if err != nil || followupSnapshot.Turn.Status != harness.TurnCompleted || len(followupProvider.requests) != 1 {
		t.Fatalf("followup after rollover: status=%s err=%v requests=%d", followupSnapshot.Turn.Status, err, len(followupProvider.requests))
	}
	if followupSnapshot.Turn.ContextRef.Generation < snapshot.Turn.ContextRef.Generation {
		t.Fatalf("followup lost compacted Context: %+v", followupSnapshot.Turn.ContextRef)
	}
	seenNew := false
	for _, msg := range followupProvider.requests[0].Messages {
		if strings.Contains(msg.Content, oldEvidence) {
			t.Fatal("next Turn replayed archived transcript")
		}
		if msg.Content == "next question" {
			seenNew = true
		}
	}
	if !seenNew {
		t.Fatal("next prompt missing after rollover")
	}
}

func TestManagedContextRefusesLossyCompactionWithoutArtifactReader(t *testing.T) {
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	store := harness.NewMemoryStore()
	prepare, err := harness.NewManagedContextPreparer(store, manager, func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
		return runtimecontext.ModelWindow{ContextTokens: 1000, ReservedOutputTokens: 128}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	provider := &multiContextCaptureModel{}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: provider, PrepareModelRequest: prepare})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: direct, Store: store, Clock: contextHarnessClock{}, Context: manager})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Start(t.Context(), harness.StartRequest{
		HostThread: harness.HostRef{Kind: "conversation", ID: "no-reader"},
		HostTurn:   harness.HostRef{Kind: "conversation_turn", ID: "current"},
		Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread:     kernel.ThreadRef{Kind: "conversation", ID: "no-reader"},
		Goal:       "current question", Config: harness.ConfigSnapshot{Model: "fixture"},
		Context: contextSeed("", contextEntry("m1", "old", model.RoleAssistant, strings.Repeat("large evidence", 500), false),
			contextEntry("m2", "current", model.RoleUser, "current question", true)),
	})
	if !errors.Is(err, runtimecontext.ErrBudgetExceeded) || len(provider.requests) != 0 {
		t.Fatalf("unsafe context dispatched: %v, requests=%d", err, len(provider.requests))
	}
}

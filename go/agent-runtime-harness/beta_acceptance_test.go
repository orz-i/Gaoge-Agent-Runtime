package harness_test

import (
	"context"
	"encoding/json"
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

const betaAcceptanceToolKey = "beta.lookup"

func TestContextV2BetaAcceptanceSurvivesApprovalCompactionAndRestart(t *testing.T) {
	kernelStore := memory.NewStore()
	harnessStore := harness.NewMemoryStore()
	var executions atomic.Int32
	firstRuntime := newBetaAcceptanceRuntime(t, kernelStore)
	firstModel := &betaAcceptanceModel{}
	firstRunner := newBetaAcceptanceRunner(t, firstRuntime, harnessStore, firstModel, &executions)
	seed := &harness.ContextSeed{
		SourcePath:   []string{"message-old", "message-current"},
		Instructions: "Keep durable evidence references.",
		Entries: []runtimecontext.Entry{
			{
				ID: "entry-old", SourceID: "message-old", TurnID: "turn-old",
				Message: model.Message{Role: model.RoleAssistant, Content: "Earlier durable answer."},
			},
			{
				ID: "entry-current", SourceID: "message-current", TurnID: "turn-current", Required: true,
				Message: model.Message{Role: model.RoleUser, Content: "Use the approved lookup."},
			},
		},
	}
	waiting, err := firstRunner.Start(t.Context(), harness.StartRequest{
		HostThread: harness.HostRef{Kind: "conversation", ID: "beta-thread"},
		HostTurn:   harness.HostRef{Kind: "conversation_turn", ID: "beta-turn"},
		Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread:     kernel.ThreadRef{Kind: "conversation", ID: "beta-thread"},
		Goal:       "Use the approved lookup.",
		Config: harness.ConfigSnapshot{
			Instructions: "Answer from verified Tool output.", Model: "deterministic-beta-model",
			ToolKeys: []string{betaAcceptanceToolKey},
			ToolPolicies: []harness.ToolPolicySnapshot{{
				Key: betaAcceptanceToolKey, DefinitionVersion: "v1",
				ApprovalCapability: "per_call", ApprovalMode: "always",
			}},
		},
		Context: seed,
	})
	if err != nil {
		t.Fatalf("start Beta acceptance turn: %v", err)
	}
	if waiting.Turn.Status != harness.TurnWaitingInput || waiting.Turn.ContextCheckpointID == "" || executions.Load() != 0 {
		t.Fatalf("durable approval boundary = %#v executions=%d", waiting.Turn, executions.Load())
	}
	initialCheckpoint, err := harnessStore.GetContextCheckpoint(t.Context(), waiting.Turn.ContextCheckpointID)
	if err != nil || initialCheckpoint.CoveredThroughSourceID != "message-current" {
		t.Fatalf("initial durable Context V2 checkpoint = %#v, err=%v", initialCheckpoint, err)
	}

	// Rebuild every process-owned Runtime object while retaining only durable stores.
	secondRuntime := newBetaAcceptanceRuntime(t, kernelStore)
	secondModel := &betaAcceptanceModel{}
	secondRunner := newBetaAcceptanceRunner(t, secondRuntime, harnessStore, secondModel, &executions)
	completed, err := secondRunner.ResolveApproval(t.Context(), waiting.Turn.ID, harness.ResolveApprovalRequest{
		Decision: harness.ApprovalApprove, Comment: "approved after restart",
	})
	if err != nil {
		t.Fatalf("resume approval after reconstruction: %v", err)
	}
	if completed.Turn.Status != harness.TurnCompleted || executions.Load() != 1 || len(secondModel.requests) != 1 {
		t.Fatalf("resumed turn = %#v executions=%d modelCalls=%d", completed.Turn, executions.Load(), len(secondModel.requests))
	}
	if !containsToolResult(secondModel.requests[0].Messages) {
		t.Fatalf("resumed model request lost Tool transaction: %#v", secondModel.requests[0].Messages)
	}

	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	compacted, err := manager.CompactToolResults(
		initialCheckpoint.ScopeID,
		initialCheckpoint.Generation,
		secondModel.requests[0].Messages,
		256,
	)
	if err != nil || len(compacted.Artifacts) != 1 || len(compacted.Messages) != len(secondModel.requests[0].Messages) {
		t.Fatalf("compact accepted Tool result = %#v, err=%v", compacted, err)
	}
	for _, artifact := range compacted.Artifacts {
		if _, fresh, putErr := harnessStore.PutContextArtifact(t.Context(), artifact); putErr != nil || !fresh {
			t.Fatalf("persist compacted Tool artifact fresh=%v err=%v", fresh, putErr)
		}
	}
	advanced, err := manager.Capture(t.Context(), runtimecontext.CaptureRequest{
		Previous: initialCheckpoint, StaticFingerprint: initialCheckpoint.StaticFingerprint,
		RunID: "beta-runtime-tail", Messages: compacted.Messages, Artifacts: compacted.Artifacts,
	})
	if err != nil || advanced.ParentCheckpointID != initialCheckpoint.ID || advanced.Revision != initialCheckpoint.Revision+1 {
		t.Fatalf("capture compacted runtime tail = %#v, err=%v previous=%#v messages=%#v", advanced, err, runtimecontext.Materialize(initialCheckpoint.Window), compacted.Messages)
	}
	updatedTurn, err := harnessStore.CommitContextCheckpoint(t.Context(), harness.ContextCheckpointCommit{
		TurnID: completed.Turn.ID, ExpectedTurnRevision: completed.Turn.Revision,
		ExpectedTurnCheckpointID: initialCheckpoint.ID, ExpectedHeadCheckpointID: initialCheckpoint.ID,
		Checkpoint: advanced, UpdatedAt: time.Now().UTC(),
	})
	if err != nil || updatedTurn.ContextCheckpointID != advanced.ID {
		t.Fatalf("commit compacted Context V2 checkpoint = %#v, err=%v", updatedTurn, err)
	}

	thirdRuntime := newBetaAcceptanceRuntime(t, kernelStore)
	thirdRunner := newBetaAcceptanceRunner(t, thirdRuntime, harnessStore, &betaAcceptanceModel{}, &executions)
	restarted, err := thirdRunner.Load(t.Context(), completed.Turn.ID)
	if err != nil || restarted.Turn.ContextCheckpointID != advanced.ID || restarted.Turn.Status != harness.TurnCompleted {
		t.Fatalf("restart projection = %#v, err=%v", restarted.Turn, err)
	}
	artifact, err := harnessStore.GetContextArtifact(t.Context(), compacted.Artifacts[0].ID)
	if err != nil || artifact.ContentHash != compacted.Artifacts[0].ContentHash || !strings.Contains(artifact.Content, "verified-") {
		t.Fatalf("restart artifact = %#v, err=%v", artifact, err)
	}
}

func newBetaAcceptanceRuntime(t *testing.T, store kernel.Store) *kernel.Runtime {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func newBetaAcceptanceRunner(
	t *testing.T,
	runtime *kernel.Runtime,
	store *harness.MemoryStore,
	provider *betaAcceptanceModel,
	executions *atomic.Int32,
) *harness.Runner {
	t.Helper()
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{
			Key: betaAcceptanceToolKey, Name: "Beta Lookup", InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			executions.Add(1)
			content := `{"value":"` + strings.Repeat("verified-", 160) + `"}`
			return tools.ExecutionResult{
				Content: json.RawMessage(content),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"},
			}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	approvals, err := interaction.New(runtime)
	if err != nil {
		t.Fatal(err)
	}
	approvalPolicy, err := harness.NewFrozenApprovalPolicy(store)
	if err != nil {
		t.Fatal(err)
	}
	agentRunner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: provider, Catalog: registry, Executor: registry,
		Approvals: interactionadapter.New(approvals), ApprovalPolicies: []plugin.ApprovalPolicy{approvalPolicy},
		ModelMiddleware: []plugin.ModelMiddleware{harness.NewContextWindowMiddleware()},
		Limits:          agent.Limits{MaxLLMCalls: 2, MaxToolCalls: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: agentRunner, Store: store, Catalog: registry,
		Clock: contextHarnessClock{}, Context: runtimecontext.NewManager(runtimecontext.Dependencies{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

type betaAcceptanceModel struct{ requests []model.Request }

func (provider *betaAcceptanceModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	provider.requests = append(provider.requests, model.CloneRequest(request))
	if containsToolResult(request.Messages) {
		return model.Response{Content: "Accepted durable Tool evidence."}, nil
	}
	return model.Response{ToolCalls: []tools.Call{{
		ID: "beta-lookup-call", ToolKey: betaAcceptanceToolKey, Arguments: json.RawMessage(`{}`),
	}}}, nil
}

func containsToolResult(messages []model.Message) bool {
	for _, message := range messages {
		if message.Role == model.RoleTool && message.ToolCallID == "beta-lookup-call" {
			return true
		}
	}
	return false
}

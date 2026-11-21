package harness

import (
	"context"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type summaryRetryModel struct {
	summaries int
	business  int
	contracts []string
}

func (*summaryRetryModel) Capabilities(context.Context, string) (model.Capabilities, error) {
	return model.Capabilities{StructuredJSONSchema: true}, nil
}

func (client *summaryRetryModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	name := ""
	if request.OutputContract != nil {
		name = request.OutputContract.Name
	}
	client.contracts = append(client.contracts, name)
	response := model.Response{Usage: &model.Usage{InputTokens: 2, OutputTokens: 1}}
	if request.OutputContract != nil && request.OutputContract.Name == "working_state_summary" {
		client.summaries++
		response.Content = `{"goal":"known invalid summary"}`
		return response, nil
	}
	client.business++
	response.Content = "answer"
	return response, nil
}

func TestRetryInvocationCannotBuyAnotherSummaryForSameTurn(t *testing.T) {
	store, runs := NewMemoryStore(), memory.NewStore()
	runtime, err := kernel.New(kernel.Dependencies{Store: runs})
	if err != nil {
		t.Fatal(err)
	}
	client := &summaryRetryModel{}
	prompt := strings.TrimSpace("current request " + strings.Repeat("current evidence; ", 700))
	seed := &ContextSeed{SourcePath: []string{"old-user", "old-answer", "current-user"}, Entries: []runtimecontext.Entry{
		{ID: "old-user", SourceID: "old-user", TurnID: "old", Message: model.Message{Role: model.RoleUser, Content: model.TextParts(strings.Repeat("original evidence; ", 700))}},
		{ID: "old-answer", SourceID: "old-answer", TurnID: "old", Message: model.Message{Role: model.RoleAssistant, Content: model.TextParts("verified")}},
		{ID: "current-user", SourceID: "current-user", TurnID: "current", Required: true, Message: model.Message{Role: model.RoleUser, Content: model.TextParts(prompt)}},
	}}
	session, err := OpenAgentSession(AgentSessionOptions{
		Runtime: runtime, Store: store, Model: client,
		Session: SessionOptions{
			HostThread: HostRef{Kind: "conversation", ID: "summary-retry"}, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "summary-retry"},
			Config: ConfigSnapshot{
				Model: "fixture", MaxModelOutputTokens: 128,
				WorkingStateSummary: &agent.WorkingStateSummaryPolicy{MaxInputTokens: 60000, MaxSerializedBytes: 100000},
				ContextPolicy:       runtimecontext.Policy{SoftLimitPercent: 10, MaxSerializedBytes: 100000, PreserveRecentTurns: 1, MinimumRecentTurns: 1, MaxCompactionTokens: 128},
				Limits:              agent.Limits{MaxTotalTokens: 5},
			},
			Transcript: TranscriptSourceFunc(func(context.Context, HostRef, HostRef, string) (*ContextSeed, error) { return seed, nil }),
		},
		Context: AgentSessionContextOptions{ReadArtifacts: true, ResolveWindow: func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
			return runtimecontext.ModelWindow{ContextTokens: 65536, ReservedOutputTokens: 128}, nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, firstErr := session.Prompt(t.Context(), "current", prompt)
	if first.Turn.Status != TurnFailed || client.summaries != 1 || client.business != 1 {
		root, _ := TopLevelInvocation(first)
		snap, _ := runtime.Load(t.Context(), root.ExecutionRefID)
		view, _ := agent.ViewState(snap)
		checkpoint, _ := store.GetContextCheckpoint(t.Context(), first.Turn.ContextCheckpointID)
		t.Fatalf("fixture did not finish one known summary and fail business: status=%s summaries=%d business=%d contracts=%v summaryPolicy=%+v nativeOutput=%+v nativeMessages=%d aligned=%v trace=%+v err=%v", first.Turn.Status, client.summaries, client.business, client.contracts, first.Config.WorkingStateSummary, view.OutputContract, len(view.Messages), runtimecontext.SourceAlignedCheckpoint(checkpoint), checkpoint.Trace, firstErr)
	}
	root, ok := TopLevelInvocation(first)
	if !ok {
		t.Fatal("missing original root")
	}
	original, err := runtime.Load(t.Context(), root.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	originalView, err := agent.ViewState(original)
	if err != nil || originalView.Budget.Usage.LLMCalls != 2 || originalView.Budget.Usage.TotalTokens != 6 ||
		len(originalView.ModelInvocations) != 2 || originalView.ModelInvocations[0].Purpose != agent.ModelInvocationContextSummary ||
		originalView.ModelInvocations[0].Status != agent.ModelInvocationConsumed {
		t.Fatalf("known invalid summary or exhausted budget lost its original bill: %+v err=%v", originalView.Budget, err)
	}
	retried, err := session.runner.RetryInvocation(t.Context(), root.ID)
	if err != nil || retried.Turn.Status != TurnCompleted || client.summaries != 1 || client.business != 2 {
		t.Fatalf("RetryInvocation repeated a charged summary: status=%s summaries=%d business=%d err=%v", retried.Turn.Status, client.summaries, client.business, err)
	}
	retained, err := runtime.Load(t.Context(), root.ExecutionRefID)
	if err != nil || retained.Run.Revision != original.Run.Revision {
		t.Fatal("new attempt changed the original billed Run")
	}
	newRoot, ok := TopLevelInvocation(retried)
	if !ok || newRoot.Attempt != 2 {
		t.Fatal("Retry did not rotate the original Invocation")
	}
	newNative, err := runtime.Load(t.Context(), newRoot.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	newView, err := agent.ViewState(newNative)
	if err != nil || newView.Budget.Usage.LLMCalls != 1 || newView.Budget.Usage.TotalTokens != 3 ||
		len(newView.ModelInvocations) != 1 || newView.ModelInvocations[0].Purpose != "" {
		t.Fatalf("new Attempt hid another summary bill: %+v err=%v", newView.Budget, err)
	}
}

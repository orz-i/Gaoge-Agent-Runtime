package harness_test

import (
	"context"
	"encoding/json"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type harnessStructuredModel struct{ calls []model.Request }

func (client *harnessStructuredModel) Capabilities(_ context.Context, name string) (model.Capabilities, error) {
	return model.Capabilities{StructuredJSONSchema: name == "fixture-structured"}, nil
}
func (client *harnessStructuredModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	client.calls = append(client.calls, model.CloneRequest(request))
	return model.Response{Content: `{"answer":"verified"}`}, nil
}
func TestSessionCarriesFrozenStructuredOutputAcrossTurns(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	client := &harnessStructuredModel{}
	agentRunner, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: client, PrepareModelRequest: harness.MaterializeContextWindowRequest,
	})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	host, err := harness.NewRunner(harness.Dependencies{
		Runtime: rt, Agent: agentRunner, Store: store,
		Clock: contextHarnessClock{}, Context: runtimecontext.NewManager(runtimecontext.Dependencies{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	transcript := harness.TranscriptSourceFunc(func(_ context.Context, _, turn harness.HostRef, prompt string) (*harness.ContextSeed, error) {
		if turn.ID == "first" {
			return contextSeed("",
				contextEntry("first-user", "first", model.RoleUser, prompt, true),
			), nil
		}
		if turn.ID == "second" {
			return contextSeed("",
				contextEntry("first-user", "first", model.RoleUser, "first question", false),
				contextEntry("first-answer", "first", model.RoleAssistant, `{"answer":"verified"}`, false),
				contextEntry("second-user", "second", model.RoleUser, prompt, true),
			), nil
		}
		return nil, harness.ErrNotFound
	})
	options := harness.SessionOptions{
		HostThread: harness.HostRef{Kind: "conversation", ID: "schema-session"},
		Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread:     kernel.ThreadRef{Kind: "conversation", ID: "schema-session"},
		Config: harness.ConfigSnapshot{Model: "fixture-structured",
			OutputContract: &model.OutputContract{Name: "answer", Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`)},
		},
		Transcript: transcript,
	}
	session, err := host.OpenSession(options)
	if err != nil {
		t.Fatal(err)
	}
	first, err := session.Prompt(t.Context(), "first", "first question")
	if err != nil || first.Turn.Status != harness.TurnCompleted || first.Output == nil || first.Output.ContentType != "application/json" ||
		string(first.Output.Content) != `{"answer":"verified"}` {
		t.Fatalf("structured Harness first: %+v err=%v", first.Turn, err)
	}
	config, err := store.GetConfigSnapshot(t.Context(), first.Turn.ConfigSnapshotID)
	if err != nil || config.OutputContract == nil || config.OutputContract.Name != "answer" {
		t.Fatalf("sealed structured schema missing: %+v %v", config, err)
	}
	if len(client.calls) != 1 || client.calls[0].OutputContract == nil {
		t.Fatalf("first request schema missing: %+v", client.calls)
	}
	replay, err := session.Prompt(t.Context(), "first", "first question")
	if err != nil || replay.Turn.ID != first.Turn.ID || len(client.calls) != 1 {
		t.Fatalf("Session replay duplicated structured request: %+v err=%v", replay.Turn, err)
	}

	// Output configuration changes invalidate the Context static fingerprint;
	// a Host may not silently reuse an earlier cached provider prefix.
	options.Config.OutputContract.Name = "different_answer"
	changed, err := host.OpenSession(options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := changed.Prompt(t.Context(), "second", "second question")
	if err != nil || second.Turn.Status != harness.TurnCompleted || second.Output == nil ||
		second.Output.ContentType != "application/json" || len(client.calls) != 2 {
		t.Fatalf("second structured turn: %+v err=%v calls=%d", second.Turn, err, len(client.calls))
	}
	if second.Turn.ContextRef.Generation <= first.Turn.ContextRef.Generation {
		t.Fatalf("schema change reused cached Context generation: %+v %+v", first.Turn.ContextRef, second.Turn.ContextRef)
	}
	if client.calls[1].OutputContract.Name != "different_answer" {
		t.Fatal("changed frozen schema not sent")
	}
}

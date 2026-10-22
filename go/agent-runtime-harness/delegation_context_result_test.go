package harness

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestDelegationForwardsAnswerWithoutEmbeddingNativeSearchPages(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	child, err := runtime.Create(t.Context(), kernel.CreateRequest{ID: "native-child", Kind: agent.RunKind, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "delegation-context"}, Goal: "research", State: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(agent.Result{Content: "verified answer https://example.org/source", Citations: []string{"https://example.org/source"}, HostedToolCalls: []model.HostedToolCall{{ID: "native-receipt", ToolKey: "native.search", Status: "completed", Output: json.RawMessage(`{"page":"` + strings.Repeat("large web page ", 20_000) + `"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{runtime: runtime}
	forwarded, err := runner.delegationModelResult(t.Context(), handoff.Delegation{Status: handoff.StatusCompleted, ChildRunID: child.Run.ID, Result: raw})
	if err != nil {
		t.Fatal(err)
	}
	if len(forwarded) > 1024 || !strings.Contains(string(forwarded), "verified answer") || !strings.Contains(string(forwarded), "native-receipt") || !strings.Contains(string(forwarded), "https://example.org/source") {
		t.Fatalf("invalid supervisor projection: %d bytes", len(forwarded))
	}
	if !strings.Contains(string(raw), "large web page") {
		t.Fatal("exact child receipt was mutated")
	}
}

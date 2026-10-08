package harness

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestDirectAgentInvocationFreezesInitialToolsForRetry(t *testing.T) {
	t.Parallel()
	// Initial means visible in the first model request, not mandatory to
	// execute. The original Invocation input must carry this distinction
	// across a failed Agent start and Harness invocation retry.
	invocation, err := newDirectAgentInvocation(
		"turn-initial-tools", "request-initial-tools", "summarize",
		kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		kernel.ThreadRef{Kind: "conversation", ID: "thread-initial-tools"},
		nil,
		[]string{"harness.delegate_agent", "harness.read_context_artifact", "harness.delegate_agent"},
		time.Date(2026, time.October, 8, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	var recovered agentInvocationInput
	if err = json.Unmarshal(invocation.Input, &recovered); err != nil {
		t.Fatal(err)
	}
	if len(recovered.RequiredToolKeys) != 0 ||
		!slices.Equal(recovered.InitialToolKeys, []string{
			"harness.delegate_agent", "harness.read_context_artifact",
		}) {
		t.Fatalf("initial/required Tool semantics lost from durable Invocation: %+v", recovered)
	}
	if invocation.InputHash == "" || recovered.Goal != "summarize" {
		t.Fatalf("Invocation integrity fields missing: %+v", invocation)
	}
}

func TestContextDelegationInitialToolsRequireFrozenContextGrant(t *testing.T) {
	t.Parallel()
	const contextKey = ContextArtifactToolKey
	allowed := ConfigSnapshot{ToolKeys: []string{
		contextKey, "mcp.unrelated_docs", DelegationToolKey,
	}}
	turn := Turn{ContextCheckpointID: "checkpoint-1"}
	if actual := contextDelegationToolKeys(turn, allowed); !slices.Equal(actual, []string{contextKey}) {
		t.Fatalf("context read not isolated to the inherited first-party key: %+v", actual)
	}
	if actual := contextDelegationToolKeys(Turn{}, allowed); len(actual) != 0 {
		t.Fatalf("child inherited contextual Tool without context checkpoint: %+v", actual)
	}
	if actual := contextDelegationToolKeys(turn, ConfigSnapshot{
		ToolKeys: []string{"mcp.unrelated_docs", DelegationToolKey},
	}); len(actual) != 0 {
		t.Fatalf("child gained context read without parent authorization: %+v", actual)
	}
}

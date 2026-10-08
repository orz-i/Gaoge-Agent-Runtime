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

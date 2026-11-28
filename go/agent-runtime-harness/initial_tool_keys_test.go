package harness

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestAgentInvocationFreezesInitialToolsForRetry(t *testing.T) {
	t.Parallel()
	// Initial means visible in the first model request, not mandatory to
	// execute. The original Invocation input must carry this distinction
	// across a failed Agent start and Harness invocation retry.
	invocation, err := newAgentInvocation(
		"turn-initial-tools", "request-initial-tools", "summarize",
		kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		kernel.ThreadRef{Kind: "conversation", ID: "thread-initial-tools"},
		nil,
		[]string{"harness.delegate_agent", "harness.read_context_artifact", "harness.delegate_agent"},
		nil, time.Date(2026, time.October, 8, 12, 0, 0, 0, time.UTC),
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

// A successful initial admission must not turn a retry into an unbounded
// Root merely because the newly allocated native Agent Run has another ID.
// The original Invocation InputHash is the single durable source of the two
// per-Run spending permissions, distinct from AgentSpec's hard ceilings.
func TestRootInvocationFreezesOriginalAllowanceAndAutoPolicyForRetry(t *testing.T) {
	originalCall := &agent.CallAllowance{LLMCalls: 2, ToolCalls: 1}
	originalAuto := &agent.AutoAllowancePolicy{ModelStep: 1, MaxModelRenewals: 2, MaxNoProgressWindow: 2}
	originalCall.Renewal = originalAuto
	invocation, err := newAgentInvocation(
		"turn-budget-freeze", "original-request", "answer",
		kernel.ActorRef{TenantID: "tenant", ActorID: "owner"},
		kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		[]string{"tool"}, []string{"tool"}, originalCall,
		time.Date(2026, time.November, 23, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	if hashInvocationBytes(invocation.Input) != invocation.InputHash {
		t.Fatal("original spending grant was not bound to immutable Invocation InputHash")
	}
	originalCall.LLMCalls = 999
	originalAuto.MaxModelRenewals = 999
	var frozen agentInvocationInput
	if err = json.Unmarshal(invocation.Input, &frozen); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(frozen.CallAllowance, &agent.CallAllowance{LLMCalls: 2, ToolCalls: 1, Renewal: &agent.AutoAllowancePolicy{ModelStep: 1, MaxModelRenewals: 2, MaxNoProgressWindow: 2}}) {
		t.Fatalf("original per-Invocation permissions drifted: %+v", frozen)
	} // Old independent renewal data may not silently become a manual-only
	// allowance when a frozen Invocation is retried by the new writer.
	legacy := append(append([]byte(nil), invocation.Input[:len(invocation.Input)-1]...), []byte(`,"autoAllowance":{"modelStep":1,"maxModelRenewals":2,"maxNoProgressWindow":2}}`)...)
	var rejected agentInvocationInput
	if decodeTypedAuthorityJSON(legacy, &rejected) {
		t.Fatal("legacy separate policy gained retry authority")
	}

	config := ConfigSnapshot{AgentSpec: AgentSpec{Model: "model", Limits: agent.Limits{MaxLLMCalls: 8, MaxToolCalls: 8}}}
	retry := rootAgentStartRequest(config, invocation.ExecutionRefID, frozen.Actor, frozen.Thread,
		"next-attempt", frozen.Goal, frozen.InitialToolKeys, frozen.RequiredToolKeys)
	retry.CallAllowance = frozen.CallAllowance
	if !reflect.DeepEqual(retry.CallAllowance, frozen.CallAllowance) {
		t.Fatal("retry native Agent widened original execution allowance")
	}
	// Altering even one frozen grant changes the Invocation InputHash; same
	// physical Run IDs must not import a caller's new grant on retry.
	changed := frozen
	changed.CallAllowance = &agent.CallAllowance{LLMCalls: 8, ToolCalls: 8}
	payload, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	if hashInvocationBytes(payload) == invocation.InputHash {
		t.Fatal("expanded retry spending budget reused the original hash")
	}
	changed = frozen
	changed.CallAllowance = frozen.CallAllowance.Clone()
	changed.CallAllowance.Renewal = &agent.AutoAllowancePolicy{ModelStep: 4, MaxModelRenewals: 2, MaxNoProgressWindow: 2}
	payload, err = json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	if hashInvocationBytes(payload) == invocation.InputHash {
		t.Fatal("changed automatic budget policy reused original Invocation authority")
	}
}

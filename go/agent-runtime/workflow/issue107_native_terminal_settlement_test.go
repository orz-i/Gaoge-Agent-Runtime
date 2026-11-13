package workflow

import (
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestIssue107NativeTerminalUnknownEffectCannotReconcileCleanup(t *testing.T) {
	f := newLocalAgentProofFixture(t)
	if !f.original.OwnsKernelRuntime(f.runtime) {
		t.Fatal("must use an original native Agent without Host middleware")
	}
	f.child.Run.Status = kernel.RunStatusCancelled
	f.child.State = json.RawMessage(`{"budget":{"limits":{"maxLLMCalls":4,"maxToolCalls":4},"usage":{}},"toolDispatchID":"unsettled-tool","pendingCalls":[{"id":"call","toolKey":"tool","name":"act","arguments":{}}]}`)
	if f.runner.VerifyLocalChild(f.runtime, f.parent, f.relation, f.child, true) {
		t.Fatal("native Agent's Kernel CAS falsely settled an in-flight Tool")
	}
	f.child.State = json.RawMessage(`{"budget":{"limits":{"maxLLMCalls":4,"maxToolCalls":4},"usage":{}}}`)
	if !f.runner.VerifyLocalChild(f.runtime, f.parent, f.relation, f.child, true) {
		t.Fatal("genuinely settled native terminal child could not be reconciled")
	}
}

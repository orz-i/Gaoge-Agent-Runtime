package workflowhttp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

func TestPublicWorkflowTraceUsesBoundedErrorCodes(t *testing.T) {
	t.Parallel()
	const secret = "Bearer sk-PRIVATE-WORKFLOW-CODE"
	private := workflow.Trace{
		Activations: []workflow.ActivationTrace{{
			ID: "activation", ErrorCode: "tool.exception:" + secret,
		}},
		Effects: []workflow.EffectTrace{{ID: "effect", ErrorCode: "agent.model_failed"}},
		Compensations: []workflow.CompensationTrace{{
			ID: "compensation", ErrorCode: "provider." + secret,
		}},
	}
	public := publicWorkflowTrace(private)
	payload, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), secret) ||
		public.Activations[0].ErrorCode != "execution.failed" ||
		public.Effects[0].ErrorCode != "agent.model_failed" ||
		public.Compensations[0].ErrorCode != "execution.failed" {
		t.Fatalf("owner workflow trace leaked provider diagnostics: %s", payload)
	}
	if private.Activations[0].ErrorCode != "tool.exception:"+secret {
		t.Fatal("public trace mutated workflow-owned state")
	}
}

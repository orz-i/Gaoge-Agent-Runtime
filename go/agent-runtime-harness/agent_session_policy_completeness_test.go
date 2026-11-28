package harness_test

import (
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// Exercise the public composition boundary: policies must cover exactly the
// selected root and member grants, never a second independently supplied set.
func TestAgentSessionToolPoliciesCoverEveryFrozenGrant(t *testing.T) {
	for _, mode := range []string{"complete", "missing root policy", "missing member policy", "orphan member", "missing registration", "duplicate root/member", "frozen roster input"} {
		t.Run(mode, func(t *testing.T) {
			client := &agentSessionModel{}
			options := agentSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), client)
			addAgentSessionTool(&options, "root.read", new(int))
			options.MemberTools = []harness.SessionTool{{Registration: agentSessionTool("private.read", new(int)),
				Policy: harness.ToolPolicySnapshot{DefinitionVersion: "v1", ApprovalMode: "never", SideEffectLevel: "read"}}}
			options.Relations, _ = runrelation.New(memory.NewRunRelationStore(), nil)
			options.NamedAgents = []harness.NamedAgent{{MemberID: "specialist", MemberRevision: "v1",
				AgentSpec: harness.AgentSpec{Model: "specialist", ToolKeys: []string{"root.read", "private.read"}}}}
			switch mode {
			case "missing root policy":
				options.LocalTools[0].Policy = harness.ToolPolicySnapshot{}
			case "missing member policy":
				options.MemberTools[0].Policy = harness.ToolPolicySnapshot{}
			case "orphan member":
				options.NamedAgents[0].ToolKeys = []string{"root.read"}
			case "missing registration":
				options.MemberTools = nil
			case "duplicate root/member":
				options.MemberTools = append(options.MemberTools, options.LocalTools[0])
			case "frozen roster input":
				options.NamedAgents = nil
				options.Session.Config.DelegationPolicy.NamedAgents = []harness.NamedAgentSnapshot{{MemberID: "specialist", MemberRevision: "v1", AgentSpec: harness.AgentSpec{Model: "specialist"}}}
			}
			session, err := harness.OpenAgentSession(options)
			if mode != "complete" {
				if !errors.Is(err, harness.ErrInvalidRequest) || len(client.requests) != 0 {
					t.Fatalf("incomplete policy set admitted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			value, err := session.Prompt(t.Context(), "policy-set", "question")
			if err != nil || len(value.Config.ToolPolicies) != 3 || len(value.Config.ToolKeys) != 2 || len(value.Config.DelegationPolicy.LocalToolGrants) != 1 || value.Config.DelegationPolicy.MaxDepth != harness.DefaultExecutionPolicy().MaxDelegationDepth {
				t.Fatalf("root/private policy set or Runner defaults lost: %+v err=%v", value.Config, err)
			}
		})
	}
}

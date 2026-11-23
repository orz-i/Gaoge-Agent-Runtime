package harness

import (
	"errors"
	"testing"
)

// A frozen selected Tool with no matching policy must not be accepted even
// when another member-specific Tool contributes its own policy. The two
// allowlists have disjoint execution authority but one complete policy set.
func TestAgentSessionToolPoliciesCoverEveryFrozenGrant(t *testing.T) {
	config := ConfigSnapshot{

		DelegationPolicy: DelegationPolicySnapshot{MaxDepth: 1,
			LocalToolGrants: []DelegatedLocalToolGrant{{MemberID: "specialist", MemberRevision: "v1", ToolKey: "private.read"}}},
		ToolPolicies: []ToolPolicySnapshot{{Key: "private.read", DefinitionVersion: "v1", ExecutionMode: "local_dispatch", ApprovalMode: "never", SideEffectLevel: "read"}}, AgentSpec: AgentSpec{MaxModelOutputTokens: 128,
			ToolKeys: []string{"local.read"}},
	}
	if _, err := normalizeAgentSessionConfig(config); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing root Tool policy admitted: %v", err)
	}
	config.ToolPolicies = append(config.ToolPolicies, ToolPolicySnapshot{Key: "local.read", DefinitionVersion: "v1", ApprovalMode: "never", SideEffectLevel: "read"})
	if _, err := normalizeAgentSessionConfig(config); err != nil {
		t.Fatalf("complete root/member Tool policy rejected: %v", err)
	}
	config.ToolPolicies = config.ToolPolicies[1:]
	if _, err := normalizeAgentSessionConfig(config); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing member Tool policy admitted: %v", err)
	}
}

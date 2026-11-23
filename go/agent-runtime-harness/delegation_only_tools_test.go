package harness

import (
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
)

func TestDelegatedLocalToolGrantsAreBoundToMemberRevisionAndImmediateParent(t *testing.T) {
	const key = "product.evidence"
	cfg := ConfigSnapshot{ToolPolicies: []ToolPolicySnapshot{{Key: key, DefinitionVersion: "v1", ExecutionMode: "local_dispatch"}}, DelegationPolicy: DelegationPolicySnapshot{
		LocalToolGrants: []DelegatedLocalToolGrant{{MemberID: "research", MemberRevision: "2", ToolKey: key}, {MemberID: "editor", MemberRevision: "1", ToolKey: key}},
	}}
	sealed, err := SealConfigSnapshot("turn", cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cfg.DelegationPolicy.LocalToolGrants[0].MemberRevision = "changed"
	resealed, err := SealConfigSnapshot("turn", cfg, time.Now())
	if err != nil || resealed.ID == sealed.ID {
		t.Fatal("member revision was not frozen", err)
	}
	cloned := cloneConfigSnapshot(sealed)
	cloned.DelegationPolicy.LocalToolGrants[0].MemberID = "altered"
	if sealed.DelegationPolicy.LocalToolGrants[0].MemberID == "altered" {
		t.Fatal("mutable store snapshot changed original authority")
	}
	for _, tc := range []struct {
		name, member, revision   string
		root, inherited, allowed bool
	}{
		{"root assignment", "research", "2", true, false, true},
		{"other member", "unknown", "2", true, false, false},
		{"other revision", "research", "1", true, false, false},
		{"nested existing authority", "editor", "1", false, true, true},
		{"nested missing immediate authority", "editor", "1", false, false, false},
		{"nested unbound member", "unbound", "1", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := agent.View{}
			if tc.inherited {
				parent.ToolKeys = []string{key}
			}
			_, err := authorizedDelegatedLocalTools(sealed, parent, tc.root, DelegateRequest{MemberID: tc.member, MemberRevision: tc.revision, Agent: agent.StartRequest{ToolKeys: []string{key}}})
			if tc.allowed && err != nil || !tc.allowed && !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("unexpected delegation decision: %v", err)
			}
		})
	}
}

func TestInvalidDelegatedLocalToolGrantsCannotBeSealed(t *testing.T) {
	for _, tc := range []struct {
		name, key, mode, member, revision string
		root, repeat                      bool
	}{
		{"callable by root", "tool", "local_dispatch", "member", "1", true, false},
		{"reserved tool", "harness.delegate_agent", "local_dispatch", "member", "1", false, false},
		{"hosted is separately authorized", "tool", "provider_hosted", "member", "1", false, false},
		{"member omitted", "tool", "local_dispatch", "", "1", false, false},
		{"revision omitted", "tool", "local_dispatch", "member", "", false, false},
		{"duplicate authority", "tool", "local_dispatch", "member", "1", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grant := DelegatedLocalToolGrant{MemberID: tc.member, MemberRevision: tc.revision, ToolKey: tc.key}
			config := ConfigSnapshot{ToolPolicies: []ToolPolicySnapshot{{Key: tc.key, DefinitionVersion: "v1", ExecutionMode: tc.mode}}, DelegationPolicy: DelegationPolicySnapshot{LocalToolGrants: []DelegatedLocalToolGrant{grant}}}
			if tc.root {
				config.ToolKeys = []string{tc.key}
			}
			if tc.repeat {
				config.DelegationPolicy.LocalToolGrants = append(config.DelegationPolicy.LocalToolGrants, grant)
			}
			if _, err := SealConfigSnapshot("turn", config, time.Now()); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("ambiguous authority accepted: %v", err)
			}
		})
	}
}

// Root LocalTools with an omitted ExecutionMode still mean local_dispatch;
// selecting the same existing Root tool for a Named Agent is an explicit
// narrowing of the parent's frozen allowlist, not a new member-only grant.
func TestNamedAgentMayInheritAuthorizedRootToolWithDefaultLocalPolicy(t *testing.T) {
	const key = "root.lookup"
	config, err := SealConfigSnapshot("root-turn", ConfigSnapshot{
		ToolKeys: []string{key}, ToolPolicies: []ToolPolicySnapshot{{Key: key, DefinitionVersion: "v1", SideEffectLevel: "read", ApprovalMode: "never"}},
		DelegationPolicy: DelegationPolicySnapshot{MaxDepth: 1},
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	original := agent.View{ToolKeys: []string{key}}
	request := DelegateRequest{MemberID: "specialist", MemberRevision: "r1", Agent: agent.StartRequest{ToolKeys: []string{key}}}
	allowed, err := authorizedDelegatedLocalTools(config, original, true, request)
	if err != nil || len(allowed) != 1 || allowed[0] != key {
		t.Fatalf("Root-authorized Tool unexpectedly denied to selected child: %v keys=%+v", err, allowed)
	}
	if _, err = authorizedDelegatedLocalTools(config, agent.View{}, false, request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unbound nested child inherited an unrelated Root Tool: %v", err)
	}
}

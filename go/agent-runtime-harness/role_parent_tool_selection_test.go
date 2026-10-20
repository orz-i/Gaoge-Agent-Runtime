package harness

import (
	"slices"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestParentAssignedToolsStayWithinFrozenScopeAndExactHostedModel(t *testing.T) {
	t.Parallel()
	config := ConfigSnapshot{Model: "MiniMax-M3.1", ToolKeys: []string{"mcp.lookup", "minimax.web_search", DelegationToolKey},
		ToolPolicies: []ToolPolicySnapshot{
			{Key: "mcp.lookup", DefinitionVersion: "mcp-v1", ExecutionMode: "local_dispatch"},
			{Key: "minimax.web_search", DefinitionVersion: "hosted-v5", ExecutionMode: "provider_hosted"},
			{Key: DelegationToolKey, DefinitionVersion: "harness-v1"},
		}, Roles: []RoleSnapshot{{ID: "reviewer", Name: "Reviewer", Revision: 1}},
	}
	parent := agent.View{Model: "MiniMax-M3.1", ToolKeys: append([]string(nil), config.ToolKeys...)}
	local, hosted, err := delegatedParentToolSelection(config, parent, config.Roles[0], []string{"mcp.lookup", "minimax.web_search"})
	if err != nil || !slices.Equal(local, []string{"mcp.lookup"}) || len(hosted) != 1 || hosted[0].Key != "minimax.web_search" || hosted[0].DefinitionVersion != "hosted-v5" {
		t.Fatalf("frozen parent selection local=%v hosted=%v err=%v", local, hosted, err)
	}
	different := RoleSnapshot{ID: "different", Revision: 1, Model: "other-model"}
	if _, _, err = delegatedParentToolSelection(config, parent, different, []string{"minimax.web_search"}); err == nil {
		t.Fatal("provider-hosted parent Tool crossed the model identity boundary")
	}
	local, _, err = delegatedParentToolSelection(config, parent, different, []string{"mcp.lookup"})
	if err != nil || !slices.Equal(local, []string{"mcp.lookup"}) {
		t.Fatalf("model-independent local Tool was denied: %v %v", local, err)
	}
	for _, forbidden := range []string{DelegationToolKey, "harness.context_artifact", "mcp.unknown", "mcp.browser_inject"} {
		if _, _, err = delegatedParentToolSelection(config, parent, config.Roles[0], []string{forbidden}); err == nil {
			t.Fatalf("unapproved or internal Tool escaped frozen parent scope: %s", forbidden)
		}
	}
	narrower := agent.View{Model: parent.Model, ToolKeys: []string{DelegationToolKey}}
	if _, _, err = delegatedParentToolSelection(config, narrower, config.Roles[0], []string{"minimax.web_search"}); err == nil {
		t.Fatal("Tool frozen on another branch was inherited by this parent Run")
	}
	remote := RoleSnapshot{ID: "remote", Revision: 1, MemberID: "a2a:researcher"}
	if _, _, err = delegatedParentToolSelection(config, parent, remote, []string{"minimax.web_search"}); err == nil {
		t.Fatal("remote A2A member inherited parent's local Hosted credentials")
	}
	if _, _, err = delegatedParentToolSelection(config, parent, remote, []string{"mcp.lookup"}); err == nil {
		t.Fatal("remote A2A member inherited parent's local Tool executor")
	}
	// A malicious/incorrect host may annotate an internal control as local
	// dispatch. The parent has it, but may never re-grant it to a child.
	for _, key := range []string{DelegationToolKey, CapabilityInvocationToolKey,
		ContextArtifactToolKey, GroupChatHandoffToolKey, agent.ToolDiscoveryKey, "runtime.debug"} {
		forged := config
		forged.ToolKeys = append(append([]string(nil), config.ToolKeys...), key)
		forged.ToolPolicies = append(append([]ToolPolicySnapshot(nil), config.ToolPolicies...),
			ToolPolicySnapshot{Key: key, DefinitionVersion: "unsafe-v1", ExecutionMode: "local_dispatch"})
		forgedView := parent
		forgedView.ToolKeys = append(append([]string(nil), parent.ToolKeys...), key)
		if _, _, err = delegatedParentToolSelection(forged, forgedView, config.Roles[0], []string{key}); err == nil {
			t.Fatalf("internal control escaped as forged local tool grant: %s", key)
		}
	}
	withoutMode := config
	withoutMode.ToolPolicies = []ToolPolicySnapshot{{Key: "minimax.web_search", DefinitionVersion: "hosted-v5"}}
	if _, _, err = delegatedParentToolSelection(withoutMode, parent, config.Roles[0], []string{"minimax.web_search"}); err == nil {
		t.Fatal("untyped Hosted Tool was treated as authorized")
	}
}

func TestParentHostedGrantIntersectionAndVersionCollisionFailClosed(t *testing.T) {
	t.Parallel()
	role := []agent.HostedToolGrant{{Key: "minimax.web_search", DefinitionVersion: "v1"}}
	same := []agent.HostedToolGrant{{Key: "minimax.web_search", DefinitionVersion: "v1"}}
	grants, err := joinDelegatedHostedGrants(role, same)
	if err != nil || !slices.Equal(grants, role) {
		t.Fatalf("failed duplicate grant dedupe: %+v %v", grants, err)
	}
	if _, err = joinDelegatedHostedGrants(role, []agent.HostedToolGrant{{Key: "minimax.web_search", DefinitionVersion: "v2"}}); err == nil {
		t.Fatal("version drift unexpectedly upgraded a role grant")
	}
}

func TestDelegationModelAdvertisesOnlyAssignableVisibleTools(t *testing.T) {
	t.Parallel()
	config := ConfigSnapshot{Roles: []RoleSnapshot{{ID: "inherits", Name: "General", Revision: 1}, {ID: "fixed", Name: "Fixed", Revision: 1, Model: "other-model"}},
		ToolPolicies: []ToolPolicySnapshot{
			{Key: "mcp.lookup", DefinitionVersion: "v1", ExecutionMode: "local_dispatch"},
			{Key: "minimax.web_search", DefinitionVersion: "v2", ExecutionMode: "provider_hosted"},
			{Key: "browser.javascript", DefinitionVersion: "v3", ExecutionMode: "client_dispatch"},
		},
	}
	request := model.Request{Model: "MiniMax-M3.1", Tools: []tools.Definition{{Key: "mcp.lookup"}, {Key: "browser.javascript"}},
		HostedTools: []model.HostedTool{{Key: "minimax.web_search", DefinitionVersion: "v2"}},
	}
	got := modelDelegationToolCandidates(request, config)
	if !slices.Equal(got, []string{"mcp.lookup", "minimax.web_search"}) {
		t.Fatalf("bad model-facing candidates: %+v", got)
	}
	// Even if an erroneous host labels an internal control as local and
	// exposes it to the parent model, the delegation schema must not offer it
	// as a child grant; the execution guard already rejects the same keys.
	for _, control := range []string{DelegationToolKey, CapabilityInvocationToolKey, ContextArtifactToolKey,
		GroupChatHandoffToolKey, agent.ToolDiscoveryKey, "runtime.internal"} {
		config.ToolPolicies = append(config.ToolPolicies, ToolPolicySnapshot{
			Key: control, DefinitionVersion: "forged-v1", ExecutionMode: "local_dispatch",
		})
		request.Tools = append(request.Tools, tools.Definition{Key: control})
	}
	got = modelDelegationToolCandidates(request, config)
	if !slices.Equal(got, []string{"mcp.lookup", "minimax.web_search"}) {
		t.Fatalf("parent's private control keys were advertised for delegation: %+v", got)
	}
	config.Roles = config.Roles[1:]
	got = modelDelegationToolCandidates(request, config)
	if !slices.Equal(got, []string{"mcp.lookup"}) {
		t.Fatalf("different-model role was offered parent Hosted grants: %+v", got)
	}
}

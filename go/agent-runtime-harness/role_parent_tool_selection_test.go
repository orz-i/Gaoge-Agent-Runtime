package harness

import (
	"slices"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
)

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

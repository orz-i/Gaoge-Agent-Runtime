package harness

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
)

func TestHostSelectedAgentRequestCannotInventPhysicalAuthority(t *testing.T) {
	cases := []struct {
		name   string
		change func(*DelegateRequest)
	}{
		{"run-id", func(d *DelegateRequest) { d.Agent.ID = "forged" }},
		{"actor", func(d *DelegateRequest) { d.Agent.Actor.TenantID = "foreign" }},
		{"goal", func(d *DelegateRequest) { d.Agent.Goal = "forged" }},
		{"parent", func(d *DelegateRequest) { d.Agent.ParentRunID = "foreign" }},
		{"request-id", func(d *DelegateRequest) { d.Agent.RequestID = "foreign" }},
		{"unbound-initial-tool", func(d *DelegateRequest) { d.Agent.InitialToolKeys = []string{"unknown"} }},
		{"unversioned-hosted", func(d *DelegateRequest) { d.Agent.HostedToolGrants = []agent.HostedToolGrant{{Key: "hosted.search"}} }},
		{"local-hosted-collision", func(d *DelegateRequest) {
			d.Agent.ToolKeys = []string{"search"}
			d.Agent.HostedToolGrants = []agent.HostedToolGrant{{Key: "search", DefinitionVersion: "v1"}}
		}},
		{"negative-allowance", func(d *DelegateRequest) { d.Agent.Limits.MaxLLMCalls = -1 }},
		{"ambiguous-options", func(d *DelegateRequest) {
			d.Agent.ModelOptions = json.RawMessage(`{"reasoning":{"effort":"low"},"reasoning":{"effort":"high"}}`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := DelegateRequest{MemberID: "researcher", Goal: "research"}
			tc.change(&request)
			if _, err := normalizeDelegateRequest(request); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("authority mutation was accepted: %v", err)
			}
		})
	}
	selected, err := normalizeDelegateRequest(DelegateRequest{MemberID: "researcher", MemberRevision: "v2", Goal: "research",
		Agent: agent.StartRequest{Model: "specialist", Instructions: "host-chosen", HostedToolGrants: []agent.HostedToolGrant{{Key: "hosted.search", DefinitionVersion: "v1"}}},
	})
	if err != nil || selected.MemberRevision != "v2" || selected.Agent.Model != "specialist" {
		t.Fatalf("Host-selected Agent config rejected: %+v %v", selected, err)
	}
	var modelInput DelegateRequest
	if err = json.Unmarshal([]byte(`{"memberID":"researcher","goal":"research","agent":{"model":"untrusted","toolKeys":["hosted.search"]}}`), &modelInput); err != nil || modelInput.Agent.Model != "" || len(modelInput.Agent.ToolKeys) != 0 {
		t.Fatalf("model-writable execution authority: %+v %v", modelInput, err)
	}
}

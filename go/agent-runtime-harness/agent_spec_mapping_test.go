package harness

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// The initial root and a later RetryInvocation must not maintain separate
// capability projections. Only their Run ID and RequestID can differ; both
// must receive the same immutable Model, output contract, local/hosted Tools,
// Skills-derived Context, and enforceable limits.
func TestAgentSpecRootFirstAndRetryMapTheSameFrozenCapabilities(t *testing.T) {
	const schema = `{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`
	selected := AgentSpec{
		Model: "selected-model", ModelOptions: json.RawMessage(`{"temperature":0.1}`),
		Instructions:         "root skill manifest belongs to Context",
		OutputContract:       &model.OutputContract{Name: "answer", Schema: json.RawMessage(schema)},
		MaxModelOutputTokens: 128, ToolKeys: []string{"local.read"},
		InitialToolKeys:  []string{"local.read"},
		HostedToolGrants: []agent.HostedToolGrant{{Key: "provider.search", DefinitionVersion: "v3"}},
		Limits:           agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 6, MaxHostedToolCalls: 2},
	}
	frozen, err := normalizeAgentSpec(selected)
	if err != nil {
		t.Fatal(err)
	}
	config := ConfigSnapshot{AgentSpec: frozen}
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
	first := rootAgentStartRequest(config, "first", actor, thread, "request", "answer", []string{"provider.search"}, []string{"local.read"})
	retry := rootAgentStartRequest(config, "retry", actor, thread, "request-retry", "answer", []string{"provider.search"}, []string{"local.read"})
	first.ID, first.RequestID = "", ""
	retry.ID, retry.RequestID = "", ""
	if !reflect.DeepEqual(first, retry) || !slices.Equal(first.ToolKeys, []string{"local.read", "provider.search"}) ||
		!slices.Equal(first.InitialToolKeys, []string{"local.read", "provider.search"}) ||
		!slices.Equal(first.HostedToolGrants, frozen.HostedToolGrants) ||
		!reflect.DeepEqual(first.OutputContract, frozen.OutputContract) {
		t.Fatalf("root attempt diverged from one frozen AgentSpec: first=%+v retry=%+v", first, retry)
	}
	if first.Instructions != "" || first.MaxModelOutputTokens != 128 || first.Model != "selected-model" {
		t.Fatal("root ownership of Context/ceiling/model changed")
	}
	// Mapping never lets a model-facing copy mutate the original Host seal.
	first.OutputContract.Schema[0] = 'X'
	first.ModelOptions[0] = 'X'
	first.HostedToolGrants[0].DefinitionVersion = "forged"
	first.ToolKeys[0] = "foreign"
	if string(frozen.OutputContract.Schema) != schema || !bytes.Equal(frozen.ModelOptions, []byte(`{"temperature":0.1}`)) ||
		frozen.HostedToolGrants[0].DefinitionVersion != "v3" || frozen.ToolKeys[0] != "local.read" {
		t.Fatal("Root request shared mutable Host-selected execution authority")
	}
}

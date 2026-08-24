package a2a

import (
	"encoding/json"
	"strings"
	"testing"

	a2asdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

const (
	testTopologyEndpoint = "https://private-agent.example/a2a"
	testTopologyTenant   = "tenant-sensitive-value"
)

func TestTopologyProviderProjectsRemoteShadowRunWithoutSecrets(t *testing.T) {
	t.Parallel()
	snapshot := newTopologyShadow(t)
	topology, matched, err := (TopologyProvider{}).Topology(t.Context(), snapshot)
	if err != nil || !matched || len(topology.Nodes) != 3 || len(topology.Edges) != 2 {
		t.Fatalf("topology=%#v matched=%v err=%v", topology, matched, err)
	}
	assertFederatedTopologySafe(t, topology)
}

func newTopologyShadow(t *testing.T) kernel.Snapshot {
	t.Helper()
	runtime := newShadowRuntime(t)
	remote := &shadowRemote{task: workingTask()}
	runner, err := NewShadowRunner(ShadowDependencies{
		Runtime: runtime, Client: remote,
		Discovery: Discovery{Descriptor: RemoteAgentDescriptor{
			Name: "remote-agent", PreferredURL: testTopologyEndpoint,
			ProtocolVersion: ProtocolVersion, ProtocolBinding: string(a2asdk.TransportProtocolHTTPJSON), Tenant: testTopologyTenant,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.StartRun(t.Context(), agent.StartRequest{
		ID: "a2a-topology", Goal: "remote goal",
		Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
	})
	if err != nil || snapshot.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("shadow snapshot=%#v err=%v", snapshot.Run, err)
	}
	return snapshot
}

func assertFederatedTopologySafe(t *testing.T, topology any) {
	t.Helper()
	raw, err := json.Marshal(topology)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{"private-agent.example", testTopologyTenant, "https://"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("topology leaked transport secret %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "remote-agent") || !strings.Contains(text, "remote-1") || !strings.Contains(text, ProtocolVersion) {
		t.Fatalf("topology missing remote identity facts: %s", text)
	}
}

func TestTopologyProviderIgnoresNonA2ARuns(t *testing.T) {
	t.Parallel()
	topology, matched, err := (TopologyProvider{}).Topology(t.Context(), kernel.Snapshot{Run: kernel.Run{Kind: agent.RunKind}})
	if err != nil || matched || topology.SchemaVersion != 0 {
		t.Fatalf("topology=%#v matched=%v err=%v", topology, matched, err)
	}
}

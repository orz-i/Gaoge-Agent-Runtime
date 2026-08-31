package workbench_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workbench"
)

const (
	staticRootNodeID = "node:root"
	staticFirstNode  = "node:a"
	staticLastNode   = "node:z"
)

func TestQueryNormalizesAndClonesTopology(t *testing.T) {
	t.Parallel()
	snapshot := baseSnapshot()
	provider := staticTopologyProvider{
		name: "topology.static",
		value: workbench.TopologyV1{
			SchemaVersion: workbench.TopologySchemaVersion,
			RootNodeID:    staticRootNodeID,
			Revision:      snapshot.Run.Revision,
			Nodes: []workbench.TopologyNode{
				{ID: staticLastNode, Kind: "step", Label: "Z", Status: "pending"},
				{ID: staticRootNodeID, Kind: "workflow", Label: "Root", Status: "running", Data: json.RawMessage(`{ "b": 2, "a": 1 }`)},
				{ID: staticFirstNode, Kind: "step", Label: "A", Status: "completed"},
			},
			Edges: []workbench.TopologyEdge{
				{ID: "edge:z", Source: staticFirstNode, Target: staticLastNode, Kind: workbench.EdgeSequence},
				{ID: "edge:a", Source: staticRootNodeID, Target: staticFirstNode, Kind: workbench.EdgeSequence},
			},
		},
	}
	query := mustQuery(t, snapshot, []workbench.Registration{{Topology: provider}})

	detail, err := query.Get(context.Background(), snapshot.Run.ID)
	if err != nil {
		t.Fatalf("get topology detail: %v", err)
	}
	assertNormalizedTopology(t, detail.Topology)
	firstHash := detail.Topology.Hash
	detail.Topology.Nodes[0].Label = "mutated"
	detail.Topology.Nodes[1].Data[0] = '['

	second, err := query.Get(context.Background(), snapshot.Run.ID)
	if err != nil {
		t.Fatalf("get topology detail again: %v", err)
	}
	if second.Topology == nil || second.Topology.Hash != firstHash || second.Topology.Nodes[0].Label != "A" {
		t.Fatalf("topology was not stable and isolated: %#v", second.Topology)
	}
}

func TestQuerySerializesEmptyWorkbenchCollectionsAsArrays(t *testing.T) {
	t.Parallel()
	snapshot := baseSnapshot()
	query := mustQuery(t, snapshot, []workbench.Registration{{
		Topology: staticTopologyProvider{
			name: "topology.single-node",
			value: workbench.TopologyV1{
				SchemaVersion: workbench.TopologySchemaVersion,
				RootNodeID:    staticRootNodeID,
				Revision:      snapshot.Run.Revision,
				Nodes: []workbench.TopologyNode{{
					ID: staticRootNodeID, Kind: "agent", Label: "Agent", Status: "completed",
				}},
			},
		},
	}})

	detail, err := query.Get(context.Background(), snapshot.Run.ID)
	if err != nil {
		t.Fatalf("get single-node topology detail: %v", err)
	}
	if detail.Sections == nil || detail.Topology == nil || detail.Topology.Edges == nil {
		t.Fatalf("empty collections must remain non-nil: %#v", detail)
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal detail: %v", err)
	}
	if !strings.Contains(string(encoded), `"sections":[]`) ||
		!strings.Contains(string(encoded), `"edges":[]`) {
		t.Fatalf("empty collections were not serialized as arrays: %s", encoded)
	}
}

func assertNormalizedTopology(t *testing.T, topology *workbench.TopologyV1) {
	t.Helper()
	if topology == nil || topology.Hash == "" || topology.SchemaVersion != workbench.TopologySchemaVersion {
		t.Fatalf("missing normalized topology: %#v", topology)
	}
	assertNormalizedTopologyOrder(t, topology)
	if string(topology.Nodes[1].Data) != `{"a":1,"b":2}` {
		t.Fatalf("node data is not canonical: %s", topology.Nodes[1].Data)
	}
}

func assertNormalizedTopologyOrder(t *testing.T, topology *workbench.TopologyV1) {
	t.Helper()
	if len(topology.Nodes) != 3 || topology.Nodes[0].ID != staticFirstNode ||
		topology.Nodes[2].ID != staticLastNode {
		t.Fatalf("unstable node order: %#v", topology.Nodes)
	}
	if len(topology.Edges) != 2 || topology.Edges[0].ID != "edge:a" || topology.Edges[1].ID != "edge:z" {
		t.Fatalf("unstable edge order: %#v", topology.Edges)
	}
}

func TestQueryKeepsBaseDetailWhenTopologyProviderFails(t *testing.T) {
	t.Parallel()
	snapshot := baseSnapshot()
	query := mustQuery(t, snapshot, []workbench.Registration{
		{Provider: staticProvider{name: "context", content: json.RawMessage(`{"ok":true}`)}},
		{Topology: staticTopologyProvider{name: "topology.broken", err: errProviderFailed}},
	})

	detail, err := query.Get(context.Background(), snapshot.Run.ID)
	if err != nil {
		t.Fatalf("topology provider failure broke base detail: %v", err)
	}
	if detail.Topology != nil || len(detail.Sections) != 1 || !detail.Sections[0].Available {
		t.Fatalf("unexpected degraded detail: %#v", detail)
	}
	if len(detail.Diagnostics) != 1 || detail.Diagnostics[0].Operation != "topology" ||
		detail.Diagnostics[0].Code != "provider_error" {
		t.Fatalf("missing topology diagnostic: %#v", detail.Diagnostics)
	}
}

func TestQueryRejectsDuplicateTopologyProviderNames(t *testing.T) {
	t.Parallel()
	snapshot := baseSnapshot()
	_, err := workbench.NewQuery(fakeRunSource{snapshot: snapshot}, fakeRunSource{snapshot: snapshot}, []workbench.Registration{
		{Topology: staticTopologyProvider{name: "topology.same"}},
		{Topology: staticTopologyProvider{name: " topology.same "}},
	})
	if !errors.Is(err, workbench.ErrInvalidInput) {
		t.Fatalf("expected duplicate topology provider rejection, got %v", err)
	}
}

type staticTopologyProvider struct {
	name      string
	value     workbench.TopologyV1
	available bool
	err       error
}

func (provider staticTopologyProvider) Name() string { return provider.name }

func (provider staticTopologyProvider) Topology(
	context.Context,
	kernel.Snapshot,
) (workbench.TopologyV1, bool, error) {
	available := provider.available || len(provider.value.Nodes) > 0
	return provider.value, available, provider.err
}

package topologyadapter

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimebudget "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workbench"
)

const (
	AgentTopologyProviderName = "topology.agent"
)

type agentTopologyProvider struct{}

// NewAgentTopologyProvider projects the Agent root without inventing
// Tool or sub-agent nodes that are not durable topology facts.
func NewAgentTopologyProvider() workbench.TopologyProvider { return agentTopologyProvider{} }

func (agentTopologyProvider) Name() string { return AgentTopologyProviderName }

func (agentTopologyProvider) Topology(
	_ context.Context,
	snapshot kernel.Snapshot,
) (workbench.TopologyV1, bool, error) {
	if snapshot.Run.Kind != agent.RunKind {
		return workbench.TopologyV1{}, false, nil
	}
	view, err := agent.ViewState(snapshot)
	if err != nil {
		return workbench.TopologyV1{}, true, err
	}
	data, err := topologyData(struct {
		Model    string                 `json:"model,omitempty"`
		ToolKeys []string               `json:"toolKeys"`
		Budget   runtimebudget.Snapshot `json:"budget"`
	}{view.Model, view.ToolKeys, view.Budget})
	if err != nil {
		return workbench.TopologyV1{}, true, err
	}
	root := topologyRunNode(snapshot, "agent", "Agent", data)
	return topologyEnvelope(snapshot, root, []workbench.TopologyNode{root}, nil), true, nil
}

func topologyEnvelope(
	snapshot kernel.Snapshot,
	root workbench.TopologyNode,
	nodes []workbench.TopologyNode,
	edges []workbench.TopologyEdge,
) workbench.TopologyV1 {
	return workbench.TopologyV1{
		SchemaVersion: workbench.TopologySchemaVersion, RootNodeID: root.ID,
		Revision: snapshot.Run.Revision, Nodes: nodes, Edges: edges,
	}
}

func topologyRunNode(
	snapshot kernel.Snapshot,
	kind string,
	label string,
	data json.RawMessage,
) workbench.TopologyNode {
	return workbench.TopologyNode{
		ID: topologyNodeID("run", snapshot.Run.ID), Kind: kind, Label: label,
		Status: string(snapshot.Run.Status), RunID: snapshot.Run.ID, Data: data,
	}
}

func topologyNodeID(kind string, value string) string {
	return strings.TrimSpace(kind) + ":" + strings.TrimSpace(value)
}

func topologyData(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, errors.Join(workbench.ErrInvalidInput, err)
	}
	return encoded, nil
}

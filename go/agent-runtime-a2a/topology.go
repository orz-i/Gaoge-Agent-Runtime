package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/workbench"
)

var ErrInvalidTopologyProjection = errors.New("invalid A2A topology projection")

// TopologyProvider projects only persisted local shadow state. It never calls
// a remote A2A endpoint and therefore cannot become a routing or discovery
// control plane.
type TopologyProvider struct{}

// Name returns the stable Workbench provider identity.
func (TopologyProvider) Name() string { return "a2a.remote" }

// Topology implements workbench.TopologyProvider for local a2a.remote Runs.
func (TopologyProvider) Topology(_ context.Context, snapshot kernel.Snapshot) (workbench.TopologyV1, bool, error) {
	if snapshot.Run.Kind != RunKind {
		return workbench.TopologyV1{}, false, nil
	}
	state, err := decodeTopologyState(snapshot.State)
	if err != nil {
		return workbench.TopologyV1{}, true, err
	}
	rootID := snapshot.Run.ID
	agentID := rootID + ":remote-agent"
	nodes := []workbench.TopologyNode{
		{
			ID: rootID, Kind: "a2a.shadow_run", Label: snapshot.Run.Goal,
			Status: string(snapshot.Run.Status), RunID: rootID,
			Data: topologyData(map[string]string{"protocolVersion": state.ProtocolVersion}),
		},
		{
			ID: agentID, Kind: "a2a.remote_agent", Label: state.RemoteName,
			Status: "remote", Data: topologyData(map[string]string{"name": state.RemoteName, "protocolVersion": state.ProtocolVersion}),
		},
	}
	edges := []workbench.TopologyEdge{{
		ID: rootID + ":delegates", Source: rootID, Target: agentID, Kind: workbench.EdgeDelegation,
	}}
	if state.RemoteTaskID != "" {
		taskID := rootID + ":remote-task"
		nodes = append(nodes, workbench.TopologyNode{
			ID: taskID, Kind: "a2a.remote_task", Label: state.RemoteTaskID, Status: state.RemoteState,
			Data: topologyData(map[string]string{
				"taskID": state.RemoteTaskID, "contextID": state.RemoteContextID, "state": state.RemoteState,
			}),
		})
		edges = append(edges, workbench.TopologyEdge{
			ID: rootID + ":tracks", Source: agentID, Target: taskID, Kind: workbench.EdgeConsumes,
		})
	}
	return workbench.TopologyV1{
		SchemaVersion: workbench.TopologySchemaVersion, RootNodeID: rootID,
		Revision: snapshot.Run.Revision, Nodes: nodes, Edges: edges,
	}, true, nil
}

func decodeTopologyState(raw json.RawMessage) (shadowState, error) {
	var state shadowState
	if json.Unmarshal(raw, &state) != nil || strings.TrimSpace(state.RemoteName) == "" ||
		strings.TrimSpace(state.RemoteURL) == "" || strings.TrimSpace(state.ProtocolVersion) == "" {
		return shadowState{}, ErrInvalidTopologyProjection
	}
	return state, nil
}

func topologyData(values map[string]string) json.RawMessage {
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil
	}
	return encoded
}

var _ workbench.TopologyProvider = TopologyProvider{}

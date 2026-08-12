package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
)

const TopologySchemaVersion = 1

const topologyOperation = "topology"

const (
	EdgeSequence   = "sequence"
	EdgeDependency = "dependency"
	EdgeDelegation = "delegation"
	EdgeHandoff    = "handoff"
	EdgeProduces   = "produces"
	EdgeConsumes   = "consumes"
)

// TopologyProvider contributes one optional, feature-owned graph projection.
type TopologyProvider interface {
	Name() string
	Topology(context.Context, kernel.Snapshot) (TopologyV1, bool, error)
}

// TopologyNode is one stable, read-only Runtime or orchestration node.
type TopologyNode struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Label   string          `json:"label"`
	Status  string          `json:"status"`
	RunID   string          `json:"runID,omitempty"`
	GroupID string          `json:"groupID,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// TopologyEdge is one truthful relationship between stable topology nodes.
type TopologyEdge struct {
	ID     string          `json:"id"`
	Source string          `json:"source"`
	Target string          `json:"target"`
	Kind   string          `json:"kind"`
	Status string          `json:"status,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// TopologyV1 is the canonical Workbench graph contract. Providers never set
// Hash; Query normalizes and hashes the graph after deterministic sorting.
type TopologyV1 struct {
	SchemaVersion int            `json:"schemaVersion"`
	RootNodeID    string         `json:"rootNodeID"`
	Revision      uint64         `json:"revision"`
	Hash          string         `json:"hash"`
	Nodes         []TopologyNode `json:"nodes"`
	Edges         []TopologyEdge `json:"edges"`
}

func freezeRegistrations(
	registrations []Registration,
) ([]Provider, []TopologyProvider, error) {
	providers := make([]Provider, 0, len(registrations))
	topologyProviders := make([]TopologyProvider, 0, len(registrations))
	seen := make(map[string]struct{}, len(registrations))
	for _, registration := range registrations {
		if registration.Provider == nil && registration.Topology == nil {
			return nil, nil, ErrInvalidInput
		}
		if registration.Provider != nil {
			if err := registerProviderName(seen, registration.Provider.Name()); err != nil {
				return nil, nil, err
			}
			providers = append(providers, registration.Provider)
		}
		if registration.Topology != nil {
			if err := registerProviderName(seen, registration.Topology.Name()); err != nil {
				return nil, nil, err
			}
			topologyProviders = append(topologyProviders, registration.Topology)
		}
	}
	sort.Slice(providers, func(left int, right int) bool {
		return strings.TrimSpace(providers[left].Name()) < strings.TrimSpace(providers[right].Name())
	})
	sort.Slice(topologyProviders, func(left int, right int) bool {
		return strings.TrimSpace(topologyProviders[left].Name()) < strings.TrimSpace(topologyProviders[right].Name())
	})
	return providers, topologyProviders, nil
}

func registerProviderName(seen map[string]struct{}, value string) error {
	name := strings.TrimSpace(value)
	if name == "" {
		return ErrInvalidInput
	}
	if _, duplicate := seen[name]; duplicate {
		return ErrInvalidInput
	}
	seen[name] = struct{}{}
	return nil
}

func (query *Query) loadTopology(
	ctx context.Context,
	snapshot kernel.Snapshot,
) (*TopologyV1, []Diagnostic) {
	var selected *TopologyV1
	diagnostics := make([]Diagnostic, 0)
	for _, provider := range query.topologyProviders {
		name := strings.TrimSpace(provider.Name())
		candidate, available, err := provider.Topology(ctx, cloneSnapshot(snapshot))
		if diagnostic := topologyProviderDiagnostic(name, err); diagnostic != nil {
			diagnostics = append(diagnostics, *diagnostic)
			continue
		}
		if !available {
			continue
		}
		normalized, normalizeErr := normalizeTopology(candidate, snapshot)
		if normalizeErr != nil {
			diagnostics = append(diagnostics, Diagnostic{
				Provider: name, Operation: topologyOperation, Code: "invalid_content", Message: normalizeErr.Error(),
			})
			continue
		}
		if selected != nil {
			diagnostics = append(diagnostics, Diagnostic{
				Provider: name, Operation: topologyOperation, Code: "conflict",
				Message: "multiple topology providers matched one run",
			})
			continue
		}
		selected = &normalized
	}
	return selected, diagnostics
}

func topologyProviderDiagnostic(name string, providerErr error) *Diagnostic {
	if providerErr == nil {
		return nil
	}
	code := diagnosticProviderError
	if errors.Is(providerErr, ErrUnavailable) {
		code = diagnosticUnavailable
	}
	return &Diagnostic{
		Provider: name, Operation: topologyOperation, Code: code, Message: truncate(providerErr.Error(), 512),
	}
}

func normalizeTopology(value TopologyV1, snapshot kernel.Snapshot) (TopologyV1, error) {
	value.RootNodeID = strings.TrimSpace(value.RootNodeID)
	value.Hash = ""
	if value.SchemaVersion != TopologySchemaVersion || value.RootNodeID == "" ||
		value.Revision == 0 || value.Revision != snapshot.Run.Revision || len(value.Nodes) == 0 {
		return TopologyV1{}, ErrInvalidInput
	}
	nodes, nodeIDs, err := normalizeTopologyNodes(value.Nodes)
	if err != nil {
		return TopologyV1{}, err
	}
	if _, exists := nodeIDs[value.RootNodeID]; !exists {
		return TopologyV1{}, ErrInvalidInput
	}
	edges, err := normalizeTopologyEdges(value.Edges, nodeIDs)
	if err != nil {
		return TopologyV1{}, err
	}
	value.Nodes, value.Edges = nodes, edges
	encoded, err := json.Marshal(value)
	if err != nil {
		return TopologyV1{}, errors.Join(ErrInvalidInput, err)
	}
	value.Hash = hashBytes(encoded)
	return value, nil
}

func normalizeTopologyNodes(values []TopologyNode) ([]TopologyNode, map[string]struct{}, error) {
	nodes := append([]TopologyNode(nil), values...)
	ids := make(map[string]struct{}, len(nodes))
	for index := range nodes {
		node := &nodes[index]
		node.ID, node.Kind = strings.TrimSpace(node.ID), strings.TrimSpace(node.Kind)
		node.Label, node.Status = strings.TrimSpace(node.Label), strings.TrimSpace(node.Status)
		node.RunID, node.GroupID = strings.TrimSpace(node.RunID), strings.TrimSpace(node.GroupID)
		if node.ID == "" || node.Kind == "" || node.Label == "" || node.Status == "" {
			return nil, nil, ErrInvalidInput
		}
		if _, duplicate := ids[node.ID]; duplicate {
			return nil, nil, ErrInvalidInput
		}
		ids[node.ID] = struct{}{}
		canonical, err := optionalCanonicalJSON(node.Data)
		if err != nil {
			return nil, nil, err
		}
		node.Data = canonical
	}
	sort.Slice(nodes, func(left int, right int) bool { return nodes[left].ID < nodes[right].ID })
	return nodes, ids, nil
}

func normalizeTopologyEdges(
	values []TopologyEdge,
	nodeIDs map[string]struct{},
) ([]TopologyEdge, error) {
	edges := append(make([]TopologyEdge, 0, len(values)), values...)
	ids := make(map[string]struct{}, len(edges))
	for index := range edges {
		normalized, err := normalizeTopologyEdge(edges[index], nodeIDs, ids)
		if err != nil {
			return nil, err
		}
		edges[index] = normalized
	}
	sort.Slice(edges, func(left int, right int) bool { return edges[left].ID < edges[right].ID })
	return edges, nil
}

func normalizeTopologyEdge(
	edge TopologyEdge,
	nodeIDs map[string]struct{},
	seen map[string]struct{},
) (TopologyEdge, error) {
	edge.ID, edge.Source = strings.TrimSpace(edge.ID), strings.TrimSpace(edge.Source)
	edge.Target, edge.Kind = strings.TrimSpace(edge.Target), strings.TrimSpace(edge.Kind)
	edge.Status = strings.TrimSpace(edge.Status)
	_, sourceExists := nodeIDs[edge.Source]
	_, targetExists := nodeIDs[edge.Target]
	if edge.ID == "" || edge.Source == "" || edge.Target == "" || edge.Source == edge.Target ||
		edge.Kind == "" || !sourceExists || !targetExists {
		return TopologyEdge{}, ErrInvalidInput
	}
	if _, duplicate := seen[edge.ID]; duplicate {
		return TopologyEdge{}, ErrInvalidInput
	}
	seen[edge.ID] = struct{}{}
	canonical, err := optionalCanonicalJSON(edge.Data)
	if err != nil {
		return TopologyEdge{}, err
	}
	edge.Data = canonical
	return edge, nil
}

func optionalCanonicalJSON(value json.RawMessage) (json.RawMessage, error) {
	if len(value) == 0 {
		return nil, nil
	}
	return canonicalJSON(value)
}

func cloneTopology(value *TopologyV1) *TopologyV1 {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Nodes = append([]TopologyNode(nil), value.Nodes...)
	for index := range cloned.Nodes {
		cloned.Nodes[index].Data = append(json.RawMessage(nil), value.Nodes[index].Data...)
	}
	cloned.Edges = append(make([]TopologyEdge, 0, len(value.Edges)), value.Edges...)
	for index := range cloned.Edges {
		cloned.Edges[index].Data = append(json.RawMessage(nil), value.Edges[index].Data...)
	}
	return &cloned
}

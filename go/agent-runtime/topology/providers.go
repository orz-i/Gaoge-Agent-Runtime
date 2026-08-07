package topology

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/planexecute"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/team"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/workbench"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/workflow"
)

const (
	AgentTopologyProviderName    = "topology.agent"
	PlanTopologyProviderName     = "topology.planexecute"
	TeamTopologyProviderName     = "topology.team"
	WorkflowTopologyProviderName = "topology.workflow"
)

type agentTopologyProvider struct{}

type childTopologyProvider struct {
	name      string
	kind      kernel.RunKind
	runs      workbench.RunSource
	relations RelationSource
	project   func(context.Context, childTopologyProvider, kernel.Snapshot) (workbench.TopologyV1, error)
}

// RelationSource exposes immutable parent/child ownership to graph projectors.
type RelationSource interface {
	ListChildren(context.Context, string) ([]runrelation.Relation, error)
}

// NewAgentTopologyProvider projects the direct Agent root without inventing
// Tool or sub-agent nodes that are not durable topology facts.
func NewAgentTopologyProvider() workbench.TopologyProvider { return agentTopologyProvider{} }

// NewPlanTopologyProvider projects Plan steps and their related Child Runs.
func NewPlanTopologyProvider(
	runs workbench.RunSource,
	relations RelationSource,
) workbench.TopologyProvider {
	return childTopologyProvider{
		name: PlanTopologyProviderName, kind: kernel.RunKindPlanExecute,
		runs: runs, relations: relations, project: projectPlanTopology,
	}
}

// NewTeamTopologyProvider projects fixed members, delegations and the Join.
func NewTeamTopologyProvider(
	runs workbench.RunSource,
	relations RelationSource,
) workbench.TopologyProvider {
	return childTopologyProvider{
		name: TeamTopologyProviderName, kind: kernel.RunKindTeam,
		runs: runs, relations: relations, project: projectTeamTopology,
	}
}

// NewWorkflowTopologyProvider projects only the compiled linear node order.
func NewWorkflowTopologyProvider(
	runs workbench.RunSource,
	relations RelationSource,
) workbench.TopologyProvider {
	return childTopologyProvider{
		name: WorkflowTopologyProviderName, kind: kernel.RunKindWorkflow,
		runs: runs, relations: relations, project: projectWorkflowTopology,
	}
}

func (agentTopologyProvider) Name() string { return AgentTopologyProviderName }

func (agentTopologyProvider) Topology(
	_ context.Context,
	snapshot kernel.Snapshot,
) (workbench.TopologyV1, bool, error) {
	if snapshot.Run.Kind != kernel.RunKindAgent {
		return workbench.TopologyV1{}, false, nil
	}
	view, err := agent.ViewState(snapshot)
	if err != nil {
		return workbench.TopologyV1{}, true, err
	}
	data, err := topologyData(struct {
		Model     string   `json:"model,omitempty"`
		ToolKeys  []string `json:"toolKeys"`
		LLMCalls  int      `json:"llmCalls"`
		ToolCalls int      `json:"toolCalls"`
	}{view.Model, view.ToolKeys, view.LLMCalls, view.ToolCalls})
	if err != nil {
		return workbench.TopologyV1{}, true, err
	}
	root := topologyRunNode(snapshot, "agent", "Agent", data)
	return topologyEnvelope(snapshot, root, []workbench.TopologyNode{root}, nil), true, nil
}

func (provider childTopologyProvider) Name() string { return provider.name }

func (provider childTopologyProvider) Topology(
	ctx context.Context,
	snapshot kernel.Snapshot,
) (workbench.TopologyV1, bool, error) {
	if snapshot.Run.Kind != provider.kind {
		return workbench.TopologyV1{}, false, nil
	}
	if provider.project == nil {
		return workbench.TopologyV1{}, true, workbench.ErrUnavailable
	}
	topology, err := provider.project(ctx, provider, snapshot)
	return topology, true, err
}

func projectPlanTopology(
	ctx context.Context,
	provider childTopologyProvider,
	snapshot kernel.Snapshot,
) (workbench.TopologyV1, error) {
	view, err := planexecute.ViewState(snapshot)
	if err != nil {
		return workbench.TopologyV1{}, err
	}
	relations, err := provider.relationsByOwner(ctx, snapshot.Run.ID, runrelation.KindPlanStep)
	if err != nil {
		return workbench.TopologyV1{}, err
	}
	rootData, err := topologyData(struct {
		PlanID         string                     `json:"planID,omitempty"`
		PlanStatus     planexecute.PlanStatus     `json:"planStatus,omitempty"`
		ApprovalPolicy planexecute.ApprovalPolicy `json:"approvalPolicy"`
		NextStep       int                        `json:"nextStep"`
	}{view.Plan.ID, view.Plan.Status, view.ApprovalPolicy, view.NextStep})
	if err != nil {
		return workbench.TopologyV1{}, err
	}
	root := topologyRunNode(snapshot, "plan_execute", topologyLabel(view.Plan.Summary, "Plan & Execute"), rootData)
	nodes := []workbench.TopologyNode{root}
	edges := make([]workbench.TopologyEdge, 0, len(view.Plan.Steps))
	previousID := root.ID
	for index, step := range view.Plan.Steps {
		node, nodeErr := provider.planStepNode(ctx, step, index, view.Plan.ID, relations[step.ID])
		if nodeErr != nil {
			return workbench.TopologyV1{}, nodeErr
		}
		nodes = append(nodes, node)
		edges = append(edges, topologyEdge(workbench.EdgeSequence, previousID, node.ID, node.Status))
		previousID = node.ID
	}
	return topologyEnvelope(snapshot, root, nodes, edges), nil
}

func (provider childTopologyProvider) planStepNode(
	ctx context.Context,
	step planexecute.Step,
	index int,
	planID string,
	relation runrelation.Relation,
) (workbench.TopologyNode, error) {
	runID, status, err := provider.childRunFacts(ctx, relation, step.ChildRunID, string(step.Status))
	if err != nil {
		return workbench.TopologyNode{}, err
	}
	data, err := topologyData(struct {
		Index int    `json:"index"`
		Goal  string `json:"goal"`
	}{index, step.Goal})
	if err != nil {
		return workbench.TopologyNode{}, err
	}
	return workbench.TopologyNode{
		ID: topologyNodeID("plan-step", step.ID), Kind: "plan_step",
		Label: topologyLabel(step.Title, fmt.Sprintf("Step %d", index+1)), Status: status,
		RunID: runID, GroupID: planID, Data: data,
	}, nil
}

func projectTeamTopology(
	ctx context.Context,
	provider childTopologyProvider,
	snapshot kernel.Snapshot,
) (workbench.TopologyV1, error) {
	view, err := team.ViewState(snapshot)
	if err != nil {
		return workbench.TopologyV1{}, err
	}
	relations, err := provider.relationsByOwner(ctx, snapshot.Run.ID, runrelation.KindTeamMember)
	if err != nil {
		return workbench.TopologyV1{}, err
	}
	rootData, err := topologyData(struct {
		Mode team.ExecutionMode `json:"mode"`
	}{view.Mode})
	if err != nil {
		return workbench.TopologyV1{}, err
	}
	root := topologyRunNode(snapshot, "team", "Agent Team", rootData)
	join, err := teamJoinNode(snapshot.Run.ID, view.Join)
	if err != nil {
		return workbench.TopologyV1{}, err
	}
	nodes := []workbench.TopologyNode{root, join}
	edges := make([]workbench.TopologyEdge, 0, len(view.Members)*2)
	for index := range view.Members {
		member := view.Members[index]
		node, nodeErr := provider.teamMemberNode(ctx, member, snapshot.Run.ID, relations[member.Member.ID])
		if nodeErr != nil {
			return workbench.TopologyV1{}, nodeErr
		}
		nodes = append(nodes, node)
		edges = append(edges,
			topologyEdge(workbench.EdgeDelegation, root.ID, node.ID, node.Status),
			topologyEdge(workbench.EdgeHandoff, node.ID, join.ID, node.Status),
		)
	}
	return topologyEnvelope(snapshot, root, nodes, edges), nil
}

func (provider childTopologyProvider) teamMemberNode(
	ctx context.Context,
	member team.MemberState,
	groupID string,
	relation runrelation.Relation,
) (workbench.TopologyNode, error) {
	runID, status, err := provider.childRunFacts(
		ctx, relation, member.Delegation.ChildRunID, string(member.Delegation.Status),
	)
	if err != nil {
		return workbench.TopologyNode{}, err
	}
	data, err := topologyData(struct {
		DelegationID string `json:"delegationID"`
		Goal         string `json:"goal"`
	}{member.Delegation.ID, member.Member.Goal})
	if err != nil {
		return workbench.TopologyNode{}, err
	}
	return workbench.TopologyNode{
		ID: topologyNodeID("team-member", member.Member.ID), Kind: "team_member",
		Label: topologyLabel(member.Member.ID, "Team member"), Status: status,
		RunID: runID, GroupID: groupID, Data: data,
	}, nil
}

func teamJoinNode(runID string, join handoff.Join) (workbench.TopologyNode, error) {
	data, err := topologyData(join)
	if err != nil {
		return workbench.TopologyNode{}, err
	}
	return workbench.TopologyNode{
		ID: topologyNodeID("team-join", runID), Kind: "team_join",
		Label: "Join " + string(join.Mode), Status: string(join.Status), GroupID: runID, Data: data,
	}, nil
}

func projectWorkflowTopology(
	ctx context.Context,
	provider childTopologyProvider,
	snapshot kernel.Snapshot,
) (workbench.TopologyV1, error) {
	view, err := workflow.ViewState(snapshot)
	if err != nil {
		return workbench.TopologyV1{}, err
	}
	relations, err := provider.relationsByOwner(ctx, snapshot.Run.ID, runrelation.KindWorkflowEffect)
	if err != nil {
		return workbench.TopologyV1{}, err
	}
	rootData, err := topologyData(struct {
		DefinitionID   string          `json:"definitionID"`
		DefinitionHash string          `json:"definitionHash"`
		CurrentNode    int             `json:"currentNode"`
		Budget         workflow.Budget `json:"budget"`
	}{view.Definition.ID, view.Definition.Hash, view.CurrentNode, view.Budget})
	if err != nil {
		return workbench.TopologyV1{}, err
	}
	root := topologyRunNode(snapshot, "workflow", topologyLabel(view.Definition.Name, "Workflow"), rootData)
	nodes := []workbench.TopologyNode{root}
	edges := make([]workbench.TopologyEdge, 0, len(view.Definition.Nodes))
	previousID := root.ID
	for index, definitionNode := range view.Definition.Nodes {
		node, nodeErr := provider.workflowNode(ctx, view, definitionNode, index, relations[definitionNode.ID])
		if nodeErr != nil {
			return workbench.TopologyV1{}, nodeErr
		}
		nodes = append(nodes, node)
		edges = append(edges, topologyEdge(workbench.EdgeSequence, previousID, node.ID, node.Status))
		previousID = node.ID
	}
	return topologyEnvelope(snapshot, root, nodes, edges), nil
}

func (provider childTopologyProvider) workflowNode(
	ctx context.Context,
	view workflow.View,
	node workflow.Node,
	index int,
	relation runrelation.Relation,
) (workbench.TopologyNode, error) {
	status, runID, data, err := workflowNodeFacts(view, node, index)
	if err != nil {
		return workbench.TopologyNode{}, err
	}
	if node.Type == workflow.NodeEffect {
		runID, status, err = provider.childRunFacts(ctx, relation, runID, status)
		if err != nil {
			return workbench.TopologyNode{}, err
		}
	}
	return workbench.TopologyNode{
		ID: topologyNodeID("workflow-node", node.ID), Kind: "workflow_" + string(node.Type),
		Label: workflowNodeLabel(node), Status: status, RunID: runID,
		GroupID: view.Definition.ID, Data: data,
	}, nil
}

func workflowNodeFacts(
	view workflow.View,
	node workflow.Node,
	index int,
) (string, string, json.RawMessage, error) {
	activation, status := workflowActivationFacts(view.Activations, node.ID)
	runID, effectStatus := workflowEffectFacts(view.Effects, node)
	status = topologyLabel(effectStatus, status)
	status = topologyLabel(workflowWaitStatus(view.Waits, node), status)
	if activation == nil && index < view.CurrentNode {
		status = string(workflow.ActivationCompleted)
	}
	data, err := topologyData(struct {
		Index      int                  `json:"index"`
		Type       workflow.NodeType    `json:"type"`
		Activation *workflow.Activation `json:"activation,omitempty"`
	}{index, node.Type, activation})
	return status, runID, data, err
}

func workflowActivationFacts(
	activations []workflow.Activation,
	nodeID string,
) (*workflow.Activation, string) {
	status := "pending"
	var activation *workflow.Activation
	for index := range activations {
		if activations[index].NodeID == nodeID {
			item := activations[index]
			activation = &item
			status = string(item.Status)
		}
	}
	return activation, status
}

func workflowEffectFacts(effects []workflow.Effect, node workflow.Node) (string, string) {
	if node.Type != workflow.NodeEffect {
		return "", ""
	}
	runID, status := "", ""
	for index := range effects {
		if effects[index].NodeID == node.ID {
			runID = effects[index].ChildRunID
			status = string(effects[index].Status)
		}
	}
	return runID, status
}

func workflowWaitStatus(waits []workflow.Wait, node workflow.Node) string {
	if node.Type != workflow.NodeWait {
		return ""
	}
	status := ""
	for index := range waits {
		if waits[index].NodeID == node.ID {
			status = string(waits[index].Status)
		}
	}
	return status
}

func workflowNodeLabel(node workflow.Node) string {
	switch node.Type {
	case workflow.NodeEffect:
		return topologyLabel(node.Effect.Kind, node.ID)
	case workflow.NodeWait:
		return topologyLabel(node.Wait.Kind, node.ID)
	default:
		return topologyLabel(node.ID, "Return")
	}
}

func (provider childTopologyProvider) relationsByOwner(
	ctx context.Context,
	parentRunID string,
	kind runrelation.Kind,
) (map[string]runrelation.Relation, error) {
	result := make(map[string]runrelation.Relation)
	if provider.relations == nil {
		return result, nil
	}
	relations, err := provider.relations.ListChildren(ctx, parentRunID)
	if err != nil {
		return nil, err
	}
	for _, relation := range relations {
		if relation.Kind == kind {
			result[relation.OwnerNodeID] = relation
		}
	}
	return result, nil
}

func (provider childTopologyProvider) childRunFacts(
	ctx context.Context,
	relation runrelation.Relation,
	fallbackRunID string,
	fallbackStatus string,
) (string, string, error) {
	runID := strings.TrimSpace(fallbackRunID)
	if relation.ChildRunID != "" {
		if runID != "" && runID != relation.ChildRunID {
			return "", "", workbench.ErrInvalidInput
		}
		runID = relation.ChildRunID
	}
	status := strings.TrimSpace(fallbackStatus)
	if provider.runs == nil || runID == "" {
		return runID, status, nil
	}
	child, err := provider.runs.Load(ctx, runID)
	if errors.Is(err, kernel.ErrNotFound) {
		return runID, status, nil
	}
	if err != nil {
		return "", "", err
	}
	return runID, string(child.Run.Status), nil
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

func topologyEdge(kind string, source string, target string, status string) workbench.TopologyEdge {
	return workbench.TopologyEdge{
		ID:     "edge:" + kind + ":" + source + ":" + target,
		Source: source, Target: target, Kind: kind, Status: status,
	}
}

func topologyLabel(value string, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

func topologyData(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, errors.Join(workbench.ErrInvalidInput, err)
	}
	return encoded, nil
}

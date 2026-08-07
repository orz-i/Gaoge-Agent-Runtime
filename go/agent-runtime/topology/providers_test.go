package topology_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/planexecute"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/team"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/topology"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/workbench"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/workflow"
)

const (
	testTopologyAgentKind    = "agent"
	testPlanChildOne         = "child-plan-1"
	testPlanChildTwo         = "child-plan-2"
	testTeamWriterID         = "writer"
	testTeamEditorID         = "editor"
	testTeamWriterChild      = "child-team-writer"
	testTeamEditorChild      = "child-team-editor"
	testWorkflowDraftNode    = "draft"
	testWorkflowApprovalNode = "approval"
	testWorkflowDraftChild   = "child-workflow-draft"
	testWorkflowApprovalWait = "wait-approval"
	testTopologyWriteGoal    = "Write"
)

func TestFeatureTopologyProvidersProjectDurableFacts(t *testing.T) {
	t.Parallel()
	fixtures := topologyFixtures(t)
	query, err := workbench.NewQuery(fixtures.runs, topologyRegistrations(fixtures.runs, fixtures.relations))
	if err != nil {
		t.Fatalf("create topology query: %v", err)
	}

	t.Run("agent", func(t *testing.T) {
		assertAgentTopology(t, getTopologyDetail(t, query, fixtures.agentRunID).Topology)
	})

	t.Run("plan", func(t *testing.T) {
		assertPlanTopology(t, getTopologyDetail(t, query, fixtures.planRunID).Topology)
	})

	t.Run("team", func(t *testing.T) {
		assertTeamTopology(t, getTopologyDetail(t, query, fixtures.teamRunID).Topology)
	})

	t.Run("linear workflow", func(t *testing.T) {
		assertLinearWorkflowTopology(t, getTopologyDetail(t, query, fixtures.workflowRunID).Topology)
	})
}

func assertAgentTopology(t *testing.T, topology *workbench.TopologyV1) {
	t.Helper()
	if len(topology.Nodes) != 1 || len(topology.Edges) != 0 || topology.Nodes[0].Kind != testTopologyAgentKind {
		t.Fatalf("agent topology invented non-durable nodes: %#v", topology)
	}
}

func assertPlanTopology(t *testing.T, topology *workbench.TopologyV1) {
	t.Helper()
	assertTopologyShape(t, topology, 3, 2)
	step := topologyNodeByID(t, topology, "plan-step:step-1")
	if step.RunID != testPlanChildOne || step.Status != string(kernel.RunStatusCompleted) {
		t.Fatalf("plan child facts not projected: %#v", step)
	}
	assertEdge(t, topology, "run:plan-1", "plan-step:step-1", workbench.EdgeSequence)
	assertEdge(t, topology, "plan-step:step-1", "plan-step:step-2", workbench.EdgeSequence)
}

func assertTeamTopology(t *testing.T, topology *workbench.TopologyV1) {
	t.Helper()
	assertTopologyShape(t, topology, 4, 4)
	member := topologyNodeByID(t, topology, "team-member:"+testTeamWriterID)
	if member.RunID != testTeamWriterChild || member.Status != string(kernel.RunStatusRunning) {
		t.Fatalf("team child facts not projected: %#v", member)
	}
	assertEdge(t, topology, "run:team-1", member.ID, workbench.EdgeDelegation)
	assertEdge(t, topology, member.ID, "team-join:team-1", workbench.EdgeHandoff)
}

func assertLinearWorkflowTopology(t *testing.T, topology *workbench.TopologyV1) {
	t.Helper()
	assertTopologyShape(t, topology, 4, 3)
	for _, edge := range topology.Edges {
		if edge.Kind != workbench.EdgeSequence {
			t.Fatalf("linear workflow invented a DAG edge: %#v", edge)
		}
	}
	effect := topologyNodeByID(t, topology, "workflow-node:"+testWorkflowDraftNode)
	if effect.RunID != testWorkflowDraftChild || effect.Status != string(kernel.RunStatusRunning) {
		t.Fatalf("workflow child facts not projected: %#v", effect)
	}
	assertEdge(t, topology, "run:workflow-1", "workflow-node:"+testWorkflowDraftNode, workbench.EdgeSequence)
	assertEdge(t, topology, "workflow-node:"+testWorkflowDraftNode, "workflow-node:"+testWorkflowApprovalNode, workbench.EdgeSequence)
	assertEdge(t, topology, "workflow-node:"+testWorkflowApprovalNode, "workflow-node:publish", workbench.EdgeSequence)
}

func topologyRegistrations(runs workbench.RunSource, relations topology.RelationSource) []workbench.Registration {
	return []workbench.Registration{
		{Topology: topology.NewAgentTopologyProvider()},
		{Topology: topology.NewPlanTopologyProvider(runs, relations)},
		{Topology: topology.NewTeamTopologyProvider(runs, relations)},
		{Topology: topology.NewWorkflowTopologyProvider(runs, relations)},
	}
}

func getTopologyDetail(t *testing.T, query *workbench.Query, runID string) workbench.Detail {
	t.Helper()
	detail, err := query.Get(context.Background(), runID)
	if err != nil {
		t.Fatalf("get topology for %s: %v", runID, err)
	}
	if detail.Topology == nil || detail.Topology.Hash == "" || len(detail.Diagnostics) != 0 {
		t.Fatalf("invalid topology detail for %s: %#v", runID, detail)
	}
	return detail
}

func assertTopologyShape(t *testing.T, topology *workbench.TopologyV1, nodes int, edges int) {
	t.Helper()
	if topology == nil || len(topology.Nodes) != nodes || len(topology.Edges) != edges {
		t.Fatalf("unexpected topology shape: %#v", topology)
	}
}

func topologyNodeByID(t *testing.T, topology *workbench.TopologyV1, id string) workbench.TopologyNode {
	t.Helper()
	for _, node := range topology.Nodes {
		if node.ID == id {
			return node
		}
	}
	t.Fatalf("topology node %s not found: %#v", id, topology.Nodes)
	return workbench.TopologyNode{}
}

func assertEdge(t *testing.T, topology *workbench.TopologyV1, source string, target string, kind string) {
	t.Helper()
	for _, edge := range topology.Edges {
		if edge.Source == source && edge.Target == target && edge.Kind == kind {
			return
		}
	}
	t.Fatalf("topology edge %s -> %s (%s) not found: %#v", source, target, kind, topology.Edges)
}

type topologyFixtureSet struct {
	runs          topologyRunSource
	relations     topologyRelationSource
	agentRunID    string
	planRunID     string
	teamRunID     string
	workflowRunID string
}

func topologyFixtures(t *testing.T) topologyFixtureSet {
	t.Helper()
	agentSnapshot := topologySnapshot(
		"agent-1", kernel.RunKindAgent, kernel.RunStatusCompleted, 2,
		json.RawMessage(`{"messages":[{"role":"user","content":"draft"}],"model":"terra","toolKeys":[],"limits":{"maxLLMCalls":8,"maxToolCalls":16},"llmCalls":1,"toolCalls":0}`),
	)
	planSnapshot := topologySnapshot(
		"plan-1", kernel.RunKindPlanExecute, kernel.RunStatusRunning, 4,
		mustStateJSON(t, planexecute.View{
			ApprovalPolicy: planexecute.ApprovalAuto,
			Plan: planexecute.Plan{ID: "plan-spec", Revision: 1, Status: planexecute.PlanRunning, Summary: "Delivery plan", Steps: []planexecute.Step{
				{ID: "step-1", Title: "Research", Goal: "Research", Status: planexecute.StepCompleted, ChildRunID: testPlanChildOne},
				{ID: "step-2", Title: testTopologyWriteGoal, Goal: testTopologyWriteGoal, Status: planexecute.StepRunning, ChildRunID: testPlanChildTwo},
			}},
			NextStep: 1,
		}),
	)
	teamSnapshot := topologySnapshot(
		"team-1", kernel.RunKindTeam, kernel.RunStatusRunning, 3,
		mustStateJSON(t, team.View{
			Mode: team.ExecutionParallel,
			Members: []team.MemberState{
				{Member: team.Member{ID: testTeamWriterID, Goal: testTopologyWriteGoal}, Delegation: handoff.Delegation{
					ID: "delegation-writer", MemberID: testTeamWriterID, ChildRunID: testTeamWriterChild, Goal: testTopologyWriteGoal, Status: handoff.StatusRunning,
				}},
				{Member: team.Member{ID: testTeamEditorID, Goal: "Edit"}, Delegation: handoff.Delegation{
					ID: "delegation-editor", MemberID: testTeamEditorID, ChildRunID: testTeamEditorChild, Goal: "Edit", Status: handoff.StatusQueued,
				}},
			},
			Join: handoff.Join{Mode: handoff.JoinAll, FailurePolicy: handoff.FailureCollect, Status: handoff.JoinPending, Pending: 2},
		}),
	)
	workflowSnapshot := topologyWorkflowSnapshot(t)
	runs := topologyRunSource{snapshots: map[string]kernel.Snapshot{
		agentSnapshot.Run.ID:    agentSnapshot,
		planSnapshot.Run.ID:     planSnapshot,
		teamSnapshot.Run.ID:     teamSnapshot,
		workflowSnapshot.Run.ID: workflowSnapshot,
		testPlanChildOne:        topologySnapshot(testPlanChildOne, kernel.RunKindAgent, kernel.RunStatusCompleted, 2, agentSnapshot.State),
		testPlanChildTwo:        topologySnapshot(testPlanChildTwo, kernel.RunKindAgent, kernel.RunStatusRunning, 1, agentSnapshot.State),
		testTeamWriterChild:     topologySnapshot(testTeamWriterChild, kernel.RunKindAgent, kernel.RunStatusRunning, 1, agentSnapshot.State),
		testTeamEditorChild:     topologySnapshot(testTeamEditorChild, kernel.RunKindAgent, kernel.RunStatusRunning, 1, agentSnapshot.State),
		testWorkflowDraftChild:  topologySnapshot(testWorkflowDraftChild, kernel.RunKindAgent, kernel.RunStatusRunning, 1, agentSnapshot.State),
	}}
	relations := topologyRelationSource{children: map[string][]runrelation.Relation{
		planSnapshot.Run.ID: {
			{ParentRunID: planSnapshot.Run.ID, ChildRunID: testPlanChildOne, Kind: runrelation.KindPlanStep, OwnerNodeID: "step-1"},
			{ParentRunID: planSnapshot.Run.ID, ChildRunID: testPlanChildTwo, Kind: runrelation.KindPlanStep, OwnerNodeID: "step-2"},
		},
		teamSnapshot.Run.ID: {
			{ParentRunID: teamSnapshot.Run.ID, ChildRunID: testTeamWriterChild, Kind: runrelation.KindTeamMember, OwnerNodeID: testTeamWriterID},
			{ParentRunID: teamSnapshot.Run.ID, ChildRunID: testTeamEditorChild, Kind: runrelation.KindTeamMember, OwnerNodeID: testTeamEditorID},
		},
		workflowSnapshot.Run.ID: {
			{ParentRunID: workflowSnapshot.Run.ID, ChildRunID: testWorkflowDraftChild, Kind: runrelation.KindWorkflowEffect, OwnerNodeID: testWorkflowDraftNode},
		},
	}}
	return topologyFixtureSet{
		runs: runs, relations: relations, agentRunID: agentSnapshot.Run.ID,
		planRunID: planSnapshot.Run.ID, teamRunID: teamSnapshot.Run.ID, workflowRunID: workflowSnapshot.Run.ID,
	}
}

func topologyWorkflowSnapshot(t *testing.T) kernel.Snapshot {
	t.Helper()
	definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
		ID: "workflow-spec", Revision: 1, Name: "Editorial workflow",
		InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`),
		Nodes: []workflow.Node{
			{ID: testWorkflowDraftNode, Type: workflow.NodeEffect, Effect: &workflow.EffectNode{Kind: testTopologyAgentKind, Input: json.RawMessage(`{"goal":"draft"}`)}},
			{ID: testWorkflowApprovalNode, Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: testWorkflowApprovalNode, Payload: json.RawMessage(`{"prompt":"approve"}`)}},
			{ID: "publish", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`{"published":true}`)}},
		},
	})
	if err != nil {
		t.Fatalf("compile workflow fixture: %v", err)
	}
	state := workflow.View{
		Definition: definition, Input: json.RawMessage(`{"topic":"runtime"}`), CurrentNode: 1,
		Activations: []workflow.Activation{
			{ID: "activation-draft", NodeID: testWorkflowDraftNode, NodeIndex: 0, Status: workflow.ActivationWaiting, Attempt: 1, EffectID: "effect-draft"},
			{ID: "activation-approval", NodeID: testWorkflowApprovalNode, NodeIndex: 1, Status: workflow.ActivationWaiting, Attempt: 1, WaitID: testWorkflowApprovalWait},
		},
		Effects: []workflow.Effect{
			{ID: "effect-draft", ActivationID: "activation-draft", NodeID: testWorkflowDraftNode, Kind: testTopologyAgentKind, Input: json.RawMessage(`{"goal":"draft"}`), Status: workflow.EffectPending, ChildRunID: testWorkflowDraftChild},
		},
		Waits: []workflow.Wait{
			{ID: testWorkflowApprovalWait, ActivationID: "activation-approval", NodeID: testWorkflowApprovalNode, Kind: testWorkflowApprovalNode, Payload: json.RawMessage(`{"prompt":"approve"}`), Status: workflow.WaitPending},
		},
		CurrentWaitID: testWorkflowApprovalWait, Budget: workflow.Budget{NodeActivations: 2, Effects: 1, Segments: 2, StateBytes: 1024},
	}
	return topologySnapshot("workflow-1", kernel.RunKindWorkflow, kernel.RunStatusWaitingInput, 5, mustStateJSON(t, state))
}

func mustStateJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal topology fixture: %v", err)
	}
	return encoded
}

func topologySnapshot(
	id string,
	kind kernel.RunKind,
	status kernel.RunStatus,
	revision uint64,
	state json.RawMessage,
) kernel.Snapshot {
	createdAt := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	return kernel.Snapshot{Run: kernel.Run{
		ID: id, Kind: kind, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "conversation-1"}, Goal: "test topology",
		Status: status, Revision: revision, CreatedAt: createdAt, UpdatedAt: createdAt,
	}, State: append(json.RawMessage(nil), state...)}
}

type topologyRunSource struct {
	snapshots map[string]kernel.Snapshot
}

func (source topologyRunSource) Load(_ context.Context, runID string) (kernel.Snapshot, error) {
	snapshot, exists := source.snapshots[runID]
	if !exists {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	return snapshot, nil
}

type topologyRelationSource struct {
	children map[string][]runrelation.Relation
}

func (source topologyRelationSource) ListChildren(
	_ context.Context,
	parentRunID string,
) ([]runrelation.Relation, error) {
	return append([]runrelation.Relation(nil), source.children[parentRunID]...), nil
}

package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/groupchat"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// These Host adapters are fully legal public Feature implementations, but
// deliberately do not forward the original runner's optional local ownership
// proof. Their physical executions must NEVER be cancelled from an unrelated
// Harness Kernel or cleared by an empty Harness relation index.
type pr82OpaqueTeamFeature struct{ child *team.Runner }

func (f pr82OpaqueTeamFeature) StartRun(ctx context.Context, req team.StartRequest) (kernel.Snapshot, error) {
	return f.child.StartRun(ctx, req)
}
func (f pr82OpaqueTeamFeature) Resume(ctx context.Context, runID string, revision uint64) (kernel.Snapshot, error) {
	return f.child.Resume(ctx, runID, revision)
}

// An intentional Host wrapper may still obtain local cancellation authority
// by forwarding the exact native runner's two independent attestations.
type pr82VerifiedTeamFeature struct{ pr82OpaqueTeamFeature }

func (f pr82VerifiedTeamFeature) OwnsKernelRuntime(rt *kernel.Runtime) bool {
	return f.child.OwnsKernelRuntime(rt)
}
func (f pr82VerifiedTeamFeature) RecordsRelationsTo(recorder runrelation.Recorder) bool {
	return f.child.RecordsRelationsTo(recorder)
}
func (f pr82VerifiedTeamFeature) VerifyLocalChild(
	rt *kernel.Runtime, parent kernel.Snapshot, relation runrelation.Relation,
	child kernel.Snapshot, control bool,
) bool {
	return f.child.VerifyLocalChild(rt, parent, relation, child, control)
}

type pr82OpaqueWorkflowFeature struct{ child *workflow.Runner }

func (f pr82OpaqueWorkflowFeature) StartRun(ctx context.Context, req workflow.StartRequest) (kernel.Snapshot, error) {
	return f.child.StartRun(ctx, req)
}
func (f pr82OpaqueWorkflowFeature) Resume(ctx context.Context, runID string, revision uint64) (kernel.Snapshot, error) {
	return f.child.Resume(ctx, runID, revision)
}
func (f pr82OpaqueWorkflowFeature) ResolveWait(ctx context.Context, runID string, rev uint64, value json.RawMessage) (kernel.Snapshot, error) {
	return f.child.ResolveWait(ctx, runID, rev, value)
}

func TestPR82OpaqueTeamCannotTurnMissingRecorderProofIntoCancellationSuccess(t *testing.T) {
	rt := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	relations := newFeatureInvocationRelations(t)
	children := &pr82OrphanMemberChildren{runs: map[string]kernel.Snapshot{}}
	coord, err := handoff.New(children)
	if err != nil {
		t.Fatal(err)
	}
	// The native Team records to nil, while Harness reads a different index.
	feature, err := team.NewRunner(team.Dependencies{Runtime: rt, Handoffs: coord})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: rt, Agent: loadingFeatureAgent{runtime: rt}, Store: store,
		Clock: featureInvocationClock{}, Relations: relations,
		Teams: pr82OpaqueTeamFeature{child: feature},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runner.StartTeamTurn(t.Context(), harness.TeamTurnRequest{
		StartRequest: harness.StartRequest{
			HostThread: harness.HostRef{Kind: testThreadKind, ID: "opaque-cancel-thread"},
			HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "opaque-cancel-turn"},
			Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
			Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "opaque-cancel-thread"},
			RequestID:  "opaque-cancel", Goal: "verify physical cleanup",
			Config: harness.ConfigSnapshot{Model: "fixture-model"},
		},
		Mode:    team.ExecutionSequential,
		Members: []team.Member{{ID: "worker", Goal: "continues"}},
	})
	if err != nil && !errors.Is(err, team.ErrMemberPending) {
		t.Fatal(err)
	}
	root, ok := harness.TopLevelInvocation(started)
	if !ok {
		t.Fatal("missing root Invocation")
	}
	parent, err := rt.Load(t.Context(), root.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := team.ViewState(parent)
	if err != nil || len(view.Members) != 1 {
		t.Fatalf("members=%+v err=%v", view.Members, err)
	}
	childID := view.Members[0].Delegation.ChildRunID
	edges, err := relations.ListChildren(t.Context(), root.ExecutionRefID)
	if err != nil || len(edges) != 0 {
		t.Fatalf("expected empty Harness index, got %+v err=%v", edges, err)
	}
	_, cancelErr := runner.Cancel(t.Context(), started.Turn.ID, "stop child")
	if !errors.Is(cancelErr, harness.ErrRuntimeCancellationUnverified) {
		t.Fatalf("unverified opaque route: %v", cancelErr)
	}
	remote, err := children.LoadRun(t.Context(), childID)
	if err != nil || remote.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("unknown remote child was changed: status=%s err=%v", remote.Run.Status, err)
	}
	items, err := store.ListItems(t.Context(), started.Turn.ID, 0, 500)
	if err != nil || !harness.ActiveCancellationFence(items) {
		t.Fatalf("unknown cancellation intent not retained: %v", err)
	}
	turn, err := store.GetTurn(t.Context(), started.Turn.ID)
	if err != nil || turn.Status == harness.TurnCancelled {
		t.Fatalf("cancel wrongly confirmed: %+v err=%v", turn, err)
	}
}

// The concrete native Features must not be composed with a foreign Runtime
// even if they share the exact same RunRelation recorder as Harness.
func TestPR82ExplicitlyVerifiedHostTeamWrapperKeepsLocalCancellation(t *testing.T) {
	_, turnID, parentItemID, relations, _, store, rt := newFeatureInvocationHarness(t)
	original := pr82LocalTeam(t, rt, relations)
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: rt, Agent: loadingFeatureAgent{runtime: rt}, Store: store,
		Clock: featureInvocationClock{}, Relations: relations,
		Teams: pr82VerifiedTeamFeature{pr82OpaqueTeamFeature{child: original}},
	})
	if err != nil {
		t.Fatal(err)
	}
	child := pr82StartLocalTeamChild(t, runner, turnID, parentItemID)
	parent, err := rt.Load(t.Context(), child.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	member := pr82LocalMember(t, rt, parent)
	_, err = runner.CancelInvocation(t.Context(), child.ID, "stop proven local member")
	if err != nil {
		t.Fatalf("verified route denied local cancellation: %v", err)
	}
	current, err := rt.Load(t.Context(), member.Run.ID)
	if err != nil || current.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("local member not cancelled: status=%s err=%v", current.Run.Status, err)
	}
	items, err := store.ListItems(t.Context(), turnID, 0, 500)
	if err != nil || harness.ActiveCancellationFence(items) {
		t.Fatalf("verified local cleanup retained unresolved fence: %v", err)
	}
}

func TestPR82NativeCompositeForeignRuntimeRejectedAtComposition(t *testing.T) {
	local, foreign := newFeatureInvocationRuntime(t), newFeatureInvocationRuntime(t)
	relations := newFeatureInvocationRelations(t)
	coord, err := handoff.New(&pr82OrphanMemberChildren{runs: map[string]kernel.Snapshot{}})
	if err != nil {
		t.Fatal(err)
	}
	nativeTeam, err := team.NewRunner(team.Dependencies{Runtime: foreign, Handoffs: coord, Relations: relations})
	if err != nil {
		t.Fatal(err)
	}
	nativeChat, err := groupchat.NewRunner(groupchat.Dependencies{Runtime: foreign, Handoffs: coord, Relations: relations})
	if err != nil {
		t.Fatal(err)
	}
	nativeWorkflow, err := workflow.NewRunner(workflow.Dependencies{Runtime: foreign, Effects: workflowWaitNoopEffects{}, Relations: relations})
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name    string
		compose func() harness.Dependencies
	}{
		{"Team", func() harness.Dependencies { return harness.Dependencies{Teams: nativeTeam} }},
		{"GroupChat", func() harness.Dependencies { return harness.Dependencies{GroupChats: nativeChat} }},
		{"Workflow", func() harness.Dependencies { return harness.Dependencies{Workflows: nativeWorkflow} }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			deps := scenario.compose()
			deps.Runtime, deps.Agent, deps.Store, deps.Clock, deps.Relations =
				local, loadingFeatureAgent{runtime: local}, harness.NewMemoryStore(), featureInvocationClock{}, relations
			_, err := harness.NewRunner(deps)
			if !errors.Is(err, harness.ErrInvalidRequest) {
				t.Fatalf("foreign native Runtime admitted: %v", err)
			}
		})
	}
}

func TestPR82OpaqueRemoteWorkflowSameRecorderDoesNotAuthorizeCancel(t *testing.T) {
	local, foreign := newFeatureInvocationRuntime(t), newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	relations := newFeatureInvocationRelations(t)
	native, err := workflow.NewRunner(workflow.Dependencies{
		Runtime: foreign, Effects: workflowWaitNoopEffects{}, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: local, Agent: loadingFeatureAgent{runtime: local},
		Workflows: pr82OpaqueWorkflowFeature{child: native}, Relations: relations,
		Store: store, Clock: featureInvocationClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
		ID: "foreign-workflow", Revision: 1, Name: "Foreign Workflow",
		Nodes: []workflow.Node{
			{ID: "approval", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "approval", Payload: json.RawMessage(`{}`)}},
			{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`{"done":true}`)}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runner.StartWorkflowTurn(t.Context(), harness.WorkflowTurnRequest{
		StartRequest: harness.StartRequest{
			HostThread: harness.HostRef{Kind: testThreadKind, ID: "foreign-workflow-thread"},
			HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "foreign-workflow-turn"},
			Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
			Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "foreign-workflow-thread"},
			Goal:       "remote workflow", RequestID: "remote-workflow",
			Config: harness.ConfigSnapshot{Model: "fixture-model"},
		},
		Definition: definition, Input: json.RawMessage(`{}`),
	})
	if err != nil || started.Turn.Status != harness.TurnWaitingInput {
		t.Fatalf("remote start: %v status=%s", err, started.Turn.Status)
	}
	root, ok := harness.TopLevelInvocation(started)
	if !ok {
		t.Fatal("missing Workflow root")
	}
	_, localErr := local.Load(t.Context(), root.ExecutionRefID)
	if !errors.Is(localErr, kernel.ErrNotFound) {
		t.Fatalf("remote run appeared in Harness Kernel: %v", localErr)
	}
	_, cancelErr := runner.Cancel(t.Context(), started.Turn.ID, "stop actual Workflow")
	if !errors.Is(cancelErr, harness.ErrRuntimeCancellationUnverified) {
		t.Fatalf("foreign Kernel cancellation accepted: %v", cancelErr)
	}
	still, err := foreign.Load(t.Context(), root.ExecutionRefID)
	if err != nil || still.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("foreign physical Run changed: status=%s err=%v", still.Run.Status, err)
	}
	turn, err := store.GetTurn(t.Context(), started.Turn.ID)
	if err != nil || turn.Status == harness.TurnCancelled {
		t.Fatalf("remote Workflow falsely cancelled: %+v err=%v", turn, err)
	}
	items, err := store.ListItems(t.Context(), started.Turn.ID, 0, 500)
	if err != nil || !harness.ActiveCancellationFence(items) {
		t.Fatalf("remote unknown outcome lost cancellation fence: %v", err)
	}
}

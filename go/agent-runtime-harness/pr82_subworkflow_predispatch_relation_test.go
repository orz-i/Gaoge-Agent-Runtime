package harness_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

var errPR82RelationWrite = errors.New("injected subworkflow relation write failure")

// A native child can be created only if its immutable parent relation exists
// already. This assertion is performed at the Kernel Store's physical Create
// boundary, not on a final projection that might hide an orphaned Run.
type pr82GuardedSubworkflowStore struct {
	*memory.Store
	relations runrelation.Store
	created   int
}

func (store *pr82GuardedSubworkflowStore) Create(ctx context.Context, record kernel.Record, events []kernel.EventDraft) (kernel.Snapshot, error) {
	if strings.HasSuffix(record.Run.ID, "-child") {
		edge, err := store.relations.GetByChild(ctx, record.Run.ID)
		if err != nil || edge.Kind != runrelation.KindWorkflowEffect || edge.ChildRunID != record.Run.ID {
			return kernel.Snapshot{}, fmt.Errorf("child Create without committed parent relation: %w", errPR82RelationWrite)
		}
		store.created++
	}
	return store.Store.Create(ctx, record, events)
}

// Fail before committing an edge or simulate a successfully committed edge
// whose caller loses its reply. Both are legitimate fault boundaries; neither
// may leave a child that was physically started after the failed Ensure.
type pr82SubworkflowRelationFault struct {
	*runrelation.Registry
	mode  string
	calls int
}

func (r *pr82SubworkflowRelationFault) Ensure(ctx context.Context, draft runrelation.Draft) (runrelation.Relation, error) {
	if draft.Kind == runrelation.KindWorkflowEffect {
		r.calls++
		if r.calls == 1 {
			switch r.mode {
			case "reject":
				return runrelation.Relation{}, errPR82RelationWrite
			case "committed-lost-reply":
				saved, err := r.Registry.Ensure(ctx, draft)
				if err != nil {
					return runrelation.Relation{}, err
				}
				return saved, errPR82RelationWrite
			}
		}
	}
	return r.Registry.Ensure(ctx, draft)
}

func TestPR82SubworkflowPreDispatchRelationAndCancellation(t *testing.T) {
	for _, mode := range []string{"reject", "committed-lost-reply", "normal"} {
		t.Run(mode, func(t *testing.T) {
			edgeStore := memory.NewRunRelationStore()
			index, err := runrelation.New(edgeStore, nil)
			if err != nil {
				t.Fatal(err)
			}
			relations := &pr82SubworkflowRelationFault{Registry: index, mode: mode}
			physical := &pr82GuardedSubworkflowStore{Store: memory.NewStore(), relations: edgeStore}
			runtime, err := kernel.New(kernel.Dependencies{Store: physical})
			if err != nil {
				t.Fatal(err)
			}
			turnStore := harness.NewMemoryStore()
			definitions, err := workflow.NewDefinitionRegistry(workflow.NewMemoryDefinitionStore(), nil)
			if err != nil {
				t.Fatal(err)
			}
			published, _, _, err := definitions.Publish(t.Context(), workflow.PublishDefinitionRequest{
				Scope:          workflow.DefinitionScope{Kind: workflow.DefinitionScopeActor, TenantID: testTenant, ActorID: testActor},
				IdempotencyKey: "pr82-subworkflow", PublishedBy: testActor,
				Draft: workflow.DefinitionDraft{ID: "pr82-child", Name: "Child", Nodes: []workflow.Node{
					{ID: "approval", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "pr82.wait", Payload: []byte(`{}`)}},
					{ID: "done", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: []byte(`true`)}},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			parent, err := workflow.CompileDefinition(workflow.DefinitionDraft{
				ID: "pr82-parent", Revision: 1, Name: "Parent", Nodes: []workflow.Node{
					{ID: "child", Type: workflow.NodeSubworkflow, Subworkflow: &workflow.SubworkflowNode{
						Definition: workflow.DefinitionReference{
							ID: published.Definition.ID, Revision: published.Definition.Revision, Hash: published.Definition.Hash,
						},
						Input: workflow.ValueSource{Kind: workflow.ValueWorkflowInput},
					}},
					{ID: "done", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: []byte(`true`)}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			native, err := workflow.NewRunner(workflow.Dependencies{
				Runtime: runtime, Effects: workflowWaitNoopEffects{}, Registry: definitions, Relations: relations,
			})
			if err != nil {
				t.Fatal(err)
			}
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: turnStore,
				Clock: featureInvocationClock{}, Workflows: native, Relations: relations,
			})
			if err != nil {
				t.Fatal(err)
			}
			started, startErr := runner.StartWorkflowTurn(t.Context(), harness.WorkflowTurnRequest{
				StartRequest: harness.StartRequest{
					HostThread: harness.HostRef{Kind: testThreadKind, ID: "pr82-subworkflow-" + mode},
					HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "pr82-subworkflow-" + mode},
					Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
					Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "pr82-subworkflow-" + mode},
					RequestID:  "pr82-subworkflow", Goal: "correct child ownership",
					Config: harness.ConfigSnapshot{Model: "fixture-model"},
				},
				Definition: parent, Input: []byte(`{"payload":1}`),
			})
			root, found := harness.TopLevelInvocation(started)
			if !found {
				t.Fatalf("missing parent Invocation: err=%v", startErr)
			}
			current, err := runtime.Load(t.Context(), root.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			view, err := workflow.ViewState(current)
			if err != nil || len(view.Effects) != 1 {
				t.Fatalf("missing persistent effect intent: %+v err=%v", view, err)
			}
			childID := view.Effects[0].ID + "-child"
			edge, edgeErr := relations.GetByChild(t.Context(), childID)
			child, childErr := runtime.Load(t.Context(), childID)
			if relations.calls == 0 {
				t.Fatal("subworkflow never attempted pre-dispatch relation registration")
			}
			switch mode {
			case "normal":
				if startErr != nil && !errors.Is(startErr, workflow.ErrEffectPending) {
					t.Fatalf("start failed: %v", startErr)
				}
				if edgeErr != nil || edge.ParentRunID != root.ExecutionRefID ||
					edge.Kind != runrelation.KindWorkflowEffect || edge.ChildRunID != childID ||
					childErr != nil || child.Run.Status != kernel.RunStatusWaitingInput || physical.created != 1 {
					t.Fatalf("child not gated by pre-created edge: edge=%+v edgeErr=%v child=%+v childErr=%v creates=%d",
						edge, edgeErr, child.Run, childErr, physical.created)
				}
				// Replaying the pending effect must reuse the same child and edge.
				_, resumeErr := native.Resume(t.Context(), current.Run.ID, current.Run.Revision)
				if resumeErr != nil && !errors.Is(resumeErr, workflow.ErrEffectPending) {
					t.Fatalf("replay error: %v", resumeErr)
				}
				if physical.created != 1 {
					t.Fatalf("replay created another child: %d", physical.created)
				}
				_, cancelErr := runner.Cancel(t.Context(), started.Turn.ID, "stop known local child")
				after, err := runtime.Load(t.Context(), childID)
				if cancelErr != nil || err != nil || after.Run.Status != kernel.RunStatusCancelled {
					t.Fatalf("verified child cancellation failed: child=%s err=%v cancel=%v", after.Run.Status, err, cancelErr)
				}
			case "reject":
				if startErr == nil || childErr == nil || !errors.Is(childErr, kernel.ErrNotFound) ||
					!errors.Is(edgeErr, runrelation.ErrNotFound) || physical.created != 0 ||
					current.Run.Status != kernel.RunStatusFailed {
					t.Fatalf("relation failed after child Create: start=%v childErr=%v edgeErr=%v creates=%d parent=%s",
						startErr, childErr, edgeErr, physical.created, current.Run.Status)
				}
				// There was no physical child, so the failed root can be
				// cleaned up without inventing any child-cancel receipt.
				if _, err := runner.Cancel(t.Context(), started.Turn.ID, "no child ever started"); err != nil {
					t.Fatalf("safe failed parent did not reconcile: %v", err)
				}
			case "committed-lost-reply":
				if startErr == nil || childErr == nil || !errors.Is(childErr, kernel.ErrNotFound) ||
					edgeErr != nil || physical.created != 0 || current.Run.Status != kernel.RunStatusFailed {
					t.Fatalf("lost reply caused child dispatch: start=%v childErr=%v edgeErr=%v creates=%d parent=%s",
						startErr, childErr, edgeErr, physical.created, current.Run.Status)
				}
				if _, cancelErr := runner.Cancel(t.Context(), started.Turn.ID, "uncertain relation"); !errors.Is(cancelErr, harness.ErrRuntimeCancellationUnverified) {
					t.Fatalf("unknown relation outcome settled: %v", cancelErr)
				}
				items, err := turnStore.ListItems(t.Context(), started.Turn.ID, 0, 500)
				if err != nil || !harness.ActiveCancellationFence(items) {
					t.Fatalf("unverified relation lost durable cancellation intent: %v", err)
				}
				// Reconstruction with exactly the original Kernel and index
				// must not turn a missing physical child into success.
				recovered, err := harness.NewRunner(harness.Dependencies{
					Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: turnStore,
					Clock: featureInvocationClock{}, Workflows: native, Relations: relations,
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, retryErr := recovered.Cancel(t.Context(), started.Turn.ID, "retry unknown"); retryErr == nil {
					t.Fatal("reconstructed Runner incorrectly settled unknown child")
				}
				items, err = turnStore.ListItems(t.Context(), started.Turn.ID, 0, 500)
				if err != nil || !harness.ActiveCancellationFence(items) {
					t.Fatalf("reconstructed Runner lost unknown intent: %v", err)
				}
			}
		})
	}
}

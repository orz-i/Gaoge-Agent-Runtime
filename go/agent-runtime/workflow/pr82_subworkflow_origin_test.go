package workflow_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

type pr82ChildCollisionIDs struct{ next int }

func (ids *pr82ChildCollisionIDs) NewID(prefix string) (string, error) {
	ids.next++
	if prefix == "effect" {
		return "reserved-effect", nil
	}
	return fmt.Sprintf("%s-proof-%d", prefix, ids.next), nil
}

type pr82ChildCollisionStore struct {
	*memory.Store
	hideChildOnce atomic.Bool
}

func (store *pr82ChildCollisionStore) Load(ctx context.Context, id string) (kernel.Snapshot, error) {
	if id == "reserved-effect-child" && store.hideChildOnce.CompareAndSwap(true, false) {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	return store.Store.Load(ctx, id)
}

func TestPR82SubworkflowDoesNotAdvanceForeignSameIDRun(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprintf("createConflict=%v", conflict), func(t *testing.T) {
			pr82CheckSubworkflowCollision(t, conflict)
		})
	}
}

func pr82CheckSubworkflowCollision(t *testing.T, conflict bool) {
	t.Helper()
	store := &pr82ChildCollisionStore{Store: memory.NewStore()}
	runtime, err := kernel.New(kernel.Dependencies{Store: store, Clock: workflowClock{}, IDs: &pr82ChildCollisionIDs{}})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := workflow.NewDefinitionRegistry(workflow.NewMemoryDefinitionStore(), registryClock{})
	if err != nil {
		t.Fatal(err)
	}
	published, _, _, err := registry.Publish(t.Context(), workflow.PublishDefinitionRequest{
		Scope: workflow.DefinitionScope{Kind: workflow.DefinitionScopeActor, TenantID: "tenant", ActorID: "actor"}, IdempotencyKey: "child", PublishedBy: "actor",
		Draft: workflow.DefinitionDraft{ID: "child", Name: "Child", Nodes: []workflow.Node{returnNode(json.RawMessage(`{"owned":true}`))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A separate workflow already occupies the deterministic ID. Its valid
	// durable state is deliberately resumable, so an unauthorized Resume is
	// observable as a revision/status change rather than merely a read leak.
	foreignDefinition := compileWorkflow(t, []workflow.Node{returnNode(json.RawMessage(`{"foreign":true}`))}, workflow.Limits{})
	foreignState, err := json.Marshal(workflow.View{Definition: foreignDefinition, Input: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := runtime.Create(t.Context(), kernel.CreateRequest{ID: "reserved-effect-child", Kind: workflow.RunKind,
		Actor: kernel.ActorRef{TenantID: "foreign-tenant", ActorID: "foreign-actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "foreign-thread"},
		RequestID: "foreign-request", Goal: "foreign goal", State: foreignState})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: runtime, Registry: registry, Effects: &requestExecutor{}})
	if err != nil {
		t.Fatal(err)
	}
	parent := compileAdvancedWorkflow(t, []workflow.Node{
		{ID: "child", Type: workflow.NodeSubworkflow, Subworkflow: &workflow.SubworkflowNode{Definition: workflow.DefinitionReference{ID: "child", Revision: published.Definition.Revision, Hash: published.Definition.Hash}, Input: workflow.ValueSource{Kind: workflow.ValueWorkflowInput}}},
		{ID: "done", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Source: &workflow.ValueSource{Kind: workflow.ValueNodeOutput, NodeID: "child"}}},
	}, workflow.DefinitionPolicy{})
	store.hideChildOnce.Store(conflict)
	result, startErr := runner.StartRun(t.Context(), workflowRequest(parent))
	after, err := runtime.Load(t.Context(), foreign.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Run.Revision != foreign.Run.Revision || after.Run.Status != foreign.Run.Status {
		t.Fatalf("foreign same-ID workflow advanced: before=%s/%d after=%s/%d parent=%s err=%v", foreign.Run.Status, foreign.Run.Revision, after.Run.Status, after.Run.Revision, result.Run.Status, startErr)
	}
	if startErr == nil {
		t.Fatalf("foreign child accepted: parent=%s result=%v", result.Run.Status, result.Result)
	}
	view, err := workflow.ViewState(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, effect := range view.Effects {
		if effect.ChildRunID == foreign.Run.ID {
			t.Fatalf("foreign Run accepted as subworkflow receipt after conflict: %+v", effect)
		}
	}
}

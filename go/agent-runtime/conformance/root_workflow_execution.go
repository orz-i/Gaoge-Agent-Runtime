package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// RunRootWorkflowSuite checks that a ROOT Workflow holds the same original,
// durable physical authority as parent-bound Workflow. A matching expected
// Run.Revision does not authorize two queue workers to dispatch one Effect.
func RunRootWorkflowSuite(t *testing.T, create func(*testing.T) kernel.Store) {
	t.Helper()
	t.Run("Root concurrent Resume and Cancel are physically serialized", func(t *testing.T) {
		runtime, err := kernel.New(kernel.Dependencies{Store: create(t)})
		if err != nil {
			t.Fatal(err)
		}
		executor := &rootPhysicalEffectBarrier{began: make(chan struct{}, 1), release: make(chan struct{})}
		runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: runtime, Effects: executor})
		if err != nil {
			t.Fatal(err)
		}
		draft := workflow.DefinitionDraft{
			ID: "root-single-writer", Revision: 1, Name: "root physical admission",
			Nodes: []workflow.Node{
				{ID: "first", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{
					Kind: "first.effect", Input: json.RawMessage(`{"original":true}`)}},
				{ID: "second", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{
					Kind: "second.effect", Input: json.RawMessage(`{"original":true}`)}},
				{ID: "done", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`{"done":true}`)}},
			},
		}
		definition, err := workflow.CompileDefinition(draft)
		if err != nil {
			t.Fatal(err)
		}
		root, err := runner.StartRun(t.Context(), workflow.StartRequest{
			ID: "root-run", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
			Goal:   "execute original physical Effect", Definition: definition, Input: json.RawMessage(`{}`),
		})
		if !errors.Is(err, workflow.ErrSegmentYielded) || executor.first.Load() != 1 || executor.second.Load() != 0 {
			t.Fatalf("fixture did not persist the second Effect: status=%s err=%v first=%d second=%d",
				root.Run.Status, err, executor.first.Load(), executor.second.Load())
		}
		if _, err = runtime.LookupChildExecution(t.Context(), root.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("quiescent yielded segment retained Root Grant: %v", err)
		}
		done := make(chan error, 1)
		go func() {
			_, resumeErr := runner.Resume(context.Background(), root.Run.ID, root.Run.Revision)
			done <- resumeErr
		}()
		select {
		case <-executor.began:
		case <-time.After(4 * time.Second):
			close(executor.release)
			t.Fatal("original physical Effect never started")
		}
		// Root physical Grant is self-owned and fences terminal Cancel,
		// stale duplicate Resume and an unbound direct Kernel Apply.
		_, err = runtime.Cancel(t.Context(), root.Run.ID, root.Run.Revision, "racing terminal")
		if !errors.Is(err, kernel.ErrChildExecutionBusy) {
			close(executor.release)
			t.Fatalf("physical Effect did not fence root Cancel: %v", err)
		}
		_, err = runner.Resume(t.Context(), root.Run.ID, root.Run.Revision)
		if !errors.Is(err, kernel.ErrChildExecutionBusy) {
			close(executor.release)
			t.Fatalf("second queue worker gained a Root physical Grant: %v", err)
		}
		current, loadErr := runtime.Load(t.Context(), root.Run.ID)
		if loadErr != nil {
			close(executor.release)
			t.Fatal(loadErr)
		}
		_, err = runtime.Apply(t.Context(), current.Run.ID, current.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: current.State,
		})
		if !errors.Is(err, kernel.ErrChildExecutionBusy) {
			close(executor.release)
			t.Fatalf("unbound Kernel Apply mutated a physically active Root: %v", err)
		}
		if executor.second.Load() != 1 {
			close(executor.release)
			t.Fatalf("two same-revision resumes reached external executor: calls=%d", executor.second.Load())
		}
		close(executor.release)
		select {
		case err = <-done:
			if err != nil {
				t.Fatalf("original Root effect failed after granted execution: %v", err)
			}
		case <-time.After(4 * time.Second):
			t.Fatal("Root original callback did not finish")
		}
		if executor.second.Load() != 1 {
			t.Fatalf("duplicate Effect dispatch: %d", executor.second.Load())
		}
		if _, err = runtime.LookupChildExecution(t.Context(), root.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("verified completed Root retained Grant: %v", err)
		}
	})
	t.Run("Root unknown external Pending quarantines physical Grant", func(t *testing.T) {
		runtime, err := kernel.New(kernel.Dependencies{Store: create(t)})
		if err != nil {
			t.Fatal(err)
		}
		executor := &rootPhysicalEffectBarrier{pending: true, began: make(chan struct{}, 1), release: make(chan struct{})}
		runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: runtime, Effects: executor})
		if err != nil {
			t.Fatal(err)
		}
		definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
			ID: "root-unknown", Revision: 1, Name: "root uncertain external Effect", Nodes: []workflow.Node{
				{ID: "physical", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{
					Kind: "first.effect", Input: json.RawMessage(`{"external":true}`)}},
				{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`{"ok":true}`)}},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		root, err := runner.StartRun(t.Context(), workflow.StartRequest{
			ID: "root-unknown", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "uncertain", Definition: definition, Input: json.RawMessage(`{}`),
		})
		if !errors.Is(err, kernel.ErrChildExecutionBusy) || !errors.Is(err, workflow.ErrEffectPending) || executor.first.Load() != 1 {
			t.Fatalf("uncertain root effect did not remain fenced: err=%v first=%d", err, executor.first.Load())
		}
		if _, err = runtime.LookupChildExecution(t.Context(), root.Run.ID); err != nil {
			t.Fatalf("unknown original root physical Grant lost: %v", err)
		}
		if _, err = runner.Resume(t.Context(), root.Run.ID, root.Run.Revision); !errors.Is(err, kernel.ErrChildExecutionBusy) {
			t.Fatalf("unsettled Root external Effect re-admitted: %v", err)
		}
		if executor.first.Load() != 1 {
			t.Fatalf("external Root Effect replayed: calls=%d", executor.first.Load())
		}
		if _, err = runtime.Cancel(t.Context(), root.Run.ID, root.Run.Revision, "unknown external effect"); !errors.Is(err, kernel.ErrChildExecutionBusy) {
			t.Fatalf("unresolved Root effect was falsely cancelled: %v", err)
		}
	})
	t.Run("Root revision CAS must predate physical Effects", func(t *testing.T) {
		rt, err := kernel.New(kernel.Dependencies{Store: create(t)})
		if err != nil {
			t.Fatal(err)
		}
		root, err := rt.Create(t.Context(), kernel.CreateRequest{
			ID: "root-revision", Kind: workflow.RunKind,
			Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
			Goal:   "exact version", State: json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		updated, err := rt.Apply(t.Context(), root.Run.ID, root.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: root.State,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = rt.BeginChildExecution(t.Context(), root.Run.ID, root.Run.Revision)
		if !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("stale Root version began Effect execution: %v", err)
		}
		grant, err := rt.BeginChildExecution(t.Context(), root.Run.ID, updated.Run.Revision)
		if err != nil {
			t.Fatalf("exact current Root revision rejected: %v", err)
		}
		if grant.ExpectedRevision != updated.Run.Revision {
			t.Fatalf("Store Grant did not seal original Root revision: %+v", grant)
		}
		if _, err = rt.BeginChildExecution(t.Context(), root.Run.ID, updated.Run.Revision); !errors.Is(err, kernel.ErrChildExecutionBusy) {
			t.Fatalf("duplicate Root Grant accepted: %v", err)
		}
		// No actual Feature callback ran, so no settlement marker may
		// be manufactured here; this fresh isolated Store is test-scoped.
	})
}

type rootPhysicalEffectBarrier struct {
	first, second atomic.Int32
	pending       bool
	began         chan struct{}
	release       chan struct{}
}

func (e *rootPhysicalEffectBarrier) Execute(ctx context.Context, req workflow.EffectRequest) (workflow.EffectResult, error) {
	switch req.Kind {
	case "first.effect":
		e.first.Add(1)
	case "second.effect":
		e.second.Add(1)
		select {
		case e.began <- struct{}{}:
		default:
		}
		select {
		case <-e.release:
		case <-ctx.Done():
			return workflow.EffectResult{}, ctx.Err()
		}
	}
	if e.pending {
		return workflow.EffectResult{Disposition: workflow.DispositionPending}, nil
	}
	return workflow.EffectResult{Disposition: workflow.DispositionCompleted,
		ReceiptID: "original_" + req.EffectID, Output: json.RawMessage(`{"receipt":true}`)}, nil
}

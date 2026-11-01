package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var errPR82SubworkflowRelation = errors.New("injected PostgreSQL subworkflow relation failure")

// The real PostgreSQL Kernel Store rejects any physical child Create unless
// the relation is already committed in the separate real PostgreSQL index.
type pr82PGChildCreateGuard struct {
	*KernelStore
	relations *runrelation.Registry
	created   int
}

func (g *pr82PGChildCreateGuard) Create(ctx context.Context, record kernel.Record, events []kernel.EventDraft) (kernel.Snapshot, error) {
	if strings.HasSuffix(record.Run.ID, "-child") {
		relation, err := g.relations.GetByChild(ctx, record.Run.ID)
		if err != nil || relation.ChildRunID != record.Run.ID || relation.Kind != runrelation.KindWorkflowEffect {
			return kernel.Snapshot{}, errPR82SubworkflowRelation
		}
		g.created++
	}
	return g.KernelStore.Create(ctx, record, events)
}

type pr82PGSubworkflowFault struct {
	*runrelation.Registry
	mode  string
	calls int
}

func (r *pr82PGSubworkflowFault) Ensure(ctx context.Context, draft runrelation.Draft) (runrelation.Relation, error) {
	if draft.Kind == runrelation.KindWorkflowEffect {
		r.calls++
		if r.calls == 1 {
			switch r.mode {
			case "reject":
				return runrelation.Relation{}, errPR82SubworkflowRelation
			case "committed-lost-reply":
				persisted, err := r.Registry.Ensure(ctx, draft)
				if err != nil {
					return runrelation.Relation{}, err
				}
				return persisted, errPR82SubworkflowRelation
			}
		}
	}
	return r.Registry.Ensure(ctx, draft)
}

type pr82UnexpectedExternalWorkflowEffect struct{}

func (pr82UnexpectedExternalWorkflowEffect) Execute(context.Context, workflow.EffectRequest) (workflow.EffectResult, error) {
	return workflow.EffectResult{}, errors.New("unexpected external Workflow Effect")
}

func TestRealPostgresPR82SubworkflowRelationshipPrecedesPhysicalChild(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	for _, mode := range []string{"normal", "reject", "committed-lost-reply"} {
		t.Run(mode, func(t *testing.T) {
			db, isolatedDSN := openIsolatedRealPostgres(t, dsn)
			if err := Migrate(db); err != nil {
				t.Fatal(err)
			}
			original, err := runrelation.New(NewRunRelationStore(db), nil)
			if err != nil {
				t.Fatal(err)
			}
			guard := &pr82PGChildCreateGuard{KernelStore: NewKernelStore(db), relations: original}
			runtime, err := kernel.New(kernel.Dependencies{Store: guard})
			if err != nil {
				t.Fatal(err)
			}
			relations := &pr82PGSubworkflowFault{Registry: original, mode: mode}
			definitions, err := workflow.NewDefinitionRegistry(workflow.NewMemoryDefinitionStore(), nil)
			if err != nil {
				t.Fatal(err)
			}
			published, _, _, err := definitions.Publish(t.Context(), workflow.PublishDefinitionRequest{
				Scope:          workflow.DefinitionScope{Kind: workflow.DefinitionScopeActor, TenantID: "tenant", ActorID: "actor"},
				IdempotencyKey: "child", PublishedBy: "actor",
				Draft: workflow.DefinitionDraft{ID: "child", Name: "Child", Nodes: []workflow.Node{
					{ID: "approval", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "pr82.approval", Payload: json.RawMessage(`{}`)}},
					{ID: "done", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`true`)}},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			parent, err := workflow.CompileDefinition(workflow.DefinitionDraft{
				ID: "parent", Revision: 1, Name: "Parent", Nodes: []workflow.Node{
					{ID: "child", Type: workflow.NodeSubworkflow, Subworkflow: &workflow.SubworkflowNode{
						Definition: workflow.DefinitionReference{
							ID: published.Definition.ID, Revision: published.Definition.Revision, Hash: published.Definition.Hash,
						},
						Input: workflow.ValueSource{Kind: workflow.ValueWorkflowInput},
					}},
					{ID: "done", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`true`)}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			runner, err := workflow.NewRunner(workflow.Dependencies{
				Runtime: runtime, Registry: definitions, Relations: relations, Effects: pr82UnexpectedExternalWorkflowEffect{},
			})
			if err != nil {
				t.Fatal(err)
			}
			pending, startErr := runner.StartRun(t.Context(), workflow.StartRequest{
				ID: "pr82-pg-parent", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
				Thread:    kernel.ThreadRef{Kind: "conversation", ID: "pr82-pg-thread"},
				RequestID: "pr82-pg-start", Goal: "parent",
				Definition: parent, Input: json.RawMessage(`{"payload":1}`),
			})
			view, err := workflow.ViewState(pending)
			if err != nil || len(view.Effects) != 1 {
				t.Fatalf("missing persisted child intent: %+v err=%v startErr=%v", view, err, startErr)
			}
			childID := view.Effects[0].ID + "-child"
			if relations.calls == 0 {
				t.Fatal("native Workflow failed to attempt pre-dispatch relation")
			}
			// Reconstruct independent Kernel/RunRelation readers against a
			// new PostgreSQL connection, as after a worker process restart.
			reopened, err := gorm.Open(pgdriver.Open(isolatedDSN), realPostgresTestConfig())
			if err != nil {
				t.Fatal(err)
			}
			raw, err := reopened.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = raw.Close() })
			recoveredRelations, err := runrelation.New(NewRunRelationStore(reopened), nil)
			if err != nil {
				t.Fatal(err)
			}
			recoveredKernel, err := kernel.New(kernel.Dependencies{Store: NewKernelStore(reopened)})
			if err != nil {
				t.Fatal(err)
			}
			edge, edgeErr := recoveredRelations.GetByChild(t.Context(), childID)
			child, childErr := recoveredKernel.Load(t.Context(), childID)
			if mode == "normal" {
				if startErr != nil && !errors.Is(startErr, workflow.ErrEffectPending) {
					t.Fatalf("normal dispatch failed: %v", startErr)
				}
				if edgeErr != nil || edge.ParentRunID != pending.Run.ID ||
					edge.Kind != runrelation.KindWorkflowEffect || childErr != nil ||
					child.Run.Status != kernel.RunStatusWaitingInput || guard.created != 1 {
					t.Fatalf("physical child started without durable edge: edge=%+v relErr=%v child=%+v childErr=%v created=%d",
						edge, edgeErr, child.Run, childErr, guard.created)
				}
				return
			}
			if startErr == nil || pending.Run.Status != kernel.RunStatusFailed ||
				!errors.Is(childErr, kernel.ErrNotFound) || guard.created != 0 {
				t.Fatalf("relation write failure still started physical child: err=%v parent=%s childErr=%v creates=%d",
					startErr, pending.Run.Status, childErr, guard.created)
			}
			if mode == "reject" && !errors.Is(edgeErr, runrelation.ErrNotFound) {
				t.Fatalf("failed PostgreSQL Ensure unexpectedly committed relation: %v", edgeErr)
			}
			if mode == "committed-lost-reply" && (edgeErr != nil || edge.ParentRunID != pending.Run.ID) {
				t.Fatalf("committed edge lost after PostgreSQL reconnect: %+v %v", edge, edgeErr)
			}
		})
	}
}

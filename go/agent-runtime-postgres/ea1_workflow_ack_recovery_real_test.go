package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type ea1WorkflowPGEffect struct{ calls atomic.Int32 }

func (e *ea1WorkflowPGEffect) Execute(context.Context, workflow.EffectRequest) (workflow.EffectResult, error) {
	e.calls.Add(1)
	return workflow.EffectResult{
		Disposition: workflow.DispositionCompleted,
		ReceiptID:   "original-physical-receipt",
		Output:      json.RawMessage(`{"committed":true}`),
	}, nil
}

// EA-1 P4: REAL PostgreSQL, original Mark's transaction committed, the ACK
// to the Workflow first-party caller was lost. Restart with an entirely new
// PostgreSQL connection and fresh Workflow Runner. Do not redispatch Effect.
// Also reject a forged StartRevision when the original marker is durable.
func TestEA1RealPostgresRootChildWorkflowMarkAckLostAcrossConnections(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	for _, root := range []bool{true, false} {
		label := "child"
		if root {
			label = "root"
		}
		t.Run(label+" committed mark and lost ACK", func(t *testing.T) {
			db, isolatedDSN := openIsolatedRealPostgres(t, dsn)
			if err := Migrate(db); err != nil {
				t.Fatal(err)
			}
			backing := NewKernelStore(db)
			wrapped := issue107PostMarkAckLossPG{KernelStore: backing}
			rt, err := kernel.New(kernel.Dependencies{Store: wrapped})
			if err != nil {
				t.Fatal(err)
			}
			actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
			thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
			parentID := ""
			var parent kernel.Snapshot
			if !root {
				parent, err = rt.Create(t.Context(), kernel.CreateRequest{
					ID: "original-parent", Kind: workflow.RunKind, Actor: actor, Thread: thread,
					Goal: "parent", State: json.RawMessage(`{}`),
				})
				if err != nil {
					t.Fatal(err)
				}
				parentID = parent.Run.ID
			}
			originalEffect := &ea1WorkflowPGEffect{}
			runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: rt, Effects: originalEffect})
			if err != nil {
				t.Fatal(err)
			}
			definition, err := workflow.CompileDefinition(workflow.DefinitionDraft{
				ID: "original-effect", Revision: 1, Name: "PostgreSQL durable Workflow",
				Nodes: []workflow.Node{
					{ID: "physical", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{
						Kind: "external.once", Input: json.RawMessage(`{"id":"one"}`)}},
					{ID: "return", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{
						Value: json.RawMessage(`{"done":true}`)}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			finished, startErr := runner.StartRun(t.Context(), workflow.StartRequest{
				ID: "original-run", RequestID: "original-effect-attempt", ParentRunID: parentID,
				Actor: actor, Thread: thread, Goal: "complete original physical callback",
				Definition: definition, Input: json.RawMessage(`{}`),
			})
			if !errors.Is(startErr, kernel.ErrChildExecutionBusy) ||
				finished.Run.Status != kernel.RunStatusCompleted || originalEffect.calls.Load() != 1 {
				t.Fatalf("P4 Mark ACK-loss fixture failed: status=%s err=%v externalCalls=%d",
					finished.Run.Status, startErr, originalEffect.calls.Load())
			}
			originalGrant, err := rt.LookupChildExecution(t.Context(), finished.Run.ID)
			if err != nil || originalGrant.ExpectedRevision == 0 {
				t.Fatalf("original Mark-committed physical Grant missing: %+v err=%v", originalGrant, err)
			}
			// A new physical GORM connection and a new Kernel + Workflow
			// Runner simulate a process restart without sharing in-memory
			// physical authorization or a private callback proof.
			newDB, err := gorm.Open(pgdriver.Open(isolatedDSN), realPostgresTestConfig())
			if err != nil {
				t.Fatal(err)
			}
			newSQL, err := newDB.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = newSQL.Close() })
			newStore := NewKernelStore(newDB)
			restored, err := kernel.New(kernel.Dependencies{Store: newStore})
			if err != nil {
				t.Fatal(err)
			}
			recoveredSnapshot, err := restored.Load(t.Context(), finished.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			view, viewErr := workflow.ViewState(recoveredSnapshot)
			if viewErr != nil || len(view.Effects) != 1 ||
				view.Effects[0].ReceiptID != "original-physical-receipt" {
				t.Fatalf("original receipt not durable at P4: effects=%+v err=%v", view.Effects, viewErr)
			}
			grant, err := restored.LookupChildExecution(t.Context(), finished.Run.ID)
			if err != nil || grant != originalGrant {
				t.Fatalf("new connection lost original Grant: %+v err=%v", grant, err)
			}
			forged := originalGrant
			forged.ExpectedRevision++
			if err = newStore.FinishChildExecution(t.Context(), forged); !errors.Is(err, kernel.ErrExecutionConflict) {
				t.Fatalf("forged original StartRevision could retire Mark-committed Grant: %v", err)
			}
			if !root {
				_, err = restored.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "parent before P4 recovery")
				if !errors.Is(err, kernel.ErrChildExecutionBusy) {
					t.Fatalf("parent was unfenced before recovering original P4 marker: %v", err)
				}
			}
			restartedEffect := &ea1WorkflowPGEffect{}
			owner, err := workflow.NewRunner(workflow.Dependencies{Runtime: restored, Effects: restartedEffect})
			if err != nil {
				t.Fatal(err)
			}
			if err = owner.ReconcileChildPhysicalGrant(t.Context(), finished.Run.ID); err != nil {
				t.Fatalf("first-party Workflow could not safely retire persisted post-Mark Grant: %v", err)
			}
			if _, err = restored.LookupChildExecution(t.Context(), finished.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("reconciled physical Grant still visible to independent connection: %v", err)
			}
			if !root {
				_, err = restored.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "after P4 recovery")
				if err != nil {
					t.Fatalf("parent still blocked after trusted recovery: %v", err)
				}
			}
			_, resumeErr := owner.Resume(t.Context(), finished.Run.ID, finished.Run.Revision)
			if !errors.Is(resumeErr, workflow.ErrWorkflowTerminal) {
				t.Fatalf("committed completed Workflow was resumed/replayed: %v", resumeErr)
			}
			if originalEffect.calls.Load() != 1 || restartedEffect.calls.Load() != 0 {
				t.Fatalf("original Effect replayed on recovery: first=%d new=%d",
					originalEffect.calls.Load(), restartedEffect.calls.Load())
			}
		})
	}
}

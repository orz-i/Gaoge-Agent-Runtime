package workflow_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// EA-1 P0 attempt-source check: a valid no-effect receipt from attempt N
// must NEVER authorize physical attempt N+1 after an ambiguous external send.
// Uses actual Root and parent-bound Child Workflow interpreters.
func TestEA1P0WorkflowAttemptLocalNoEffectAcrossRootAndChild(t *testing.T) {
	for _, root := range []bool{true, false} {
		label := "child"
		if root {
			label = "root"
		}
		t.Run(label, func(t *testing.T) {
			rt := newWorkflowRuntime(t)
			actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
			thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
			parentID := ""
			var parent kernel.Snapshot
			var err error
			if !root {
				parent, err = rt.Create(t.Context(), kernel.CreateRequest{
					ID: "parent", Kind: workflow.RunKind, Actor: actor, Thread: thread,
					Goal: "parent", State: json.RawMessage(`{}`),
				})
				if err != nil {
					t.Fatal(err)
				}
				parentID = parent.Run.ID
			}
			executor := &auditNoEffectAttemptCarryover{}
			runner := newWorkflowRunner(t, rt, executor)
			definition := compileWorkflow(t, []workflow.Node{
				{ID: "write", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{
					Kind: "external.write", Input: json.RawMessage(`{"immutable":true}`),
					Retry: workflow.RetryPolicy{
						MaxAttempts: 3, RetryableErrorCodes: []string{"*"},
					},
				}},
				returnNode(json.RawMessage(`{"done":true}`)),
			}, workflow.Limits{})
			request := workflowRequest(definition)
			request.ID = "original-attempt"
			request.ParentRunID = parentID
			request.Actor = actor
			request.Thread = thread
			first, firstErr := runner.StartRun(t.Context(), request)
			if !errors.Is(firstErr, workflow.ErrEffectPending) ||
				executor.attempts != 1 || executor.physicalWrites != 0 {
				t.Fatalf("first attempt was not a proven pre-dispatch NoEffect: err=%v attempts=%d writes=%d",
					firstErr, executor.attempts, executor.physicalWrites)
			}
			firstState, stateErr := workflow.ViewState(first)
			if stateErr != nil || len(firstState.Effects) != 1 ||
				firstState.Effects[0].Attempt != 2 ||
				!firstState.Effects[0].PhysicalNoEffect ||
				firstState.Effects[0].PhysicalNoEffectAttempt != 1 {
				t.Fatalf("first physical receipt must identify PREVIOUS attempt=1, not newly scheduled attempt=2: %+v err=%v",
					firstState.Effects, stateErr)
			}
			if _, err = rt.LookupChildExecution(t.Context(), first.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("true no-effect predecessor failed to retire old Grant: %v", err)
			}
			second, secondErr := runner.Resume(t.Context(), first.Run.ID, first.Run.Revision)
			if !errors.Is(secondErr, workflow.ErrEffectPending) ||
				executor.attempts != 2 || executor.physicalWrites != 1 {
				t.Fatalf("second physical post-send ambiguous attempt did not execute: err=%v attempts=%d writes=%d",
					secondErr, executor.attempts, executor.physicalWrites)
			}
			secondState, stateErr := workflow.ViewState(second)
			if stateErr != nil || len(secondState.Effects) != 1 ||
				secondState.Effects[0].PhysicalNoEffect ||
				secondState.Effects[0].PhysicalNoEffectAttempt != 0 {
				t.Fatalf("NoEffect evidence from attempt 1 falsely survived unknown attempt 2: %+v err=%v",
					secondState.Effects, stateErr)
			}
			originalGrant, lookupErr := rt.LookupChildExecution(t.Context(), second.Run.ID)
			if lookupErr != nil || originalGrant.ExpectedRevision == 0 {
				t.Fatalf("ambiguous external send lost its original physical Grant: %+v err=%v",
					originalGrant, lookupErr)
			}
			if !root {
				_, cancelErr := rt.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "unknown child Effect")
				if !errors.Is(cancelErr, kernel.ErrChildExecutionBusy) {
					t.Fatalf("attempt 2 unknown send did not fence original parent: %v", cancelErr)
				}
			}
			if _, resumeErr := runner.Resume(t.Context(), second.Run.ID, second.Run.Revision); !errors.Is(resumeErr, kernel.ErrChildExecutionBusy) {
				t.Fatalf("unknown physical attempt was re-admitted: %v", resumeErr)
			}
			if executor.attempts != 2 || executor.physicalWrites != 1 {
				t.Fatalf("later attempt physically replayed original ambiguous write: attempts=%d writes=%d",
					executor.attempts, executor.physicalWrites)
			}
		})
	}
}

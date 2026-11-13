package harness_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestIssue107HostedWaitingApprovalCancellationUsesOriginalHarnessFence(t *testing.T) {
	f := newIssue107HarnessFixture(t, true, true)
	waiting, err := f.harness.StartWorkflowTurn(t.Context(), f.request)
	if err != nil || len(waiting.Subtasks) != 1 || waiting.Subtasks[0].Approval == nil {
		t.Fatalf("Host fixture: err=%v subtasks=%+v", err, waiting.Subtasks)
	}
	task := waiting.Subtasks[0]
	child, err := f.runtime.Load(t.Context(), task.RunID)
	if err != nil || f.agent.OwnsKernelRuntime(f.runtime) ||
		!f.agent.QuiescentApprovalCancellation(f.runtime, child) {
		t.Fatalf("missing narrowly scoped Host waiting proof: err=%v child=%+v", err, child.Run)
	}
	cancelled, err := f.harness.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "stop before approval")
	if err != nil || f.toolCalls != 0 {
		t.Fatalf("safe Host cancellation did not complete: err=%v tools=%d", err, f.toolCalls)
	}
	child, err = f.runtime.Load(t.Context(), task.RunID)
	if err != nil || child.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("checkpoint escaped cancellation: %+v err=%v", child.Run, err)
	}
	if f.agent.QuiescentApprovalCancellation(f.runtime, child) {
		t.Fatal("cancelled Run retained an executable approval proof")
	}
	if _, err = f.harness.ResolveSubtaskApproval(t.Context(), waiting.Turn.ID, task.ID,
		task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove}); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("approval after cancellation succeeded: %v", err)
	}
	if cancelled.Subtasks[0].CancelStatus != "confirmed" {
		t.Fatalf("subtask cancellation not settled: %+v", cancelled.Subtasks)
	}
}

func TestIssue107HostedNoStaticFenceFailsBeforeCancelIntent(t *testing.T) {
	f := newIssue107HarnessFixture(t, true, false)
	waiting, err := f.harness.StartWorkflowTurn(t.Context(), f.request)
	if err != nil || len(waiting.Subtasks) != 1 || waiting.Subtasks[0].Approval == nil {
		t.Fatalf("Host fixture: err=%v subtasks=%+v", err, waiting.Subtasks)
	}
	task := waiting.Subtasks[0]
	// A mutable executor cannot enable an opt-in missing from the original
	// composition root, even though the exact Agent and Recorder still match.
	f.executor.approvalFenced = true
	_, err = f.harness.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "unverified owner")
	if !errors.Is(err, harness.ErrRuntimeCancellationUnverified) {
		t.Fatalf("missing original Host admission attestation accepted: %v", err)
	}
	items, err := f.store.ListItems(t.Context(), waiting.Turn.ID, 0, 500)
	if err != nil || harness.ActiveCancellationFence(items) {
		t.Fatalf("pre-effect rejection stranded a cancellation intent: %v", err)
	}
	// Denial before cancellation admission must preserve the original pending
	// checkpoint and allow exactly one subsequent approved Tool execution.
	resolved, err := f.harness.ResolveSubtaskApproval(t.Context(), waiting.Turn.ID, task.ID,
		task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove})
	if err != nil || len(resolved.Subtasks) != 1 {
		t.Fatalf("no-effect cancellation failure blocked later approval: %+v err=%v", resolved.Subtasks, err)
	}
	child, err := f.runtime.Load(t.Context(), task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.agent.Resume(t.Context(), child.Run.ID, child.Run.Revision); err != nil || f.toolCalls != 1 {
		t.Fatalf("approved Tool was dropped or executed twice: %v; calls=%d", err, f.toolCalls)
	}
}

func TestIssue107HostedAfterApprovalRunningCannotUseWaitingOnlyCancelProof(t *testing.T) {
	f := newIssue107HarnessFixture(t, true, true)
	waiting, err := f.harness.StartWorkflowTurn(t.Context(), f.request)
	if err != nil || len(waiting.Subtasks) != 1 || waiting.Subtasks[0].Approval == nil {
		t.Fatalf("Host fixture: err=%v subtasks=%+v", err, waiting.Subtasks)
	}
	task := waiting.Subtasks[0]
	if _, err = f.harness.ResolveSubtaskApproval(t.Context(), waiting.Turn.ID, task.ID,
		task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove}); err != nil {
		t.Fatal(err)
	}
	child, err := f.runtime.Load(t.Context(), task.RunID)
	if err != nil || child.Run.Status != kernel.RunStatusRunning ||
		f.agent.QuiescentApprovalCancellation(f.runtime, child) {
		t.Fatalf("post-approval must not certify waiting proof: %+v err=%v", child.Run, err)
	}
	_, err = f.harness.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "running with Host middleware")
	if !errors.Is(err, harness.ErrRuntimeCancellationUnverified) {
		t.Fatalf("running Host Agent was wrongly physically cancelled: %v", err)
	}
	items, err := f.store.ListItems(t.Context(), waiting.Turn.ID, 0, 500)
	if err != nil || harness.ActiveCancellationFence(items) {
		t.Fatalf("unsafe running attempt stranded a cancellation intent: %v", err)
	}
	if _, err = f.agent.Resume(t.Context(), child.Run.ID, child.Run.Revision); err != nil || f.toolCalls != 1 {
		t.Fatalf("running Host Agent lost its original Tool execution: err=%v tools=%d", err, f.toolCalls)
	}
}

func TestIssue107HostedRootCancellationClosesVerifiedWaitingChild(t *testing.T) {
	f := newIssue107HarnessFixture(t, true, true)
	waiting, err := f.harness.StartWorkflowTurn(t.Context(), f.request)
	if err != nil || len(waiting.Subtasks) != 1 || waiting.Subtasks[0].Approval == nil {
		t.Fatalf("Host fixture: err=%v subtasks=%+v", err, waiting.Subtasks)
	}
	cancelled, err := f.harness.Cancel(t.Context(), waiting.Turn.ID, "stop entire Workflow")
	if err != nil || cancelled.Turn.Status != harness.TurnCancelled {
		t.Fatalf("root cancellation failed: err=%v turn=%+v", err, cancelled.Turn)
	}
	child, err := f.runtime.Load(t.Context(), waiting.Subtasks[0].RunID)
	if err != nil || child.Run.Status != kernel.RunStatusCancelled || f.toolCalls != 0 {
		t.Fatalf("root failed to stop its valid child: child=%+v err=%v tools=%d", child.Run, err, f.toolCalls)
	}
}

func TestIssue107HostedApprovalCancelPhysicalAdmissionRace(t *testing.T) {
	for i := range 40 {
		t.Run(fmt.Sprint("attempt-", i), func(t *testing.T) {
			f := newIssue107HarnessFixture(t, true, true)
			waiting, err := f.harness.StartWorkflowTurn(t.Context(), f.request)
			if err != nil || len(waiting.Subtasks) != 1 || waiting.Subtasks[0].Approval == nil {
				t.Fatalf("fixture: %+v err=%v", waiting.Subtasks, err)
			}
			task := waiting.Subtasks[0]
			gate := make(chan struct{})
			var wg sync.WaitGroup
			var cancelErr, approvalErr error
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-gate
				_, cancelErr = f.harness.CancelSubtask(context.Background(), waiting.Turn.ID, task.ID, "racing cancellation")
			}()
			go func() {
				defer wg.Done()
				<-gate
				_, approvalErr = f.harness.ResolveSubtaskApproval(context.Background(), waiting.Turn.ID, task.ID,
					task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove})
			}()
			close(gate)
			wg.Wait()
			if cancelErr == nil && approvalErr == nil {
				t.Fatal("concurrent cancel and approval were both admitted")
			}
			child, err := f.runtime.Load(t.Context(), task.RunID)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case cancelErr == nil:
				if child.Run.Status != kernel.RunStatusCancelled || f.toolCalls != 0 {
					t.Fatalf("cancel won but Tool escaped: child=%s tools=%d approval=%v", child.Run.Status, f.toolCalls, approvalErr)
				}
			case approvalErr == nil:
				if child.Run.Status != kernel.RunStatusRunning || f.toolCalls != 0 {
					t.Fatalf("approval won but cancellation or Tool escaped: child=%s tools=%d cancel=%v", child.Run.Status, f.toolCalls, cancelErr)
				}
				if _, err := f.agent.Resume(t.Context(), child.Run.ID, child.Run.Revision); err != nil || f.toolCalls != 1 {
					t.Fatalf("approved Tool must execute exactly once: err=%v calls=%d", err, f.toolCalls)
				}
			default:
				t.Fatalf("both branches lost admission: cancel=%v approval=%v child=%s", cancelErr, approvalErr, child.Run.Status)
			}
		})
	}
}

func TestIssue107HostedRepeatedCancelAlreadyTerminalChild(t *testing.T) {
	f := newIssue107HarnessFixture(t, true, true)
	waiting, err := f.harness.StartWorkflowTurn(t.Context(), f.request)
	if err != nil || len(waiting.Subtasks) != 1 || waiting.Subtasks[0].Approval == nil {
		t.Fatalf("fixture: err=%v subtasks=%+v", err, waiting.Subtasks)
	}
	task := waiting.Subtasks[0]
	if _, err = f.harness.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "cancel once"); err != nil {
		t.Fatalf("first cancellation: %v", err)
	}
	_, err = f.harness.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "cancel again")
	if err != nil {
		t.Fatalf("already terminal owned child cannot be reconciled by repeat cancellation: %v", err)
	}
	child, err := f.runtime.Load(t.Context(), task.RunID)
	if err != nil || child.Run.Status != kernel.RunStatusCancelled || f.toolCalls != 0 {
		t.Fatalf("double cancellation changed execution: status=%s err=%v tools=%d", child.Run.Status, err, f.toolCalls)
	}
}

func TestIssue107RetainedMiddlewareContextCannotReplaySettledApproval(t *testing.T) {
	f := newIssue107HarnessFixture(t, true, true, true)
	waiting, err := f.harness.StartWorkflowTurn(t.Context(), f.request)
	if err != nil || len(waiting.Subtasks) != 1 || waiting.Subtasks[0].Approval == nil {
		t.Fatalf("fixture: %+v err=%v", waiting.Subtasks, err)
	}
	task := waiting.Subtasks[0]
	before, err := f.runtime.Load(t.Context(), task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.harness.ResolveSubtaskApproval(t.Context(), waiting.Turn.ID, task.ID,
		task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove})
	if err != nil || !f.observedApprovalGrant || f.savedApprovalCtx == nil {
		t.Fatalf("valid original physical admission not observed: err=%v grant=%v ctx=%v", err, f.observedApprovalGrant, f.savedApprovalCtx != nil)
	}
	if harness.VerifiedSubtaskApprovalDispatch(f.savedApprovalCtx, task.RunID, before.Run.Revision, task.Approval.CheckpointID) {
		t.Fatal("middleware retained a live approval authority after physical callback completed")
	}
}

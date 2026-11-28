package harness_test

import (
	"errors"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestNativeExternalCancellationRetainsOriginalElection(t *testing.T) {
	for _, target := range []string{"root", "member", "direct-root"} {
		t.Run(target, func(t *testing.T) {
			c := newMemberExternalCase(t, true, true)
			ctx := t.Context()
			req := testStartRequest()
			req.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 8, MaxToolCalls: 16, MaxTotalTokens: 5000, MaxChildRuns: 3, MaxConcurrentRuns: 1}
			req.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1, LocalToolGrants: []harness.DelegatedLocalToolGrant{{MemberID: "researcher", MemberRevision: "v3", ToolKey: roleEvidenceTool}}}
			req.Config.ToolKeys = []string{harness.DelegationToolKey}
			req.Config.ToolPolicies = []harness.ToolPolicySnapshot{harness.DelegationToolPolicySnapshot(), {Key: roleEvidenceTool, DefinitionVersion: "v1", ExecutionMode: "local_dispatch", ApprovalCapability: "per_call", ApprovalMode: "always"}}
			if target == "direct-root" {
				req.Config.Model = "specialist-model"
				req.Config.ToolKeys = []string{roleEvidenceTool}
				req.Config.DelegationPolicy = harness.DelegationPolicySnapshot{}
				req.Config.ToolPolicies = req.Config.ToolPolicies[1:]
			}
			start, err := c.host.StartAgentTurn(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			root, ok := harness.TopLevelInvocation(start)
			if !ok {
				t.Fatal("missing root")
			}
			member := harness.Subtask{RunID: root.ExecutionRefID}
			if target != "direct-root" {
				if len(start.Subtasks) != 1 {
					t.Fatal(start.Subtasks)
				}
				member = start.Subtasks[0]
			}
			wait, err := c.native.LoadRun(ctx, member.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if target != "direct-root" {
				wait, err = c.native.Resume(ctx, wait.Run.ID, wait.Run.Revision)
				if err != nil {
					t.Fatal(err)
				}
			}
			control := agent.ExternalToolControl{Actor: wait.Run.Actor, ParentRunID: root.ExecutionRefID, RunID: member.RunID, CallID: "lookup", OperationID: "external-browser", InputDigest: strings.Repeat("e", 64), ControlID: "original-browser-cancel"}
			if target == "direct-root" {
				control.ParentRunID = ""
			}
			c.owner.original = control
			selected := root.ExecutionRefID
			cancel := func() (harness.Snapshot, error) { return c.host.Cancel(ctx, start.Turn.ID, "original control") }
			if target == "member" {
				selected = member.RunID
				cancel = func() (harness.Snapshot, error) {
					return c.host.CancelSubtask(ctx, start.Turn.ID, member.ID, "original control")
				}
			}
			ordinaryCancel := cancel
			originalGrant, err := c.runtime.LookupChildExecution(ctx, member.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if err = c.host.VerifyExternalToolControlScope(ctx, start.Turn.ID, selected, control); err == nil {
				t.Fatal("accepted an unelected cancellation")
			}
			for attempt := range 2 {
				if attempt == 1 {
					cancel = func() (harness.Snapshot, error) {
						return c.host.ContinueExternalToolCancellation(ctx, start.Turn.ID, selected, control.ControlID, "original control")
					}
				}
				if _, err = cancel(); !errors.Is(err, kernel.ErrChildExecutionBusy) {
					t.Fatalf("cancel before owner receipt: %v", err)
				}
				if err = c.host.VerifyExternalToolControlScope(ctx, start.Turn.ID, selected, control); err != nil {
					t.Fatalf("original election: %v", err)
				}
			}
			items, err := c.store.ListItems(ctx, start.Turn.ID, 0, 1000)
			if err != nil || !harness.ActiveCancellationFence(items) {
				t.Fatalf("lost election: %v", err)
			}
			intents := 0
			for _, item := range items {
				if item.ParentItemID == harness.CancellationFenceParentItemID {
					intents++
				}
			}
			if intents != 1 {
				t.Fatalf("re-elected original cancellation: %d", intents)
			}
			foreign := control
			foreign.ControlID = "another-owner-control"
			if err = c.host.VerifyExternalToolControlScope(ctx, start.Turn.ID, selected, foreign); err == nil {
				t.Fatal("replaced the original owner control")
			}
			ledger, err := c.runtime.BudgetStore().LoadBudget(ctx, start.Turn.ID)
			if err != nil || ledger.RunCancelled(selected) || ledger.Reservations["tool:"+member.RunID+":lookup"].Status != budget.ReservationHeld {
				t.Fatalf("changed unknown quota: %+v %v", ledger, err)
			}
			c.owner.started = true
			if _, err = c.native.ReconcileExternalToolNoEffectForControl(ctx, wait.Run.Revision, control); !errors.Is(err, agent.ErrToolOutcomeUnknown) {
				t.Fatalf("accepted started owner: %v", err)
			}
			c.owner.started = false
			applied, err := c.native.ReconcileExternalToolNoEffectForControl(ctx, wait.Run.Revision, control)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.native.ReconcileExternalToolNoEffectForControl(ctx, applied.Run.Revision, control); err != nil {
				t.Fatalf("same receipt replay: %v", err)
			}
			parked, err := c.native.Resume(ctx, member.RunID, applied.Run.Revision)
			if err != nil || parked.Run.Revision != applied.Run.Revision {
				t.Fatalf("Resume escaped original control after retirement: %+v %v", parked.Run, err)
			}
			retired, err := c.runtime.LoadOwnedRetirement(ctx, originalGrant.ID)
			if err != nil || !retired.Valid() || retired.Evidence.Grant != originalGrant {
				t.Fatalf("lost original native retirement: %+v %v", retired, err)
			}
			if _, err = ordinaryCancel(); err == nil {
				t.Fatal("ordinary cancellation completed another bound control")
			}
			if _, err = c.host.ContinueExternalToolCancellation(ctx, start.Turn.ID, selected, "another-owner-control", "foreign control"); err == nil {
				t.Fatal("foreign control completed original cancellation")
			}
			if _, err = cancel(); err != nil {
				t.Fatalf("complete original cancellation: %v", err)
			}
			final, err := c.native.LoadRun(ctx, member.RunID)
			if err != nil || final.Run.Status != kernel.RunStatusCancelled {
				t.Fatalf("target: %+v %v", final.Run, err)
			}
			ledger, err = c.runtime.BudgetStore().LoadBudget(ctx, start.Turn.ID)
			reservation := ledger.Reservations["tool:"+member.RunID+":lookup"]
			if err != nil || reservation.Status != budget.ReservationSettled || reservation.Usage.ToolCalls != 0 || c.owner.prepared != 1 {
				t.Fatalf("original receipt/accounting: %+v %v preparations=%d", reservation, err, c.owner.prepared)
			}
		})
	}
}

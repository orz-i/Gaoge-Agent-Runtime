package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestNativeWrongToolReceiptCannotConsumePendingCall(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	m := &lateModel{invokeTool: true}
	effects := 0
	catalog, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: "slow", Name: "Slow", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			effects++
			return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: "not-" + request.Call.ID, Disposition: "committed"}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: m, Catalog: catalog, Executor: catalog,
		RunExecution: &agent.RunExecution{OwnerID: "worker", LeaseDuration: 2 * time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}
	staged, e := runner.StartRun(t.Context(), agent.StartRequest{
		ID: "wrong-receipt", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "do slow Tool", ToolKeys: []string{"slow"},
	})
	if !errors.Is(e, agent.ErrToolOutcomeUnknown) || !errors.Is(e, agent.ErrToolReceiptMismatch) ||
		staged.Run.Status != kernel.RunStatusRunning || effects != 1 {
		t.Fatalf("mismatched receipt was accepted: status=%s effects=%d err=%v", staged.Run.Status, effects, e)
	}
	var persisted struct {
		ToolDispatchID string `json:"toolDispatchID"`
	}
	if err = json.Unmarshal(staged.State, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.ToolDispatchID != "late-call" {
		t.Fatalf("lost durable dispatch intent: %+v", persisted)
	}
	if _, e = runner.Resume(t.Context(), staged.Run.ID, staged.Run.Revision); !errors.Is(e, agent.ErrToolOutcomeUnknown) || effects != 1 {
		t.Fatalf("mismatched result was retried as a physical Tool effect: %v effects=%d", e, effects)
	}
	if _, e = runner.ReconcileToolOutcome(t.Context(), staged.Run.ID, staged.Run.Revision, "late-call"); !errors.Is(e, agent.ErrToolVerificationRequired) {
		t.Fatalf("missing trusted verifier silently accepted: %v", e)
	}
}

func TestNativePendingToolNeedsVerifiedResultBeforeReentry(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	effects := 0
	m := &lateModel{invokeTool: true}
	catalog, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: "slow", Name: "Slow", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			effects++ // the handler may have caused a non-idempotent effect
			return tools.ExecutionResult{Content: json.RawMessage(`{"status":"pending"}`),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: tools.ReceiptDispositionPending}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	verifyCalls := 0
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: m, Catalog: catalog, Executor: catalog,
		RunExecution: &agent.RunExecution{OwnerID: "native", LeaseDuration: 2 * time.Minute},
		ToolOutcomeVerifier: agent.ToolOutcomeVerifierFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			verifyCalls++
			if verifyCalls == 1 {
				return tools.ExecutionResult{}, errors.New("provider lookup temporarily unavailable")
			}
			if verifyCalls == 2 {
				return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
					Receipt: tools.Receipt{ExecutionID: "wrong-call-id", Disposition: "committed"}}, nil
			}
			if effects != 1 || request.Call.ID != "late-call" {
				return tools.ExecutionResult{}, errors.New("provider did not confirm this operation")
			}
			return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	first, e := runner.StartRun(t.Context(), agent.StartRequest{
		ID: "pending-native", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "pending Tool", ToolKeys: []string{"slow"},
	})
	if e != nil || first.Run.Status != kernel.RunStatusRunning || effects != 1 {
		t.Fatalf("native pending Tool failed: %+v effects=%d err=%v", first.Run, effects, e)
	}
	var persisted struct {
		ToolDispatchID string `json:"toolDispatchID"`
	}
	if e = json.Unmarshal(first.State, &persisted); e != nil || persisted.ToolDispatchID != "late-call" {
		t.Fatalf("Pending lost durable operation identity: %+v err=%v", persisted, e)
	}
	if _, e = runner.Resume(t.Context(), first.Run.ID, first.Run.Revision); !errors.Is(e, agent.ErrToolOutcomeUnknown) || effects != 1 {
		t.Fatalf("Pending Tool was reexecuted: effects=%d err=%v", effects, e)
	}
	if _, e = runner.ReconcileToolOutcome(t.Context(), first.Run.ID, first.Run.Revision, "late-call"); !errors.Is(e, agent.ErrToolOutcomeUnknown) || effects != 1 {
		t.Fatalf("unverified result was accepted: %v effects=%d", e, effects)
	}
	// Failed and mismatched provider verifications must each release the
	// read-only reconciliation lease; no Tool reentry is permitted.
	if _, e = runner.ReconcileToolOutcome(t.Context(), first.Run.ID, first.Run.Revision, "late-call"); !errors.Is(e, agent.ErrToolReceiptMismatch) || effects != 1 {
		t.Fatalf("wrong provider receipt was committed: err=%v effects=%d", e, effects)
	}
	completed, e := runner.ReconcileToolOutcome(t.Context(), first.Run.ID, first.Run.Revision, "late-call")
	if e != nil || completed.Run.Status != kernel.RunStatusRunning || effects != 1 || verifyCalls != 3 {
		t.Fatalf("trusted Pending reconciliation failed: %+v effects=%d verification=%d err=%v", completed.Run, effects, verifyCalls, e)
	}
	done, e := runner.Resume(t.Context(), completed.Run.ID, completed.Run.Revision)
	if e != nil || done.Run.Status != kernel.RunStatusCompleted || effects != 1 {
		t.Fatalf("pending Tool was replayed after verified receipt: %+v effects=%d err=%v", done.Run, effects, e)
	}
}

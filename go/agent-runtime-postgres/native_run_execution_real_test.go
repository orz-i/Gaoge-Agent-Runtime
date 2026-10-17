package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type nativePostgresModel struct{ calls *int }

func (m nativePostgresModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	*m.calls++
	for _, msg := range request.Messages {
		if msg.Role == model.RoleTool {
			return model.Response{Content: "complete", Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}}, nil
		}
	}
	return model.Response{ToolCalls: []tools.Call{{ID: "real-pg-tool", ToolKey: "lookup",
		Arguments: json.RawMessage(`{}`)}}, Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}}, nil
}

type nativePostgresApprovalPolicy struct{}

func (nativePostgresApprovalPolicy) Name() string { return "required" }
func (nativePostgresApprovalPolicy) Approval(context.Context, plugin.ToolInvocation) (plugin.ApprovalRequirement, error) {
	return plugin.ApprovalRequired, nil
}

func TestRealPostgresNativeRunLeaseApprovalAndResume(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("real PostgreSQL is not configured")
	}
	db, isolation := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	calls, effects := 0, 0
	mkRunner := func(store *KernelStore, owner string) (*agent.Runner, *kernel.Runtime) {
		t.Helper()
		rt, err := kernel.New(kernel.Dependencies{Store: store})
		if err != nil {
			t.Fatal(err)
		}
		approvals, err := interaction.New(rt)
		if err != nil {
			t.Fatal(err)
		}
		registry, err := tools.NewRegistry([]tools.Registration{{
			Definition: tools.Definition{Key: "lookup", Name: "Lookup", InputSchema: json.RawMessage(`{"type":"object"}`)},
			Handler: tools.HandlerFunc(func(_ context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
				effects++
				return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
					Receipt: tools.Receipt{ExecutionID: r.Call.ID, Disposition: "committed"}}, nil
			}),
		}})
		if err != nil {
			t.Fatal(err)
		}
		runner, err := agent.NewRunner(agent.Dependencies{
			Runtime: rt, Model: nativePostgresModel{calls: &calls}, Catalog: registry, Executor: registry,
			Approvals:        interactionadapter.New(approvals),
			ApprovalPolicies: []plugin.ApprovalPolicy{nativePostgresApprovalPolicy{}},
			RunExecution:     &agent.RunExecution{OwnerID: owner, LeaseDuration: 2 * time.Minute},
		})
		if err != nil {
			t.Fatal(err)
		}
		return runner, rt
	}
	starter, rt := mkRunner(NewKernelStore(db), "original")
	waiting, err := starter.StartRun(t.Context(), agent.StartRequest{
		ID: "real-pg-native-lease", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"},
		Goal:   "one approval through native leases", Model: "fixture", ToolKeys: []string{"lookup"},
	})
	if err != nil || waiting.Run.Status != kernel.RunStatusWaitingInput || calls != 1 {
		t.Fatalf("real PG native start: %+v calls=%d err=%v", waiting.Run, calls, err)
	}
	if err = rt.RequireExecution(t.Context(), waiting.Run.ID); !errors.Is(err, kernel.ErrExecutionRequired) {
		t.Fatalf("native PG Stage2B start didn't acquire an epoch: %v", err)
	}
	approver, other := mkRunner(NewKernelStore(reopenRecoveryPostgres(t, isolation)), "approver")
	stored, err := other.Load(t.Context(), waiting.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	granted, err := approver.ResolveApproval(t.Context(), stored.Run.ID, stored.Run.Revision,
		plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
	if err != nil || granted.Run.Status != kernel.RunStatusRunning || effects != 0 {
		t.Fatalf("control raced ahead of execution claim: %+v tools=%d err=%v", granted.Run, effects, err)
	}
	resumer, _ := mkRunner(NewKernelStore(reopenRecoveryPostgres(t, isolation)), "resumer")
	completed, err := resumer.Resume(t.Context(), granted.Run.ID, granted.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted || calls != 2 || effects != 1 {
		t.Fatalf("real PG native resume status=%s model=%d tool=%d err=%v", completed.Run.Status, calls, effects, err)
	}
	if _, err = resumer.Resume(t.Context(), completed.Run.ID, completed.Run.Revision); !errors.Is(err, agent.ErrRunTerminal) {
		t.Fatalf("terminal replay entered execution: %v", err)
	}
	if calls != 2 || effects != 1 {
		t.Fatal("terminal replay dispatched physical effects")
	}
}

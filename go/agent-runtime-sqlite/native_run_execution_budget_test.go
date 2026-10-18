package sqlite_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestNativeLeaseBudgetChild(t *testing.T) {
	phase := os.Getenv("GAOGE_NATIVE_BUDGET_PHASE")
	if phase == "" {
		return
	}
	db := os.Getenv("GAOGE_NATIVE_BUDGET_DB")
	marker := os.Getenv("GAOGE_NATIVE_BUDGET_MARKER")
	ctx := context.Background()
	var store *sqlite.Store
	var err error
	if phase == "start" {
		store, err = sqlite.CreateNew(ctx, db)
	} else {
		store, err = sqlite.OpenExisting(ctx, db)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := controlBudgetRunner(rt, marker, &agent.RunExecution{OwnerID: phase, LeaseDuration: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	switch phase {
	case "start":
		waiting, err := runner.StartRun(ctx, agent.StartRequest{
			ID: "native-budget", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"},
			Goal:   "lookup then use one more Model call", Model: "fixture", ToolKeys: []string{"lookup"},
			CallAllowance: &agent.CallAllowance{LLMCalls: 1, ToolCalls: 2},
			Limits:        agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 4},
		})
		if err != nil || waiting.Run.Status != kernel.RunStatusPausedBudget ||
			waiting.Run.ErrorCode != "agent.model_allowance_exhausted" {
			t.Fatalf("native Stage2B budget not gated %+v %v", waiting.Run, err)
		}
		if err = rt.RequireExecution(ctx, waiting.Run.ID); !errors.Is(err, kernel.ErrExecutionRequired) {
			t.Fatalf("Run did not acquire sticky native lease: %v", err)
		}
	case "grant":
		waiting, err := rt.Load(ctx, "native-budget")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = runner.GrantCallAllowance(ctx, waiting.Run.ID, waiting.Run.Revision,
			agent.CallAllowance{ToolCalls: 1}); !errors.Is(err, agent.ErrInvalidCallAllowance) {
			t.Fatalf("wrong dimension accepted: %v", err)
		}
		if _, err = runner.GrantCallAllowance(ctx, waiting.Run.ID, waiting.Run.Revision,
			agent.CallAllowance{LLMCalls: 5}); !errors.Is(err, agent.ErrInvalidCallAllowance) {
			t.Fatalf("hard cap exceeded: %v", err)
		}
		ready, err := runner.GrantCallAllowance(ctx, waiting.Run.ID, waiting.Run.Revision,
			agent.CallAllowance{LLMCalls: 1})
		if err != nil || ready.Run.Status != kernel.RunStatusRunning {
			t.Fatalf("grant %+v %v", ready.Run, err)
		}
	case "resume":
		ready, err := rt.Load(ctx, "native-budget")
		if err != nil {
			t.Fatal(err)
		}
		if ready.Run.Status != kernel.RunStatusRunning {
			t.Fatalf("missing grant: %+v", ready.Run)
		}
		done, err := runner.Resume(ctx, ready.Run.ID, ready.Run.Revision)
		if err != nil || done.Run.Status != kernel.RunStatusCompleted {
			t.Fatalf("native budget resume %+v %v", done.Run, err)
		}
	default:
		t.Fatalf("bad native phase: %s", phase)
	}
}

func TestNativeRunExecutionBudgetAcrossProcesses(t *testing.T) {
	base := privateDir(t)
	db := filepath.Join(base, "native-budget.sqlite")
	marker := filepath.Join(base, "native-budget-effects.txt")
	for _, phase := range []string{"start", "grant", "resume"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestNativeLeaseBudgetChild$")
		cmd.Env = append(os.Environ(), "GAOGE_NATIVE_BUDGET_PHASE="+phase,
			"GAOGE_NATIVE_BUDGET_DB="+db, "GAOGE_NATIVE_BUDGET_MARKER="+marker)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("native budget %s: %v output=%s", phase, err, out)
		}
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	modelCalls, toolCalls := 0, 0
	for _, entry := range strings.Fields(string(data)) {
		if entry == "model" {
			modelCalls++
		}
		if entry == "tool:lookup-call" {
			toolCalls++
		}
	}
	if modelCalls != 2 || toolCalls != 1 {
		t.Fatalf("native cross-process budget replay model=%d tool=%d markers=%q", modelCalls, toolCalls, data)
	}
}

package sqlite_test

import (
	"context"
	"encoding/json"
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
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type controlBudgetModel struct{ path string }

func (m controlBudgetModel) Generate(_ context.Context, r model.Request) (model.Response, error) {
	if err := appendMarker(m.path, "model"); err != nil {
		return model.Response{}, err
	}
	for _, msg := range r.Messages {
		if msg.Role == model.RoleTool {
			return model.Response{Content: "done after grant", Usage: &model.Usage{InputTokens: 2, OutputTokens: 1}}, nil
		}
	}
	return model.Response{ToolCalls: []tools.Call{{ID: "lookup-call", ToolKey: "lookup", Arguments: json.RawMessage(`{}`)}},
		Usage: &model.Usage{InputTokens: 2, OutputTokens: 1}}, nil
}

func controlBudgetRunner(rt *kernel.Runtime, markerFile string, lease ...*agent.RunExecution) (*agent.Runner, error) {
	var runExecution *agent.RunExecution
	if len(lease) > 0 {
		runExecution = lease[0]
	}
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: "lookup", Name: "Lookup", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
			if err := appendMarker(markerFile, "tool:"+r.Call.ID); err != nil {
				return tools.ExecutionResult{}, err
			}
			return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: r.Call.ID, Disposition: "committed"}}, nil
		}),
	}})
	if err != nil {
		return nil, err
	}
	return agent.NewRunner(agent.Dependencies{Runtime: rt, Model: controlBudgetModel{path: markerFile},
		Catalog: registry, Executor: registry, RunExecution: runExecution})
}

func TestFencedAllowanceProcessChild(t *testing.T) {
	phase := os.Getenv("GAOGE_STAGE2_ALLOWANCE_PHASE")
	if phase == "" {
		return
	}
	path := os.Getenv("GAOGE_STAGE2_ALLOWANCE_DB")
	markerFile := os.Getenv("GAOGE_STAGE2_ALLOWANCE_MARKER")
	leaseFile := os.Getenv("GAOGE_STAGE2_ALLOWANCE_LEASE")
	ctx := context.Background()
	var store *sqlite.Store
	var err error
	if phase == "start" {
		store, err = sqlite.CreateNew(ctx, path)
	} else {
		store, err = sqlite.OpenExisting(ctx, path)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	var runtime *kernel.Runtime
	var fencing *controlTestFencedCreateStore
	if phase == "start" {
		fencing = &controlTestFencedCreateStore{Store: store, execution: store}
		runtime, err = kernel.New(kernel.Dependencies{Store: fencing})
	} else {
		runtime, err = kernel.New(kernel.Dependencies{Store: store})
	}
	if err != nil {
		t.Fatal(err)
	}
	runner, err := controlBudgetRunner(runtime, markerFile)
	if err != nil {
		t.Fatal(err)
	}
	if phase == "start" {
		paused, e := runner.StartRun(ctx, agent.StartRequest{
			ID: "fenced-budget", Actor: kernel.ActorRef{TenantID: "local", ActorID: "profile"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
			Goal:   "do lookup then ask budget grant", Model: "fixture", ToolKeys: []string{"lookup"},
			CallAllowance: &agent.CallAllowance{LLMCalls: 1, ToolCalls: 2},
			Limits:        agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 4},
		})
		if e != nil || paused.Run.Status != kernel.RunStatusPausedBudget ||
			paused.Run.ErrorCode != "agent.model_allowance_exhausted" {
			t.Fatalf("starting fenced budget must pause after Tool: %+v %v", paused.Run, e)
		}
		data, e := json.Marshal(fencing.lease)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(leaseFile, data, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	paused, err := runtime.Load(ctx, "fenced-budget")
	if err != nil {
		t.Fatal(err)
	}
	if paused.Run.Status != kernel.RunStatusPausedBudget {
		t.Fatalf("unexpected phase: %+v", paused.Run)
	}
	if _, err = runner.GrantCallAllowance(ctx, paused.Run.ID, paused.Run.Revision, agent.CallAllowance{ToolCalls: 1}); !errors.Is(err, agent.ErrInvalidCallAllowance) {
		t.Fatalf("wrong dimension accepted: %v", err)
	}
	if _, err = runner.GrantCallAllowance(ctx, paused.Run.ID, paused.Run.Revision, agent.CallAllowance{LLMCalls: 6}); !errors.Is(err, agent.ErrInvalidCallAllowance) {
		t.Fatalf("hard ceiling expanded: %v", err)
	}
	granted, err := runner.GrantCallAllowance(ctx, paused.Run.ID, paused.Run.Revision, agent.CallAllowance{LLMCalls: 1})
	if err != nil || granted.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("budget control failed %+v %v", granted.Run, err)
	}
	if _, err = runner.GrantCallAllowance(ctx, paused.Run.ID, paused.Run.Revision, agent.CallAllowance{LLMCalls: 1}); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("replayed grant succeeded: %v", err)
	}
	leaseData, err := os.ReadFile(leaseFile)
	if err != nil {
		t.Fatal(err)
	}
	var old kernel.ExecutionLease
	if err = json.Unmarshal(leaseData, &old); err != nil {
		t.Fatal(err)
	}
	staleRun := granted.Run
	staleRun.Revision++
	_, err = store.ApplyExecution(ctx, old, granted.Run.Revision, kernel.StoreMutation{
		Record: kernel.Record{Run: staleRun, State: granted.State},
	})
	if !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("original executor committed after grant: %v", err)
	}
	newLease, err := runtime.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
		RunID: granted.Run.ID, OwnerID: "budget-resumer", LeaseDuration: 30 * time.Second,
	})
	if err != nil || newLease.Epoch <= old.Epoch {
		t.Fatalf("budget did not revoke old epoch: %+v %v", newLease, err)
	}
	bound, err := runtime.WithExecution(newLease)
	if err != nil {
		t.Fatal(err)
	}
	resumer, err := controlBudgetRunner(bound, markerFile)
	if err != nil {
		t.Fatal(err)
	}
	done, err := resumer.Resume(ctx, granted.Run.ID, granted.Run.Revision)
	if err != nil || done.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("fenced budget resume failed %+v %v", done.Run, err)
	}
}

func runFencedAllowanceChild(t *testing.T, phase, db, marker, lease string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestFencedAllowanceProcessChild$")
	cmd.Env = append(os.Environ(),
		"GAOGE_STAGE2_ALLOWANCE_PHASE="+phase, "GAOGE_STAGE2_ALLOWANCE_DB="+db,
		"GAOGE_STAGE2_ALLOWANCE_MARKER="+marker, "GAOGE_STAGE2_ALLOWANCE_LEASE="+lease,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s process: %v output=%s", phase, err, output)
	}
}

func TestFencedAgentAllowanceAcrossProcesses(t *testing.T) {
	base := privateDir(t)
	db := filepath.Join(base, "budget.sqlite")
	marker := filepath.Join(base, "budget-markers.txt")
	lease := filepath.Join(base, "budget-lease.json")
	runFencedAllowanceChild(t, "start", db, marker, lease)
	runFencedAllowanceChild(t, "grant", db, marker, lease)
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	modelCalls, toolCalls := 0, 0
	for _, line := range strings.Fields(string(data)) {
		if line == "model" {
			modelCalls++
		}
		if line == "tool:lookup-call" {
			toolCalls++
		}
	}
	if modelCalls != 2 || toolCalls != 1 {
		t.Fatalf("duplicate model/tool after fenced budget recovery: model=%d tools=%d", modelCalls, toolCalls)
	}
}

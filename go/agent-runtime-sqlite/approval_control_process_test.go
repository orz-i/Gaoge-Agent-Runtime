package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type controlTestApprovalPolicy struct{}

func (controlTestApprovalPolicy) Name() string { return "requires-approval" }
func (controlTestApprovalPolicy) Approval(context.Context, plugin.ToolInvocation) (plugin.ApprovalRequirement, error) {
	return plugin.ApprovalRequired, nil
}

type controlTestModel struct{ marker string }

func (m controlTestModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	if err := appendMarker(m.marker, "model"); err != nil {
		return model.Response{}, err
	}
	toolMessages := 0
	for _, msg := range request.Messages {
		if msg.Role == model.RoleTool {
			toolMessages++
		}
	}
	if toolMessages >= 2 {
		return model.Response{Content: "finished", Usage: &model.Usage{InputTokens: 2, OutputTokens: 1}}, nil
	}
	return model.Response{
		ToolCalls: []tools.Call{
			{ID: "first-call", ToolKey: "publish", Arguments: json.RawMessage(`{"order":1}`)},
			{ID: "second-call", ToolKey: "publish", Arguments: json.RawMessage(`{"order":2}`)},
		}, Usage: &model.Usage{InputTokens: 2, OutputTokens: 1},
	}, nil
}

type controlTestFencedCreateStore struct {
	kernel.Store
	execution kernel.ExecutionStore
	lease     kernel.ExecutionLease
}

func (s *controlTestFencedCreateStore) Create(ctx context.Context, record kernel.Record, events []kernel.EventDraft) (kernel.Snapshot, error) {
	snap, err := s.Store.Create(ctx, record, events)
	if err != nil {
		return snap, err
	}
	s.lease, err = s.execution.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
		RunID: record.Run.ID, OwnerID: "starter", LeaseDuration: 30 * time.Second,
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return snap, nil
}
func (s *controlTestFencedCreateStore) Apply(ctx context.Context, id string, revision uint64, mut kernel.StoreMutation) (kernel.Snapshot, error) {
	return s.execution.ApplyExecution(ctx, s.lease, revision, mut)
}

func controlTestRunner(rt *kernel.Runtime, markerPath string, deferResumption bool) (*agent.Runner, error) {
	approvals, err := interaction.New(rt)
	if err != nil {
		return nil, err
	}
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: "publish", Name: "Publish", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			if err := appendMarker(markerPath, "tool:"+request.Call.ID); err != nil {
				return tools.ExecutionResult{}, err
			}
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"ok":true}`), Receipt: tools.Receipt{
					ExecutionID: request.Call.ID, Disposition: "committed",
				},
			}, nil
		}),
	}})
	if err != nil {
		return nil, err
	}
	return agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: controlTestModel{marker: markerPath},
		Catalog: registry, Executor: registry,
		Approvals:        interactionadapter.New(approvals),
		ApprovalPolicies: []plugin.ApprovalPolicy{controlTestApprovalPolicy{}},
		DeferResumption:  true,
	})
}

func TestFencedApprovalProcessChild(t *testing.T) {
	phase := os.Getenv("GAOGE_STAGE2_APPROVAL_PHASE")
	if phase == "" {
		return
	}
	dbFile := os.Getenv("GAOGE_STAGE2_APPROVAL_DB")
	markerFile := os.Getenv("GAOGE_STAGE2_APPROVAL_MARKERS")
	leaseFile := os.Getenv("GAOGE_STAGE2_APPROVAL_LEASE")
	firstDecision := os.Getenv("GAOGE_STAGE2_APPROVAL_DECISION")
	ctx := context.Background()
	var store *sqlite.Store
	var err error
	if phase == "start" {
		store, err = sqlite.CreateNew(ctx, dbFile)
	} else {
		store, err = sqlite.OpenExisting(ctx, dbFile)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	var rt *kernel.Runtime
	var startStore *controlTestFencedCreateStore
	if phase == "start" {
		startStore = &controlTestFencedCreateStore{Store: store, execution: store}
		rt, err = kernel.New(kernel.Dependencies{Store: startStore})
	} else {
		rt, err = kernel.New(kernel.Dependencies{Store: store})
	}
	if err != nil {
		t.Fatal(err)
	}
	autoDrive := os.Getenv("GAOGE_STAGE2_APPROVAL_AUTODRIVE") == "1"
	runner, err := controlTestRunner(rt, markerFile, !autoDrive)
	if err != nil {
		t.Fatal(err)
	}
	if phase == "start" {
		waiting, e := runner.StartRun(ctx, agent.StartRequest{
			ID: "fenced-approval", Actor: kernel.ActorRef{TenantID: "local", ActorID: "profile"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
			Goal:   "require two independent approvals", Model: "fixture", ToolKeys: []string{"publish"},
		})
		if e != nil || waiting.Run.Status != kernel.RunStatusWaitingInput || waiting.Checkpoint == nil {
			t.Fatalf("fenced start: %+v checkpoint=%#v err=%v", waiting.Run, waiting.Checkpoint, e)
		}
		raw, e := json.Marshal(startStore.lease)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(leaseFile, raw, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	waiting, err := rt.Load(ctx, "fenced-approval")
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("unexpected phase %s: %+v", phase, waiting.Run)
	}
	if phase == "second" {
		// An earlier checkpoint cannot authorize the next Tool call, even
		// with the latest Run revision.
		wrong := kernel.ControlGuard{Kind: kernel.ControlApproval, CheckpointID: "old-checkpoint"}
		_, _, e := rt.ApplyControl(ctx, waiting.Run.ID, waiting.Run.Revision, wrong, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: waiting.State,
		}, nil)
		if !errors.Is(e, kernel.ErrInvalidInput) {
			t.Fatalf("stale checkpoint control unexpectedly authorized: %v", e)
		}
	}
	decision := plugin.ApprovalApprove
	if phase == "first" && firstDecision == "reject" {
		decision = plugin.ApprovalReject
	}
	beforeControl, err := os.ReadFile(markerFile)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := runner.ResolveApproval(ctx, waiting.Run.ID, waiting.Run.Revision, plugin.ApprovalResponse{Decision: decision})
	afterControl, readErr := os.ReadFile(markerFile)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(afterControl) != string(beforeControl) {
		t.Fatal("approval dispatched Model/Tool before acquiring a new execution owner")
	}
	if err != nil || resolved.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("fenced %s decision=%s status=%s err=%v", phase, decision, resolved.Run.Status, err)
	}
	// Control must invalidate the old execution identity, even though its
	// lease has not yet expired and no new owner has been acquired.
	raw, err := os.ReadFile(leaseFile)
	if err != nil {
		t.Fatal(err)
	}
	var firstLease kernel.ExecutionLease
	if err = json.Unmarshal(raw, &firstLease); err != nil {
		t.Fatal(err)
	}
	forgedRun := resolved.Run
	forgedRun.Revision++
	_, err = store.ApplyExecution(ctx, firstLease, resolved.Run.Revision, kernel.StoreMutation{
		Record: kernel.Record{Run: forgedRun, State: resolved.State},
	})
	if !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("revoked execution owner committed after %s control: %v", phase, err)
	}
	newLease, err := rt.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
		RunID: resolved.Run.ID, OwnerID: "after-" + phase, LeaseDuration: 30 * time.Second,
	})
	if err != nil || newLease.Epoch <= firstLease.Epoch {
		t.Fatalf("new execution not claimable after control: %+v %v", newLease, err)
	}
	bound, err := rt.WithExecution(newLease)
	if err != nil {
		t.Fatal(err)
	}
	resumedRunner, err := controlTestRunner(bound, markerFile, true)
	if err != nil {
		t.Fatal(err)
	}
	next, err := resumedRunner.Resume(ctx, resolved.Run.ID, resolved.Run.Revision)
	if err != nil {
		t.Fatalf("fenced resume after %s: %v", phase, err)
	}
	if phase == "first" {
		if next.Run.Status != kernel.RunStatusWaitingInput {
			t.Fatalf("second approval bypassed: %s", next.Run.Status)
		}
		if next.Checkpoint == nil || waiting.Checkpoint == nil || next.Checkpoint.ID == waiting.Checkpoint.ID {
			t.Fatalf("next Tool incorrectly reused first Checkpoint: %+v", next.Checkpoint)
		}
	} else if next.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("final Resume status=%s", next.Run.Status)
	}
}

func runFencedApprovalChild(t *testing.T, phase, decision, db, marker, lease string, autoDrive ...bool) {
	t.Helper()
	auto := "0"
	if len(autoDrive) > 0 && autoDrive[0] {
		auto = "1"
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestFencedApprovalProcessChild$")
	cmd.Env = append(os.Environ(),
		"GAOGE_STAGE2_APPROVAL_PHASE="+phase,
		"GAOGE_STAGE2_APPROVAL_DECISION="+decision,
		"GAOGE_STAGE2_APPROVAL_DB="+db,
		"GAOGE_STAGE2_APPROVAL_MARKERS="+marker,
		"GAOGE_STAGE2_APPROVAL_LEASE="+lease,
		"GAOGE_STAGE2_APPROVAL_AUTODRIVE="+auto,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("phase=%s decision=%s subprocess error=%v output=%s", phase, decision, err, output)
	}
}

func TestFencedAgentBatchApprovalAcrossThreeProcesses(t *testing.T) {
	for _, firstDecision := range []string{"approve", "reject"} {
		t.Run(firstDecision, func(t *testing.T) {
			dir := privateDir(t)
			db := filepath.Join(dir, "agent.sqlite")
			marker := filepath.Join(dir, "executions.txt")
			lease := filepath.Join(dir, "original-lease.json")
			runFencedApprovalChild(t, "start", firstDecision, db, marker, lease)
			runFencedApprovalChild(t, "first", firstDecision, db, marker, lease)
			result, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			calls := strings.Split(strings.TrimSpace(string(result)), "\n")
			firstTools := 0
			for _, call := range calls {
				if strings.HasPrefix(call, "tool:") {
					firstTools++
				}
			}
			if firstDecision == "approve" && firstTools != 1 {
				t.Fatalf("first approval must execute exactly one Tool: %v", calls)
			}
			if firstDecision == "reject" && firstTools != 0 {
				t.Fatalf("rejected first Tool executed: %v", calls)
			}
			runFencedApprovalChild(t, "second", firstDecision, db, marker, lease)
			result, err = os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Fields(string(result))
			toolCalls := make([]string, 0)
			modelCalls := 0
			for _, line := range lines {
				if strings.HasPrefix(line, "tool:") {
					toolCalls = append(toolCalls, line)
				}
				if line == "model" {
					modelCalls++
				}
			}
			expectedTools := 2
			if firstDecision == "reject" {
				expectedTools = 1
			}
			if len(toolCalls) != expectedTools || modelCalls != 2 {
				t.Fatalf("fenced three-process resume first=%s tools=%v want=%d models=%d want=2", firstDecision, toolCalls, expectedTools, modelCalls)
			}
			if len(toolCalls) > 0 && firstDecision == "reject" && toolCalls[0] != "tool:second-call" {
				t.Fatalf("wrong Tool executed after rejected first: %v", toolCalls)
			}
			if firstDecision == "approve" && fmt.Sprint(toolCalls) != fmt.Sprint([]string{"tool:first-call", "tool:second-call"}) {
				t.Fatalf("batch approvals reused or swapped calls: %v", toolCalls)
			}
		})
	}
}

// Explicit DeferResumption is NOT required to avoid an unsafe Tool dispatch:
// a ControlStore returns the fact that it revoked an old epoch, so the Runner
// returns the newly-running Snapshot before any model/tool side effect.
func TestFencedApprovalAutoDefersUntilNewClaim(t *testing.T) {
	dir := privateDir(t)
	db := filepath.Join(dir, "runtime.sqlite")
	markers := filepath.Join(dir, "executions.txt")
	lease := filepath.Join(dir, "first-lease.json")
	runFencedApprovalChild(t, "start", "approve", db, markers, lease, true)
	runFencedApprovalChild(t, "first", "approve", db, markers, lease, true)
	runFencedApprovalChild(t, "second", "approve", db, markers, lease, true)
	data, err := os.ReadFile(markers)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Fields(string(data))
	toolsDone := 0
	for _, v := range calls {
		if strings.HasPrefix(v, "tool:") {
			toolsDone++
		}
	}
	if toolsDone != 2 {
		t.Fatalf("automatic mode lost/doubled approved Tools: %v", calls)
	}
}

package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func nativeApprovalRunner(rt *kernel.Runtime, marker, owner string) (*agent.Runner, error) {
	approvals, err := interaction.New(rt)
	if err != nil {
		return nil, err
	}
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: "publish", Name: "Publish", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
			if err := appendMarker(marker, "tool:"+r.Call.ID); err != nil {
				return tools.ExecutionResult{}, err
			}
			return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: r.Call.ID, Disposition: "committed"}}, nil
		}),
	}})
	if err != nil {
		return nil, err
	}
	return agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: controlTestModel{marker: marker}, Catalog: registry, Executor: registry,
		Approvals:        interactionadapter.New(approvals),
		ApprovalPolicies: []plugin.ApprovalPolicy{controlTestApprovalPolicy{}},
		// This is the public native direct-Agent configuration; NO special
		// testing wrapper changes Store.Create/Apply.
		RunExecution: &agent.RunExecution{OwnerID: owner, LeaseDuration: 2 * time.Minute},
		// Deliberately keep DeferResumption=false to test the implicit safe defer
		// after an existing epoch is revoked by the Feature control.
	})
}

func TestNativeLeaseApprovalChild(t *testing.T) {
	phase := os.Getenv("GAOGE_NATIVE_APPROVAL_PHASE")
	if phase == "" {
		return
	}
	dbPath := os.Getenv("GAOGE_NATIVE_APPROVAL_DB")
	marker := os.Getenv("GAOGE_NATIVE_APPROVAL_MARKER")
	firstDecision := os.Getenv("GAOGE_NATIVE_APPROVAL_FIRST")
	ctx := context.Background()
	var store *sqlite.Store
	var err error
	if phase == "start" {
		store, err = sqlite.CreateNew(ctx, dbPath)
	} else {
		store, err = sqlite.OpenExisting(ctx, dbPath)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := nativeApprovalRunner(rt, marker, "phase-"+phase)
	if err != nil {
		t.Fatal(err)
	}
	switch phase {
	case "start":
		pending, err := runner.StartRun(ctx, agent.StartRequest{
			ID: "native-leased-agent", Actor: kernel.ActorRef{TenantID: "local", ActorID: "profile"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "two independent tool approvals",
			Model: "fixture", ToolKeys: []string{"publish"},
		})
		if err != nil || pending.Run.Status != kernel.RunStatusWaitingInput {
			t.Fatalf("native StartRun: %+v %v", pending.Run, err)
		}
		if err := rt.RequireExecution(ctx, pending.Run.ID); !errors.Is(err, kernel.ErrExecutionRequired) {
			t.Fatalf("native StartRun never acquired a sticky execution epoch: %v", err)
		}
	case "approve1", "approve2":
		pending, err := rt.Load(ctx, "native-leased-agent")
		if err != nil {
			t.Fatal(err)
		}
		if pending.Run.Status != kernel.RunStatusWaitingInput {
			t.Fatalf("expected approval gate: %+v", pending.Run)
		}
		before, err := os.ReadFile(marker)
		if err != nil {
			t.Fatal(err)
		}
		decision := plugin.ApprovalApprove
		if phase == "approve1" && firstDecision == "reject" {
			decision = plugin.ApprovalReject
		}
		ready, err := runner.ResolveApproval(ctx, pending.Run.ID, pending.Run.Revision, plugin.ApprovalResponse{Decision: decision})
		if err != nil || ready.Run.Status != kernel.RunStatusRunning {
			t.Fatalf("native ResolveApproval: %+v %v", ready.Run, err)
		}
		after, err := os.ReadFile(marker)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Fatalf("control dispatched external effects before new claim: %q -> %q", before, after)
		}
	case "conflict":
		running, err := rt.Load(ctx, "native-leased-agent")
		if err != nil {
			t.Fatal(err)
		}
		active, err := rt.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
			RunID: running.Run.ID, OwnerID: "simulated-in-flight-owner", LeaseDuration: time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(marker)
		if err != nil {
			t.Fatal(err)
		}
		_, err = runner.Resume(ctx, running.Run.ID, running.Run.Revision)
		if !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("competing RunExecution was not rejected: %v", err)
		}
		after, err := os.ReadFile(marker)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatalf("concurrent owner caused physical Tool effect")
		}
		if err = rt.ReleaseExecution(ctx, active); err != nil {
			t.Fatal(err)
		}
	case "resume1", "resume2":
		running, err := rt.Load(ctx, "native-leased-agent")
		if err != nil {
			t.Fatal(err)
		}
		if running.Run.Status != kernel.RunStatusRunning {
			t.Fatalf("missing approved state: %+v", running.Run)
		}
		next, err := runner.Resume(ctx, running.Run.ID, running.Run.Revision)
		if err != nil {
			t.Fatalf("native Resume: %v", err)
		}
		if phase == "resume1" && next.Run.Status != kernel.RunStatusWaitingInput {
			t.Fatalf("native Resume bypassed second approval: %+v", next.Run)
		}
		if phase == "resume2" && next.Run.Status != kernel.RunStatusCompleted {
			t.Fatalf("native Agent not completed: %+v", next.Run)
		}
	default:
		t.Fatalf("invalid native phase %s", phase)
	}
}

func nativeApprovalChild(t *testing.T, phase, decision, dbPath, marker string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestNativeLeaseApprovalChild$")
	cmd.Env = append(os.Environ(),
		"GAOGE_NATIVE_APPROVAL_PHASE="+phase, "GAOGE_NATIVE_APPROVAL_FIRST="+decision,
		"GAOGE_NATIVE_APPROVAL_DB="+dbPath, "GAOGE_NATIVE_APPROVAL_MARKER="+marker)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native phase %q: %v output=%s", phase, err, out)
	}
}

func TestNativeRunExecutionAcrossProcesses(t *testing.T) {
	for _, first := range []string{"approve", "reject"} {
		t.Run(first, func(t *testing.T) {
			dir := privateDir(t)
			dbPath := filepath.Join(dir, "native.sqlite")
			marker := filepath.Join(dir, "effects.txt")
			nativeApprovalChild(t, "start", first, dbPath, marker)
			nativeApprovalChild(t, "approve1", first, dbPath, marker)
			nativeApprovalChild(t, "conflict", first, dbPath, marker)
			nativeApprovalChild(t, "resume1", first, dbPath, marker)
			nativeApprovalChild(t, "approve2", first, dbPath, marker)
			nativeApprovalChild(t, "resume2", first, dbPath, marker)
			data, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Fields(string(data))
			var toolCalls []string
			models := 0
			for _, line := range lines {
				if strings.HasPrefix(line, "tool:") {
					toolCalls = append(toolCalls, line)
				}
				if line == "model" {
					models++
				}
			}
			expected := []string{"tool:first-call", "tool:second-call"}
			if first == "reject" {
				expected = []string{"tool:second-call"}
			}
			if models != 2 || strings.Join(toolCalls, ",") != strings.Join(expected, ",") {
				t.Fatalf("unexpected real native effects: models=%d tools=%v want=%v", models, toolCalls, expected)
			}
		})
	}
}

// auditClaimAdvancesRevision intercepts the native Acquire after Resume has
// loaded its candidate Snapshot, then persists an unrelated legal CAS under
// the just-issued lease. The caller now holds a VALID epoch but STALE revision.
type auditClaimAdvancesRevision struct {
	kernel.Store
	kernel.ExecutionStore
	did bool
}

func (w *auditClaimAdvancesRevision) AcquireExecution(ctx context.Context, req kernel.ExecutionAcquireRequest) (kernel.ExecutionLease, error) {
	lease, err := w.ExecutionStore.AcquireExecution(ctx, req)
	if err != nil {
		return lease, err
	}
	if w.did {
		return lease, nil
	}
	w.did = true
	fresh, err := w.Load(ctx, req.RunID)
	if err != nil {
		return kernel.ExecutionLease{}, err
	}
	rt, err := kernel.New(kernel.Dependencies{Store: w})
	if err != nil {
		return kernel.ExecutionLease{}, err
	}
	scoped, err := rt.WithExecution(lease)
	if err != nil {
		return kernel.ExecutionLease{}, err
	}
	_, err = scoped.Apply(ctx, req.RunID, fresh.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: fresh.State,
		Checkpoint: fresh.Checkpoint,
		Events:     []kernel.EventDraft{{Type: "simulated.concurrent.new-revision"}},
	})
	if err != nil {
		return kernel.ExecutionLease{}, err
	}
	return lease, nil
}

func TestNativeResumeReloadsSnapshotAfterAcquireBeforeTool(t *testing.T) {
	ctx := t.Context()
	dir := privateDir(t)
	db := filepath.Join(dir, "native-run.sqlite")
	marker := filepath.Join(dir, "effects.txt")
	store, err := sqlite.CreateNew(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	starter, err := nativeApprovalRunner(rt, marker, "starter")
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := starter.StartRun(ctx, agent.StartRequest{
		ID: "stale-after-claim", Actor: kernel.ActorRef{TenantID: "local", ActorID: "profile"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "two approvals",
		Model: "fixture", ToolKeys: []string{"publish"},
	})
	if err != nil || waiting.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("setup pending=%+v err=%v", waiting.Run, err)
	}
	ready, err := starter.ResolveApproval(ctx, waiting.Run.ID, waiting.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
	if err != nil || ready.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("setup ready=%+v err=%v", ready.Run, err)
	}
	before, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	wrapper := &auditClaimAdvancesRevision{Store: store, ExecutionStore: store}
	wrappedRT, err := kernel.New(kernel.Dependencies{Store: wrapper})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := nativeApprovalRunner(wrappedRT, marker, "candidate")
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := runner.Resume(ctx, ready.Run.ID, ready.Run.Revision)
	after, readErr := os.ReadFile(marker)
	if readErr != nil {
		t.Fatal(readErr)
	}
	loaded, readErr := rt.Load(ctx, ready.Run.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	effects := strings.Count(string(after), "tool:first-call") - strings.Count(string(before), "tool:first-call")
	t.Logf("AUDIT native Resume fresh lease BUT stale snapshot: committedRevision=%d callerRevision=%d physicalToolEffects=%d error=%v resumedStatus=%s",
		loaded.Run.Revision, ready.Run.Revision, effects, err, resumed.Run.Status)
	if effects != 0 || !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("stale snapshot executed external Tool: physical=%d err=%v", effects, err)
	}
	// A failed pre-dispatch revision check must release the newly claimed
	// segment, rather than blocking a valid recovery for the full TTL.
	recovered, claimErr := rt.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
		RunID: ready.Run.ID, OwnerID: "replacement", LeaseDuration: time.Second,
	})
	if claimErr != nil {
		t.Fatalf("stale Snapshot retained idle execution lease: %v", claimErr)
	}
	if err = rt.ReleaseExecution(ctx, recovered); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyRunCannotJoinNativeExecutionDuringTool(t *testing.T) {
	ctx := t.Context()
	profile := privateDir(t)
	store, err := sqlite.CreateNew(ctx, filepath.Join(profile, "mixed.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	approvals, err := interaction.New(rt)
	if err != nil {
		t.Fatal(err)
	}
	firstEntered := make(chan struct{})
	unblockFirst := make(chan struct{})
	var physical atomic.Int32
	reg, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: "publish", Name: "Publish", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(ctx context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
			ordinal := physical.Add(1)
			if ordinal == 1 {
				close(firstEntered)
				<-unblockFirst
			}
			return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: req.Call.ID, Disposition: "committed"}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(lease *agent.RunExecution) *agent.Runner {
		t.Helper()
		runner, e := agent.NewRunner(agent.Dependencies{
			Runtime: rt, Model: controlTestModel{marker: filepath.Join(profile, "model-calls.txt")},
			Catalog: reg, Executor: reg, Approvals: interactionadapter.New(approvals),
			ApprovalPolicies: []plugin.ApprovalPolicy{controlTestApprovalPolicy{}},
			DeferResumption:  true, RunExecution: lease,
		})
		if e != nil {
			t.Fatal(e)
		}
		return runner
	}
	legacy := mk(nil)
	waiting, err := legacy.StartRun(ctx, agent.StartRequest{
		ID: "mixed-opt-in", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "two tools", ToolKeys: []string{"publish"},
	})
	if err != nil || waiting.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("legacy start pending %+v %v", waiting.Run, err)
	}
	ready, err := legacy.ResolveApproval(ctx, waiting.Run.ID, waiting.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
	if err != nil || ready.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("legacy control %+v %v", ready.Run, err)
	}
	oldDone := make(chan error, 1)
	go func() { _, e := legacy.Resume(ctx, ready.Run.ID, ready.Run.Revision); oldDone <- e }()
	select {
	case <-firstEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("legacy tool did not start")
	}
	native := mk(&agent.RunExecution{OwnerID: "native-worker", LeaseDuration: 2 * time.Minute})
	next, nativeErr := native.Resume(ctx, ready.Run.ID, ready.Run.Revision)
	// Every Tool dispatch now advances the Agent Revision to commit its
	// immutable external-effect intent, even without a RunExecution lease.
	// First prove the stale caller is rejected by CAS, then re-read under
	// the same in-flight Legacy Tool and prove epoch-0 cannot be upgraded.
	current, currentErr := rt.Load(ctx, ready.Run.ID)
	if currentErr != nil {
		t.Fatal(currentErr)
	}
	_, freshNativeErr := native.Resume(ctx, ready.Run.ID, current.Run.Revision)
	_, forbiddenClaimErr := rt.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
		RunID: ready.Run.ID, OwnerID: "invalid-native-takeover", LeaseDuration: 2 * time.Minute,
	})
	close(unblockFirst)
	oldErr := <-oldDone
	t.Logf("AUDIT MIXED legacy epoch0 Tool already in flight; native claim accepted=%t status=%s physical_effects=%d stale_legacy_error=%v",
		nativeErr == nil, next.Run.Status, physical.Load(), oldErr)
	if !errors.Is(nativeErr, kernel.ErrConflict) ||
		!errors.Is(freshNativeErr, agent.ErrToolOutcomeUnknown) ||
		!errors.Is(forbiddenClaimErr, kernel.ErrExecutionRequired) ||
		physical.Load() != 1 || oldErr != nil {
		t.Fatalf("legacy Run was upgraded into concurrent native execution: effects=%d stale=%v fresh=%v storeClaim=%v legacy=%v",
			physical.Load(), nativeErr, freshNativeErr, forbiddenClaimErr, oldErr)
	}
}

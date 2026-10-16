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

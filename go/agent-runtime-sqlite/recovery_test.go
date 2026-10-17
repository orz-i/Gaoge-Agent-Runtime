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

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type fixtureModel struct {
	marker   string
	withTool bool
}

func (m fixtureModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	if err := appendMarker(m.marker, "model"); err != nil {
		return model.Response{}, err
	}
	if m.withTool {
		for _, message := range request.Messages {
			if message.Role == model.RoleTool {
				return model.Response{Content: "finished after tool", Usage: &model.Usage{InputTokens: 3, OutputTokens: 2}}, nil
			}
		}
		return model.Response{
			ToolCalls: []tools.Call{{ID: "call-1", ToolKey: "lookup", Arguments: json.RawMessage(`{}`)}},
			Usage:     &model.Usage{InputTokens: 3, OutputTokens: 2},
		}, nil
	}
	return model.Response{Content: "done", Usage: &model.Usage{InputTokens: 3, OutputTokens: 2}}, nil
}

func appendMarker(path, value string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintln(file, value); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

type failAfterCommitStore struct {
	kernel.Store
	point string
}

func (s failAfterCommitStore) Create(ctx context.Context, record kernel.Record, events []kernel.EventDraft) (kernel.Snapshot, error) {
	snap, err := s.Store.Create(ctx, record, events)
	if err == nil && s.point == "create" {
		os.Exit(75) // Crash after durable commit and before first drive.
	}
	return snap, err
}

func (s failAfterCommitStore) Apply(ctx context.Context, id string, rev uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	snap, err := s.Store.Apply(ctx, id, rev, m)
	if err != nil {
		return snap, err
	}
	for _, event := range m.Events {
		if s.point == "model_receipt" && event.Type == "agent.model_invocation.completed" {
			os.Exit(76) // Crash after durable model receipt, before consumption.
		}
		if s.point == "tool_receipt" && event.Type == "tool.completed" {
			os.Exit(77) // Crash after Tool receipt commit, before next step.
		}
	}
	return snap, err
}

// TestAgentCrashSubprocess runs in an isolated OS process. os.Exit simulates
// a killed app without any deferred Close, exactly as an embedded host may die.
func TestAgentCrashSubprocess(t *testing.T) {
	phase := os.Getenv("GAOGE_SQLITE_SUBPROCESS_PHASE")
	if phase == "" {
		return
	}
	path := os.Getenv("GAOGE_SQLITE_SUBPROCESS_DB")
	marker := os.Getenv("GAOGE_SQLITE_SUBPROCESS_MARKER")
	withTool := os.Getenv("GAOGE_SQLITE_SUBPROCESS_TOOL") == "1"
	var store *sqlite.Store
	var err error
	if phase == "resume" {
		store, err = sqlite.Open(context.Background(), path)
	} else {
		store, err = sqlite.CreateNew(context.Background(), path)
	}
	if err != nil {
		t.Fatal(err)
	}
	// No defer: crash phases must exit without running cleanup.
	var runtimeStore kernel.Store = store
	if phase != "resume" {
		runtimeStore = failAfterCommitStore{Store: store, point: phase}
	}
	runtime, err := kernel.New(kernel.Dependencies{Store: runtimeStore})
	if err != nil {
		t.Fatal(err)
	}
	var registry *tools.Registry
	if withTool {
		registry, err = tools.NewRegistry([]tools.Registration{{
			Definition: tools.Definition{
				Key: "lookup", Name: "Lookup", InputSchema: json.RawMessage(`{"type":"object"}`),
			},
			Handler: tools.HandlerFunc(func(ctx context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
				if err := appendMarker(marker, "tool"); err != nil {
					return tools.ExecutionResult{}, err
				}
				return tools.ExecutionResult{
					Content: json.RawMessage(`{"value":"ok"}`),
					Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"},
				}, nil
			}),
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: fixtureModel{marker: marker, withTool: withTool},
		Catalog: registry, Executor: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	if phase == "resume" {
		current, err := runtime.Load(context.Background(), "crash-agent")
		if err != nil {
			t.Fatal(err)
		}
		completed, err := runner.Resume(context.Background(), current.Run.ID, current.Run.Revision)
		if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
			t.Fatalf("resume=%+v err=%v", completed.Run, err)
		}
		_ = store.Close()
		return
	}
	request := agent.StartRequest{
		ID: "crash-agent", Actor: kernel.ActorRef{TenantID: "local", ActorID: "profile-1"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "complete durable work", Model: "fixture",
	}
	if withTool {
		request.ToolKeys = []string{"lookup"}
	}
	_, err = runner.StartRun(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	// The crash interceptor must have stopped this process.
	t.Fatal("crash interception did not occur")
}

func subprocess(t *testing.T, path, marker, phase string, withTool bool, expectedExit int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAgentCrashSubprocess$")
	toolFlag := "0"
	if withTool {
		toolFlag = "1"
	}
	cmd.Env = append(os.Environ(),
		"GAOGE_SQLITE_SUBPROCESS_PHASE="+phase,
		"GAOGE_SQLITE_SUBPROCESS_DB="+path,
		"GAOGE_SQLITE_SUBPROCESS_MARKER="+marker,
		"GAOGE_SQLITE_SUBPROCESS_TOOL="+toolFlag)
	output, err := cmd.CombinedOutput()
	if expectedExit == 0 {
		if err != nil {
			t.Fatalf("phase=%s err=%v output=%s", phase, err, output)
		}
		return
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != expectedExit {
		t.Fatalf("phase=%s exit=%v want=%d output=%s", phase, err, expectedExit, output)
	}
}

func TestAgentCrashAndResumeInNewProcess(t *testing.T) {
	for _, tc := range []struct {
		name          string
		crash         string
		exitCode      int
		withTool      bool
		models, tools int
	}{
		{name: "after-create", crash: "create", exitCode: 75, models: 1},
		{name: "after-model-receipt", crash: "model_receipt", exitCode: 76, models: 1},
		{name: "after-tool-receipt", crash: "tool_receipt", exitCode: 77, withTool: true, models: 2, tools: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(privateDir(t), "runtime.sqlite")
			marker := filepath.Join(privateDir(t), "calls.log")
			subprocess(t, path, marker, tc.crash, tc.withTool, tc.exitCode)
			// A different OS process (new Store, Kernel and Runner) resumes.
			subprocess(t, path, marker, "resume", tc.withTool, 0)
			data, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			var models, toolCalls int
			for _, line := range strings.Fields(string(data)) {
				switch line {
				case "model":
					models++
				case "tool":
					toolCalls++
				default:
					t.Fatalf("unexpected marker %q", line)
				}
			}
			if models != tc.models || toolCalls != tc.tools {
				t.Fatalf("replayed external effect: model calls=%d want=%d tool calls=%d want=%d", models, tc.models, toolCalls, tc.tools)
			}
			stored, err := sqlite.Open(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stored.Close() }()
			snapshot, err := stored.Load(t.Context(), "crash-agent")
			if err != nil || snapshot.Run.Status != kernel.RunStatusCompleted {
				t.Fatalf("reopened completion=%+v err=%v", snapshot.Run, err)
			}
		})
	}
}

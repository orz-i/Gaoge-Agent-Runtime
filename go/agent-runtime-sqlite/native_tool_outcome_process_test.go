package sqlite_test

import (
	"context"
	"database/sql"
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
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestNativeUnknownToolChild(t *testing.T) {
	phase := os.Getenv("GAOGE_NATIVE_UNKNOWN_PHASE")
	if phase == "" {
		return
	}
	path := os.Getenv("GAOGE_NATIVE_UNKNOWN_DB")
	marker := os.Getenv("GAOGE_NATIVE_UNKNOWN_MARKER")
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
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: "lookup", Name: "Lookup", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
			if err := appendMarker(marker, "tool:"+req.Call.ID); err != nil {
				return tools.ExecutionResult{}, err
			}
			if phase == "start" {
				db, openErr := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
				if openErr != nil {
					return tools.ExecutionResult{}, openErr
				}
				_, execErr := db.Exec(`UPDATE runtime_runs SET execution_expiry_ms=0 WHERE run_id=?`, req.RunID)
				closeErr := db.Close()
				if execErr != nil {
					return tools.ExecutionResult{}, execErr
				}
				if closeErr != nil {
					return tools.ExecutionResult{}, closeErr
				}
			}
			return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: req.Call.ID, Disposition: "committed"}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: controlBudgetModel{path: marker}, Catalog: registry, Executor: registry,
		RunExecution: &agent.RunExecution{OwnerID: "owner-" + phase, LeaseDuration: 2 * time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}
	switch phase {
	case "start":
		current, runErr := runner.StartRun(ctx, agent.StartRequest{
			ID: "native-unknown", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "external receipt uncertain",
			ToolKeys: []string{"lookup"},
		})
		if !errors.Is(runErr, kernel.ErrExecutionConflict) || current.Run.ID != "native-unknown" {
			t.Fatalf("injected post-effect CAS expiry not detected: run=%+v err=%v", current.Run, runErr)
		}
	case "blocked":
		current, loadErr := rt.Load(ctx, "native-unknown")
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		_, runErr := runner.Resume(ctx, current.Run.ID, current.Run.Revision)
		if !errors.Is(runErr, agent.ErrToolOutcomeUnknown) {
			t.Fatalf("ambiguous Tool automatically replayed: %v", runErr)
		}
	case "reconcile":
		current, loadErr := rt.Load(ctx, "native-unknown")
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		result := tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
			Receipt: tools.Receipt{ExecutionID: "lookup-call", Disposition: "committed"}}
		if _, invalid := runner.ReconcileToolOutcome(ctx, current.Run.ID, current.Run.Revision,
			"not-this-call", result); !errors.Is(invalid, agent.ErrInvalidRequest) {
			t.Fatalf("provider receipt not bound to exact stable Call: %v", invalid)
		}
		recorded, recordErr := runner.ReconcileToolOutcome(ctx, current.Run.ID, current.Run.Revision,
			"lookup-call", result)
		if recordErr != nil || recorded.Run.Revision != current.Run.Revision+1 {
			t.Fatalf("known external receipt could not be persisted: %+v %v", recorded.Run, recordErr)
		}
		done, doneErr := runner.Resume(ctx, recorded.Run.ID, recorded.Run.Revision)
		if doneErr != nil || done.Run.Status != kernel.RunStatusCompleted {
			t.Fatalf("reconciled native Agent did not complete: %+v %v", done.Run, doneErr)
		}
	default:
		t.Fatalf("unsupported child phase: %s", phase)
	}
}

func TestNativeUnknownToolReconcilesAcrossProcessesWithoutReplay(t *testing.T) {
	profile := privateDir(t)
	db := filepath.Join(profile, "runtime.sqlite")
	marker := filepath.Join(profile, "effects.txt")
	for _, phase := range []string{"start", "blocked", "reconcile"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestNativeUnknownToolChild$")
		cmd.Env = append(os.Environ(), "GAOGE_NATIVE_UNKNOWN_PHASE="+phase,
			"GAOGE_NATIVE_UNKNOWN_DB="+db, "GAOGE_NATIVE_UNKNOWN_MARKER="+marker)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("unknown-result phase %q: %v output=%s", phase, err, out)
		}
	}
	contents, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	models, physical := 0, 0
	for _, line := range strings.Fields(string(contents)) {
		if line == "model" {
			models++
		}
		if line == "tool:lookup-call" {
			physical++
		}
	}
	if models != 2 || physical != 1 {
		t.Fatalf("reconciled Tool was reexecuted: models=%d effects=%d markers=%q", models, physical, contents)
	}
}

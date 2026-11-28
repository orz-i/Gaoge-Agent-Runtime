package harnesssqlite_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesssqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-sqlite"
	kernelsqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile/testprofile"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

const workerPhase = "HARNESS_SQLITE_TEST_PHASE"
const workerProfile = "HARNESS_SQLITE_TEST_PROFILE"

func request() harness.StartRequest {
	return harness.StartRequest{
		HostThread: harness.HostRef{Kind: "conversation", ID: "thread"}, HostTurn: harness.HostRef{Kind: "turn", ID: "first"},
		Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal: "answer", Config: harness.ConfigSnapshot{Environment: harness.VersionRef{ID: "desktop", Revision: 1}, AgentSpec: harness.AgentSpec{Model: "fixture"}},
	}
}

type localModel struct {
	dir   string
	phase string
}

func (m localModel) Generate(ctx context.Context, _ model.Request) (model.Response, error) {
	// This append-only file represents the observable effect of a paid call. Any
	// second call leaves durable evidence even if a replay ignores the error.
	file, err := os.OpenFile(filepath.Join(m.dir, "model.calls"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return model.Response{}, err
	}
	_, writeErr := file.WriteString("call\n")
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return model.Response{}, err
	}
	if m.phase == "in-model" {
		fmt.Println("at-cut")
		<-ctx.Done()
		return model.Response{}, ctx.Err()
	}
	return model.Response{Content: "answer"}, nil
}

type receiptCut struct {
	*kernelsqlite.Store
}

func (s receiptCut) FinishChildExecution(ctx context.Context, grant kernel.ChildExecutionGrant, permit kernel.ChildExecutionSettlement) error {
	err := s.Store.FinishChildExecution(ctx, grant, permit)
	if err == nil {
		fmt.Println("at-cut")
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func runnerFor(t *testing.T, host harness.Store, runs *kernelsqlite.Store, client localModel) *harness.Runner {
	t.Helper()
	var execution kernel.Store = runs
	if client.phase == "after-receipt" {
		execution = receiptCut{runs}
	}
	rt, err := kernel.New(kernel.Dependencies{Store: execution})
	if err != nil {
		t.Fatal(err)
	}
	native, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client})
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(kernelsqlite.NewRunRelationStore(runs), nil)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: native, Store: host, Relations: relations})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func TestCrashWorker(t *testing.T) {
	phase, dir := os.Getenv(workerPhase), os.Getenv(workerProfile)
	if phase == "" {
		t.Skip("subprocess worker")
	}
	host, err := harnesssqlite.OpenExisting(t.Context(), filepath.Join(dir, "harness.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = host.Close() }()
	runs, err := kernelsqlite.OpenExisting(t.Context(), filepath.Join(dir, "kernel.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runs.Close() }()
	runner := runnerFor(t, host, runs, localModel{dir, phase})
	_, err = runner.StartAgentTurn(t.Context(), request())
	t.Fatalf("StartAgentTurn returned before cut: %v", err)
}

func TestRunnerRecoversOriginalDispatchAfterProcessKill(t *testing.T) {
	for _, phase := range []string{"in-model", "after-receipt"} {
		t.Run(phase, func(t *testing.T) {
			dir := testprofile.Dir(t)
			hostPath, runPath := filepath.Join(dir, "harness.sqlite"), filepath.Join(dir, "kernel.sqlite")
			host, err := harnesssqlite.CreateNew(t.Context(), hostPath)
			if err != nil {
				t.Fatal(err)
			}
			authority, err := host.ManagedRecoveryAuthority(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err = host.Close(); err != nil {
				t.Fatal(err)
			}
			runs, err := kernelsqlite.CreateNew(t.Context(), runPath)
			if err != nil {
				t.Fatal(err)
			}
			if err = runs.Close(); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCrashWorker$")
			cmd.Env = append(os.Environ(), workerPhase+"="+phase, workerProfile+"="+dir)
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			lines := bufio.NewScanner(out)
			var log strings.Builder
			atCut := false
			for lines.Scan() {
				if lines.Text() == "at-cut" {
					atCut = true
					break
				}
				fmt.Fprintln(&log, lines.Text())
			}
			if !atCut {
				t.Fatalf("worker missed cut: %s, scan=%v, wait=%v", log.String(), lines.Err(), cmd.Wait())
			}
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			var killed *exec.ExitError
			if err = cmd.Wait(); !errors.As(err, &killed) {
				t.Fatalf("worker exit: %v", err)
			}

			host, err = harnesssqlite.OpenExisting(t.Context(), hostPath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = host.Close() }()
			runs, err = kernelsqlite.OpenExisting(t.Context(), runPath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = runs.Close() }()
			after, err := host.ManagedRecoveryAuthority(t.Context())
			if err != nil || after != authority {
				t.Fatalf("authority changed: %+v %v", after, err)
			}
			runner := runnerFor(t, host, runs, localModel{dir: dir})
			req := request()
			sessionID, err := harness.SessionID(req.HostThread)
			if err != nil {
				t.Fatal(err)
			}
			turnID, err := harness.TurnID(sessionID, req.HostTurn)
			if err != nil {
				t.Fatal(err)
			}
			before, err := host.ListItems(t.Context(), turnID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range before {
				if item.ParentItemID == harness.ExecutionDispatchParentItemID {
					t.Fatal("native root acquired Host dispatch")
				}
			}
			owners, err := host.ListInvocations(t.Context(), turnID)
			if err != nil || len(owners) != 1 {
				t.Fatalf("original invocation: %v %+v", err, owners)
			}
			runID := owners[0].ExecutionRefID
			grant, grantErr := runs.LookupChildExecution(t.Context(), runID)
			receipt, receiptErr := runs.LookupOwnedRetirementForRun(t.Context(), runID)
			returned := phase == "after-receipt"
			if returned {
				if !errors.Is(grantErr, kernel.ErrNotFound) || receiptErr != nil || !receipt.Valid() ||
					!receipt.Evidence.Completion.HostCompleted || receipt.Evidence.Snapshot.Run.Status != kernel.RunStatusCompleted {
					t.Fatalf("original retirement missing: %v %v %+v", grantErr, receiptErr, receipt)
				}
			} else if grantErr != nil || !grant.IsOwned() || !errors.Is(receiptErr, kernel.ErrNotFound) {
				t.Fatalf("unknown original grant: %v %v %+v", grantErr, receiptErr, grant)
			}
			snap, replayErr := runner.StartAgentTurn(t.Context(), req)
			if returned && (replayErr != nil || snap.Turn.Status != harness.TurnCompleted) {
				t.Fatalf("recover committed return: %s %v", snap.Turn.Status, replayErr)
			}
			if !returned && (replayErr != nil || snap.Turn.Status != harness.TurnRunning) {
				t.Fatalf("recover pending dispatch: %s %v", snap.Turn.Status, replayErr)
			}
			items, err := host.ListItems(t.Context(), turnID, 0, 100)
			if err != nil || harness.PendingExecutionDispatch(items, owners[0].ID) {
				t.Fatalf("replay acquired Host authority: %+v %v", items, err)
			}
			for _, item := range items {
				if item.ParentItemID == harness.ExecutionDispatchParentItemID {
					t.Fatal("replay created Host dispatch")
				}
			}
			if returned {
				again, err := runs.LookupOwnedRetirementForRun(t.Context(), runID)
				if err != nil || kernel.ManagedDigest(again) != kernel.ManagedDigest(receipt) {
					t.Fatalf("replay changed retirement: %v", err)
				}
			} else if again, err := runs.LookupChildExecution(t.Context(), runID); err != nil || again != grant {
				t.Fatalf("replay changed unknown original grant: %v", err)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "model.calls"))
			if err != nil || string(calls) != "call\n" {
				t.Fatalf("physical calls after replay: %q %v", calls, err)
			}
		})
	}
}

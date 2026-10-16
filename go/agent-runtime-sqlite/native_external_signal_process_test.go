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
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestNativeExternalSignalChild(t *testing.T) {
	phase := os.Getenv("GAOGE_NATIVE_SIGNAL_PHASE")
	if phase == "" {
		return
	}
	path := os.Getenv("GAOGE_NATIVE_SIGNAL_DB")
	marker := os.Getenv("GAOGE_NATIVE_SIGNAL_MARKER")
	hostInbox := os.Getenv("GAOGE_NATIVE_SIGNAL_HOST_INBOX")
	early := os.Getenv("GAOGE_NATIVE_SIGNAL_EARLY") == "1"
	var earlyErr error
	var runner *agent.Runner
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
	catalog, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: "lookup", Name: "Lookup", InputSchema: json.RawMessage(`{"type":"object"}`), Terminal: true},
		Handler: tools.HandlerFunc(func(_ context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
			if e := appendMarker(marker, "tool:"+req.Call.ID); e != nil {
				return tools.ExecutionResult{}, e
			}
			if early {
				signal := agent.ExternalToolResultSignal{
					RunID: req.RunID, CallID: req.Call.ID, OperationID: "external-op-1",
					InputDigest: strings.Repeat("a", 64), SignalID: "ready-1",
				}
				raw, e := json.Marshal(signal)
				if e != nil {
					return tools.ExecutionResult{}, e
				}
				// Simulate a durable HOST-side delivery record. It must be
				// retained when the ToolWait is not committed yet.
				if e = os.WriteFile(hostInbox, raw, 0600); e != nil {
					return tools.ExecutionResult{}, e
				}
				_, earlyErr = runner.NotifyExternalToolResultReady(ctx, signal)
				if !errors.Is(earlyErr, agent.ErrToolSignalNotReady) {
					return tools.ExecutionResult{}, errors.New("native early signal wasn't retryable")
				}
			}
			until := time.Now().UTC().Add(2 * time.Minute)
			return tools.ExecutionResult{Content: json.RawMessage(`null`),
				Receipt: tools.Receipt{ExecutionID: req.Call.ID, Disposition: tools.ReceiptDispositionPending},
				Pending: &tools.PendingExecution{Kind: tools.PendingExternalResult, OperationID: "external-op-1",
					InputDigest: strings.Repeat("a", 64), DeadlineAt: &until}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	verifier := agent.ToolOutcomeVerifierFunc(func(_ context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
		data, e := os.ReadFile(marker)
		if e != nil {
			return tools.ExecutionResult{}, e
		}
		if req.RunID != "native-external" || req.Call.ID != "lookup-call" ||
			strings.Count(string(data), "tool:lookup-call") != 1 {
			return tools.ExecutionResult{}, errors.New("provider has no committed receipt")
		}
		return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
			Receipt: tools.Receipt{ExecutionID: req.Call.ID, Disposition: "committed"}}, nil
	})
	runner, err = agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: controlBudgetModel{path: marker}, Catalog: catalog, Executor: catalog,
		RunExecution:        &agent.RunExecution{OwnerID: "signal-" + phase, LeaseDuration: 2 * time.Minute},
		ToolOutcomeVerifier: verifier,
	})
	if err != nil {
		t.Fatal(err)
	}
	switch phase {
	case "start":
		snap, e := runner.StartRun(ctx, agent.StartRequest{
			ID: "native-external", Actor: kernel.ActorRef{TenantID: "profile", ActorID: "a"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "pending externally",
			ToolKeys: []string{"lookup"},
		})
		if e != nil || snap.Run.Status != kernel.RunStatusRunning {
			t.Fatalf("start external wait: %+v %v", snap.Run, e)
		}
		pending, e := runner.QueryPendingTool(ctx, snap.Run.ID, "lookup-call")
		if e != nil || pending.Kind != tools.PendingExternalResult || pending.Generation == 0 {
			t.Fatalf("external wait wasn't persisted: %+v %v", pending, e)
		}
		if early {
			if !errors.Is(earlyErr, agent.ErrToolSignalNotReady) {
				t.Fatalf("expected early refusal: %v", earlyErr)
			}
			if _, e = os.Stat(hostInbox); e != nil {
				t.Fatalf("host dropped rejected early signal: %v", e)
			}
		}
	case "signal":
		snap, e := rt.Load(ctx, "native-external")
		if e != nil {
			t.Fatal(e)
		}
		signal := agent.ExternalToolResultSignal{
			RunID: snap.Run.ID, CallID: "lookup-call", OperationID: "external-op-1",
			InputDigest: strings.Repeat("a", 64), SignalID: "ready-1",
		}
		if early {
			raw, readErr := os.ReadFile(hostInbox)
			if readErr != nil {
				t.Fatalf("external host signal not durable: %v", readErr)
			}
			if readErr = json.Unmarshal(raw, &signal); readErr != nil {
				t.Fatal(readErr)
			}
		}
		next, e := runner.NotifyExternalToolResultReady(ctx, signal)
		if e != nil || next.Run.Revision != snap.Run.Revision+1 {
			t.Fatalf("fenced cross-process ready CAS: %+v %v", next.Run, e)
		}
		if early {
			// ACK the HOST delivery only after the Run + wakeup CAS commits.
			if e = os.Remove(hostInbox); e != nil {
				t.Fatalf("host couldn't ACK signal: %v", e)
			}
		}
		duplicate, e := runner.NotifyExternalToolResultReady(ctx, signal)
		if e != nil || duplicate.Run.Revision != next.Run.Revision {
			t.Fatalf("duplicate signal not idempotent: %+v %v", duplicate.Run, e)
		}
		pending, e := runner.QueryPendingTool(ctx, snap.Run.ID, "lookup-call")
		if e != nil || pending.SignalID != "ready-1" {
			t.Fatalf("signal not durable: %+v %v", pending, e)
		}
	case "project-abort", "project-retry":
		snap, e := rt.Load(ctx, "native-external")
		if e != nil {
			t.Fatal(e)
		}
		wait, e := runner.QueryPendingTool(ctx, snap.Run.ID, "lookup-call")
		if e != nil || wait.SignalID != "ready-1" {
			t.Fatalf("expected ready wait before delivery: %+v %v", wait, e)
		}
		now := time.Now().UTC()
		worker := "projector-abort"
		if phase == "project-retry" {
			worker = "projector-retry"
			now = now.Add(2 * time.Second)
		}
		claims, e := store.ClaimTransitions(ctx, kernel.TransitionClaimRequest{
			WorkerID: worker, Limit: 128, LeaseDuration: time.Second, Now: now,
		})
		if e != nil {
			t.Fatal(e)
		}
		seen := 0
		for _, claim := range claims {
			for _, event := range claim.Transition.Events {
				if event.Type != agent.EventExternalToolResultReady {
					continue
				}
				seen++
				if !event.Wakeup || event.WakeupGeneration != wait.Generation {
					t.Fatalf("ready work lost wait generation: %+v", event)
				}
				if phase == "project-abort" {
					old := kernel.TransitionLeaseRequest{
						TransitionID: claim.Transition.ID, LeaseID: claim.LeaseID, WorkerID: claim.WorkerID,
					}
					encoded, marshalErr := json.Marshal(old)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					if writeErr := os.WriteFile(filepath.Join(filepath.Dir(path), "old-projector-claim.json"), encoded, 0600); writeErr != nil {
						t.Fatalf("failed to persist simulated old projector claim: %v", writeErr)
					}
				}
				if phase == "project-retry" {
					oldJSON, readErr := os.ReadFile(filepath.Join(filepath.Dir(path), "old-projector-claim.json"))
					if readErr != nil {
						t.Fatal(readErr)
					}
					var old kernel.TransitionLeaseRequest
					if err = json.Unmarshal(oldJSON, &old); err != nil {
						t.Fatal(err)
					}
					if ackErr := store.AckTransition(ctx, old); !errors.Is(ackErr, kernel.ErrConflict) {
						t.Fatalf("stale projector worker ACKed newer lease: %v", ackErr)
					}
					if claim.Transition.Attempts != 2 {
						t.Fatalf("unACKed ready work wasn't reclaimed after process death: %+v", claim)
					}
					// An outbox lease is NOT Tool execution authority. This
					// delivery may be retried, but it must never call Tool.
					if _, resumeErr := runner.ResumeToolWait(ctx, snap.Run.ID, snap.Run.Revision, wait.Generation); !errors.Is(resumeErr, agent.ErrToolOutcomeUnknown) {
						t.Fatalf("ready-work delivery re-executed Tool: %v", resumeErr)
					}
					if ackErr := store.AckTransition(ctx, kernel.TransitionLeaseRequest{
						TransitionID: claim.Transition.ID, LeaseID: claim.LeaseID, WorkerID: claim.WorkerID,
					}); ackErr != nil {
						t.Fatalf("confirmed ready projection wasn't ACKed: %v", ackErr)
					}
				}
			}
		}
		if seen != 1 {
			t.Fatalf("expected exactly one durable readiness transition, got %d claims=%d", seen, len(claims))
		}
	case "reconcile":
		snap, e := rt.Load(ctx, "native-external")
		if e != nil {
			t.Fatal(e)
		}
		pending, e := runner.QueryPendingTool(ctx, snap.Run.ID, "lookup-call")
		if e != nil || pending.SignalID != "ready-1" {
			t.Fatalf("signal lost after process exit: %+v %v", pending, e)
		}
		if _, e = runner.ResumeToolWait(ctx, snap.Run.ID, snap.Run.Revision, pending.Generation); !errors.Is(e, agent.ErrToolOutcomeUnknown) {
			t.Fatalf("signal replayed external handler: %v", e)
		}
		done, e := runner.ReconcileToolOutcome(ctx, snap.Run.ID, snap.Run.Revision, "lookup-call")
		if e != nil || done.Run.Status != kernel.RunStatusCompleted {
			t.Fatalf("verified external result didn't complete: %+v %v", done.Run, e)
		}
	default:
		t.Fatalf("unknown phase %s", phase)
	}
}

func TestNativeExternalReadySignalAcrossProcesses(t *testing.T) {
	runNativeExternalSignalScenario(t, false)
}

func TestNativeEarlyExternalSignalRedeliveredAcrossProcesses(t *testing.T) {
	runNativeExternalSignalScenario(t, true)
}

func runNativeExternalSignalScenario(t *testing.T, early bool) {
	t.Helper()
	dir := privateDir(t)
	path := filepath.Join(dir, "native-external.sqlite")
	marker := filepath.Join(dir, "effects.txt")
	hostInbox := filepath.Join(dir, "host-pending-signal.json")
	earlyEnv := "0"
	if early {
		earlyEnv = "1"
	}
	// Simulate projector process death *after* Claim but *before* ACK;
	// a second projector reclaims the same committed work after lease TTL.
	for _, phase := range []string{"start", "signal", "project-abort", "project-retry", "reconcile"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestNativeExternalSignalChild$")
		cmd.Env = append(os.Environ(), "GAOGE_NATIVE_SIGNAL_PHASE="+phase,
			"GAOGE_NATIVE_SIGNAL_DB="+path, "GAOGE_NATIVE_SIGNAL_MARKER="+marker,
			"GAOGE_NATIVE_SIGNAL_EARLY="+earlyEnv, "GAOGE_NATIVE_SIGNAL_HOST_INBOX="+hostInbox)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("native external phase %s: %v output=%s", phase, err, output)
		}
	}
	contents, e := os.ReadFile(marker)
	if e != nil {
		t.Fatal(e)
	}
	models, effects := 0, 0
	for _, line := range strings.Fields(string(contents)) {
		if line == "model" {
			models++
		}
		if line == "tool:lookup-call" {
			effects++
		}
	}
	if models != 1 || effects != 1 {
		// This fixture uses a Terminal Tool; reconciliation completes directly.
		t.Fatalf("unexpected external calls after signal: model=%d tool=%d trace=%q", models, effects, contents)
	}
}

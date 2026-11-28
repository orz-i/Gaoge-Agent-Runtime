package continuatione2e

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

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness/conformance"
	postgresadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
	"gorm.io/gorm"
)

type managedFrontierProcessStore struct {
	*postgresadapter.KernelStore
	event  string
	before bool
}

func (s *managedFrontierProcessStore) Commit(commitCtx context.Context, commitID string, commitRev uint64, commitMutation kernel.StoreMutation, commitAuth kernel.CommitAuthority) (kernel.Snapshot, bool, error) {
	if commitAuth.Lease != nil {
		hit := false
		for _, e := range commitMutation.Events {
			if e.Type == s.event {
				hit = true
			}
		}
		if hit && s.before {
			os.Exit(managedRecoveryCrashExit)
		}
		out, _, err := s.KernelStore.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
		if err == nil && hit {
			os.Exit(managedRecoveryCrashExit)
		}
		return out, false, err
	}
	return s.KernelStore.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
}

type managedFrontierProcessModel struct {
	files, phase string
}

func (m managedFrontierProcessModel) Generate(_ context.Context, req model.Request) (model.Response, error) {
	if err := appendManagedRecoveryRecord(filepath.Join(m.files, "model.jsonl"), req); err != nil {
		return model.Response{}, err
	}
	if m.phase == "unknown" {
		os.Exit(managedRecoveryCrashExit)
	}
	if m.phase == "warmup" || strings.HasPrefix(m.phase, "continued/") {
		return model.Response{Content: "original continuation", Continue: true}, nil
	}
	if strings.HasPrefix(m.phase, "tool_completed/") {
		return model.Response{ToolCalls: []tools.Call{{ID: "original-tool", ToolKey: "fixture.lookup", Arguments: json.RawMessage(`{}`)}}}, nil
	}
	if strings.HasPrefix(m.phase, "input_applied/") {
		if err := appendManagedRecoveryRecord(filepath.Join(m.files, "inputs.jsonl"), agent.RunInput{Sequence: 1, ID: "original-steering", Text: "frontier steering"}); err != nil {
			return model.Response{}, err
		}
	}
	return model.Response{Content: "complete"}, nil
}
func openManagedFrontierProcess(t *testing.T, db *gorm.DB, files, phase string) *harness.SessionClient {
	t.Helper()
	host, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	var ks kernel.Store = postgresadapter.NewKernelStore(db)
	if phase != "resume" && phase != "unknown" && phase != "warmup" {
		event, when, _ := strings.Cut(phase, "/")
		if event == "cancel" {
			event = "pending"
		}
		switch event {
		case "input_applied", "initial_input":
			event = "agent.input_applied"
		case "initial_intent", "resume_intent":
			event = "agent.model_invocation.pending"
		case "resume_receipt":
			event = "agent.model_invocation.completed"
		case "resume_unknown":
			event = "agent.model_invocation.dispatch_intent"
		case "continued":
			event = "agent.model_continued"
		case "tool_completed":
			event = "tool.completed"
		default:
			event = "agent.model_invocation." + event
		}
		ks = &managedFrontierProcessStore{KernelStore: postgresadapter.NewKernelStore(db), event: event, before: when == "before"}
	}
	rt, err := kernel.New(kernel.Dependencies{Store: ks, ManagedRecoveryHost: host})
	if err != nil {
		t.Fatal(err)
	}
	options := managedRecoverySessionOptions()
	options.Config = harness.ConfigSnapshot{AgentSpec: harness.AgentSpec{Model: "fixture", MaxModelOutputTokens: 128}, SharedBudget: &budget.Limits{MaxLLMCalls: 3}}
	if phase != "resume" {
		options.Transcript = harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
			return nil, nil
		})
	}
	var local []harness.SessionTool
	if _, e := os.Stat(filepath.Join(files, "tool.jsonl")); e == nil {
		options.Config.SharedBudget.MaxToolCalls = 2
		local = []harness.SessionTool{{Registration: tools.Registration{
			Definition: tools.Definition{Key: "fixture.lookup", Name: "fixture lookup", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)},
			Handler: tools.HandlerFunc(func(_ context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
				if err := appendManagedRecoveryRecord(filepath.Join(files, "tool.jsonl"), r); err != nil {
					return tools.ExecutionResult{}, err
				}
				return tools.ExecutionResult{Content: json.RawMessage(`{"original":"evidence"}`), Receipt: tools.Receipt{ExecutionID: r.Call.ID, Disposition: "read"}}, nil
			})}, Policy: harness.ToolPolicySnapshot{Key: "fixture.lookup", DefinitionVersion: "fixture-v1", ApprovalMode: "never", SideEffectLevel: "read"}}}
	} else if !os.IsNotExist(e) {
		t.Fatal(e)
	}
	execution, leaseDuration := conformance.ManagedFixtureExecutionPolicy()
	session, err := harness.OpenAgentSession(harness.AgentSessionOptions{
		LocalTools: local,
		Runtime:    rt, Store: host, Model: managedFrontierProcessModel{files: files, phase: phase}, Inputs: managedRecoveryFileInputs{path: filepath.Join(files, "inputs.jsonl")},
		Session: options, Context: harness.AgentSessionContextOptions{ResolveWindow: func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
			return runtimecontext.ModelWindow{ContextTokens: 4096, ReservedOutputTokens: 128}, nil
		}},
		Execution:    execution,
		RunExecution: &agent.RunExecution{OwnerID: "frontier-" + phase, LeaseDuration: leaseDuration},
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}
func TestManagedFrontierProcess(t *testing.T) {
	phase := os.Getenv("GAOGE_TEST_MANAGED_FRONTIER_PHASE")
	if phase == "" {
		t.Skip("subprocess helper")
	}
	db := openManagedRecoveryDB(t, os.Getenv("TEST_POSTGRES_DSN"))
	session := openManagedFrontierProcess(t, db, os.Getenv("GAOGE_TEST_MANAGED_FRONTIER_FILES"), phase)
	var err error
	if strings.HasPrefix(phase, "resume_") {
		_, err = session.Resume(t.Context(), managedRecoveryTurn)
	} else {
		_, err = session.Prompt(t.Context(), managedRecoveryTurn, "original task")
	}
	t.Fatalf("expected hard process exit: %v", err)
}

func TestRealPostgresManagedOriginalFrontierMatrix(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	for _, phase := range []string{"claimed/before", "claimed/after", "completed/before", "completed/after", "input_applied/before", "input_applied/after", "cancel/after", "unknown", "dispatch_intent/after", "initial_input/before", "initial_input/after", "initial_intent/before", "initial_intent/after", "continued/before", "continued/after", "tool_completed/after", "resume_intent/before", "resume_intent/after", "resume_receipt/after", "resume_unknown/after"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			db, isolated := managedRecoveryDatabase(t, dsn)
			if err := postgresadapter.Migrate(db); err != nil {
				t.Fatal(err)
			}
			if err := harnesspostgres.Migrate(db); err != nil {
				t.Fatal(err)
			}
			files := t.TempDir()
			for _, name := range []string{"inputs.jsonl", "model.jsonl"} {
				if err := os.WriteFile(filepath.Join(files, name), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			nextDispatch := strings.HasPrefix(phase, "resume_")
			initial := strings.HasPrefix(phase, "initial_")
			continued := strings.HasPrefix(phase, "continued/")
			tool := strings.HasPrefix(phase, "tool_completed/")
			if tool {
				if err := os.WriteFile(filepath.Join(files, "tool.jsonl"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if initial {
				if err := appendManagedRecoveryRecord(filepath.Join(files, "inputs.jsonl"), agent.RunInput{Sequence: 1, ID: "initial-steering", Text: "initial steering"}); err != nil {
					t.Fatal(err)
				}
			}
			var prior kernel.ManagedTransfer
			if nextDispatch {
				first, e := openManagedFrontierProcess(t, db, files, "warmup").Prompt(ctx, managedRecoveryTurn, "original task")
				if e != nil || first.Turn.Status != harness.TurnRunning {
					t.Fatalf("ordinary first dispatch did not yield: %v %s", e, first.Turn.Status)
				}
				root, ok := harness.TopLevelInvocation(first)
				if !ok || harness.PendingExecutionDispatch(first.Items, root.ID) {
					t.Fatal("original Start dispatch not retired")
				}
				prior, e = postgresadapter.NewKernelStore(db).LoadManagedRecovery(ctx, root.ExecutionRefID)
				if e != nil || prior.SafePoint.Host.Phase != "start" {
					t.Fatalf("missing original Start point: %v", e)
				}
			}
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManagedFrontierProcess$", "-test.count=1")
			cmd.Env = append(os.Environ(), "TEST_POSTGRES_DSN="+isolated, "GAOGE_TEST_MANAGED_FRONTIER_PHASE="+phase, "GAOGE_TEST_MANAGED_FRONTIER_FILES="+files)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != managedRecoveryCrashExit {
				t.Fatalf("expected exit at %s: %v\n%s", phase, err, out)
			}
			host, err := harnesspostgres.New(db)
			if err != nil {
				t.Fatal(err)
			}
			turnID, root := managedRecoveryRoot(t, host)
			ks := postgresadapter.NewKernelStore(db)
			safe, err := ks.LoadManagedRecovery(ctx, root.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			original, err := ks.Load(ctx, root.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			before, err := agent.ViewState(original)
			wantInv := 1
			if initial && phase != "initial_intent/after" {
				wantInv = 0
			}
			if nextDispatch && phase != "resume_intent/before" {
				wantInv = 2
			}
			if err != nil || len(before.ModelInvocations) != wantInv {
				t.Fatalf("wrong cut: %+v %v", before, err)
			}
			if nextDispatch {
				b := safe.SafePoint.Host
				g, e := ks.LookupChildExecution(ctx, root.ExecutionRefID)
				if e != nil || b.Phase != "resume" || b.DispatchID == prior.SafePoint.Host.DispatchID || !b.MatchesOriginalGrant(g) || g != safe.SafePoint.OriginalGrant {
					t.Fatalf("new Resume did not own its exact point: %v %s", e, b.Phase)
				}
				old, e := ks.LoadManagedRecoveryByID(ctx, prior.SafePoint.ID)
				if e != nil || old.SafePoint.OriginalGrant != prior.SafePoint.OriginalGrant {
					t.Fatalf("lost historical Start identity: %v", e)
				}
			}
			if initial {
				wantCursor := uint64(1)
				if phase == "initial_input/before" {
					wantCursor = 0
				}
				proof, ok := safe.SafePoint.AgentObligations()
				if before.InputCursor != wantCursor || !ok || !kernel.ManagedSourceMatches(safe.SafePoint, original) ||
					(wantInv == 0 && (proof.Phase != "prepare" || proof.InvocationID != "" || proof.PreparationContext == nil)) {
					t.Fatalf("wrong initial frontier: %+v", proof)
				}
			}
			if phase == "continued/after" || tool {
				proof, ok := safe.SafePoint.AgentObligations()
				if !ok || proof.Phase != "prepare" || proof.PreparationTransitionDigest == "" || !kernel.ManagedSourceMatches(safe.SafePoint, original) || before.ModelInvocations[0].Status != agent.ModelInvocationConsumed {
					t.Fatalf("transition committed without exact preparation proof: %+v", proof)
				}
			}
			if tool {
				ledger, e := host.LoadBudget(ctx, turnID)
				if e != nil || ledger.View("").Usage.ToolCalls != 1 || len(readManagedRecoveryRecords[tools.ExecutionRequest](t, filepath.Join(files, "tool.jsonl"))) != 1 {
					t.Fatalf("original Tool not settled: %v", e)
				}
			}
			beforeCalls := readManagedRecoveryRecords[model.Request](t, filepath.Join(files, "model.jsonl"))
			wantBefore := 0
			if strings.HasPrefix(phase, "completed/") || strings.HasPrefix(phase, "input_applied/") || phase == "unknown" || continued || tool {
				wantBefore = 1
			}
			if nextDispatch {
				wantBefore = 1
				if phase == "resume_receipt/after" {
					wantBefore = 2
				}
			}
			if len(beforeCalls) != wantBefore {
				t.Fatalf("wrong original physical call count: %d", len(beforeCalls))
			}
			session := openManagedFrontierProcess(t, db, files, "resume")
			// The historical expiry is only a conservative deadline for a
			// live probe, not proof that the owner is still live or expired.
			// Do not accidentally elect RESUME in the CANCEL fixture if a
			// slow worker already consumed that window.
			if until := safe.SafePoint.OriginalLease.ExpiresAt.Add(-200 * time.Millisecond); time.Until(until) > time.Second {
				probe, cancel := context.WithDeadline(ctx, until)
				_, probeErr := session.Resume(probe, managedRecoveryTurn)
				cancel()
				if probeErr == nil {
					t.Fatal("live lease enabled recovery")
				}
				if len(readManagedRecoveryRecords[model.Request](t, filepath.Join(files, "model.jsonl"))) != wantBefore {
					t.Fatal("live probe invoked the model")
				}
			}
			_, leaseDuration := conformance.ManagedFixtureExecutionPolicy()
			conformance.AwaitManagedOriginalLeaseExpiry(t, ks, original, safe, 2*leaseDuration+5*time.Second)
			// Before the atomic native receipt CAS, even a response already
			// returned to the dead process has no durable receipt. Never
			// resurrect a separately settled budget receipt or resend it.
			if phase == "unknown" || phase == "dispatch_intent/after" || phase == "resume_unknown/after" || phase == "completed/before" {
				ledger, e := ks.LoadBudget(ctx, turnID)
				inv := before.ModelInvocations[len(before.ModelInvocations)-1]
				res, found := ledger.Reservations["model:"+inv.ID]
				if e != nil || !found || inv.Status != agent.ModelInvocationPending || inv.DispatchAttempt == 0 ||
					res.RunID != root.ExecutionRefID || !res.Dispatched || res.Status != budget.ReservationHeld || len(res.Receipt) != 0 {
					t.Fatalf("unknown receipt did not retain the original dispatched hold: %v", e)
				}
				if _, err = session.Resume(ctx, managedRecoveryTurn); err == nil {
					t.Fatal("unknown dispatch resumed")
				}
				after, e := ks.Load(ctx, root.ExecutionRefID)
				afterLedger, ledgerErr := ks.LoadBudget(ctx, turnID)
				grant, grantErr := ks.LookupChildExecution(ctx, root.ExecutionRefID)
				if e != nil || ledgerErr != nil || grantErr != nil || grant != safe.SafePoint.OriginalGrant ||
					kernel.ManagedDigest(after) != kernel.ManagedDigest(original) || kernel.ManagedDigest(afterLedger) != kernel.ManagedDigest(ledger) ||
					len(readManagedRecoveryRecords[model.Request](t, filepath.Join(files, "model.jsonl"))) != wantBefore {
					t.Fatal("unknown dispatch changed original Run, Grant, hold or physical call count")
				}
				return
			}
			var done harness.Snapshot
			if phase == "cancel/after" {
				done, err = session.Cancel(ctx, managedRecoveryTurn, "stop original task")
			} else {
				done, err = session.Resume(ctx, managedRecoveryTurn)
				// Continue is a legitimate native yield, not an error.
				if continued && err == nil && done.Turn.Status != harness.TurnCompleted {
					done, err = session.Resume(ctx, managedRecoveryTurn)
				}
			}
			if err != nil {
				t.Fatalf("recover: %v", err)
			}
			ledger, err := host.LoadBudget(ctx, turnID)
			if err != nil {
				t.Fatal(err)
			}
			calls := readManagedRecoveryRecords[model.Request](t, filepath.Join(files, "model.jsonl"))
			final, err := ks.Load(ctx, root.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			view, err := agent.ViewState(final)
			if err != nil {
				t.Fatal(err)
			}
			wantCalls := 1
			wantCursor := uint64(0)
			wantStatus := harness.TurnCompleted
			if strings.HasPrefix(phase, "input_applied/") {
				wantCalls, wantCursor = 2, 1
			}
			if continued || tool || nextDispatch {
				wantCalls = 2
			}
			if initial {
				wantCursor = 1
			}
			if phase == "cancel/after" {
				wantCalls, wantStatus = 0, harness.TurnCancelled
			}
			if done.Turn.Status != wantStatus || len(calls) != wantCalls || view.InputCursor != wantCursor ||
				(wantInv != 0 && view.ModelInvocations[0].ID != before.ModelInvocations[0].ID) || ledger.View("").Usage.LLMCalls != wantCalls ||
				!ledger.FinishedRuns[root.ExecutionRefID] || ledger.View("").Reserved != (budget.Usage{}) {
				t.Fatalf("original obligations: status=%s calls=%d cursor=%d ledger=%+v", done.Turn.Status, len(calls), view.InputCursor, ledger.View(""))
			}
			if nextDispatch {
				if len(view.ModelInvocations) != 2 || (wantInv == 2 && view.ModelInvocations[1].ID != before.ModelInvocations[1].ID) ||
					strings.Count(requestMessages(calls[1]), "original continuation") != 1 {
					t.Fatal("new Resume lost or duplicated its original invocation or input")
				}
			}
			if initial && strings.Count(requestMessages(calls[0]), "initial steering") != 1 {
				t.Fatal("initial input lost or duplicated")
			}
			if strings.HasPrefix(phase, "input_applied/") && strings.Count(requestMessages(calls[1]), "frontier steering") != 1 {
				t.Fatal("input duplicated or missing")
			}
			if continued && strings.Count(requestMessages(calls[1]), "original continuation") != 1 {
				t.Fatal("original continuation duplicated or lost")
			}
			if tool && (ledger.View("").Usage.ToolCalls != 1 || len(readManagedRecoveryRecords[tools.ExecutionRequest](t, filepath.Join(files, "tool.jsonl"))) != 1 || strings.Count(requestMessages(calls[1]), `{"original":"evidence"}`) != 1) {
				t.Fatal("original Tool effect or receipt duplicated or lost")
			}
			if _, err = ks.LookupChildExecution(ctx, root.ExecutionRefID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("original grant not retired: %v", err)
			}
		})
	}
}

package continuatione2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	postgresadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"gorm.io/gorm"
)

const (
	autoSummaryContextACK = "context-commit-ack-loss"
	autoSummaryConsumeACK = "kernel-consume-ack-loss"
	autoSummaryUnknown    = "provider-response-without-receipt"
	autoSummaryCrashExit  = 78
)

var errAutoSummaryReturnedACK = errors.New("auto-summary fixture lost committed ACK after Store return")

// This ordinary-mode fixture deliberately keeps ManagedRecovery disabled.
// Each case has a separately logged warmup, one summary Provider call and the
// original three-call invoice oracle. Neither Store nor the fixture ledger
// deduplicates a business effect on behalf of the Agent.
func TestRealPostgresAutoWorkingStateAcrossProcessCrash(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	for _, mode := range []string{autoSummaryContextACK, autoSummaryConsumeACK, autoSummaryUnknown} {
		t.Run(mode, func(t *testing.T) {
			db, isolatedDSN := managedRecoveryDatabase(t, dsn)
			if err := postgresadapter.Migrate(db); err != nil {
				t.Fatal(err)
			}
			if err := harnesspostgres.Migrate(db); err != nil {
				t.Fatal(err)
			}
			files := t.TempDir()
			warmup := newAutoSummarySession(t, db, files, "warmup", mode, false)
			if result, err := warmup.Prompt(t.Context(), "old", "Remember the invoice terms."); err != nil || result.Turn.Status != harness.TurnCompleted {
				t.Fatalf("independent source warmup failed: status=%s error=%v", result.Turn.Status, err)
			}
			runAutoSummaryProcess(t, isolatedDSN, files, "start", mode, autoSummaryCrashExit, true)
			store, err := harnesspostgres.New(db)
			if err != nil {
				t.Fatal(err)
			}
			turnID, root := managedRecoveryRoot(t, store)
			before, err := postgresadapter.NewKernelStore(db).Load(t.Context(), root.ExecutionRefID)
			if err != nil || before.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("original summary Run did not survive process exit: %+v error=%v", before.Run, err)
			}
			original := autoSummaryInvocation(t, before)
			beforeView, err := agent.ViewState(before)
			wantCharged := 0
			if mode == autoSummaryConsumeACK {
				wantCharged = 1
			}
			if err != nil || len(beforeView.ModelInvocations) != 1 || beforeView.Budget.Usage.LLMCalls != wantCharged {
				t.Fatalf("crash did not isolate summary receipt consumption: usage=%+v error=%v", beforeView.Budget.Usage, err)
			}
			turn, err := store.GetTurn(t.Context(), turnID)
			if err != nil {
				t.Fatal(err)
			}
			checkpoint, err := store.GetContextCheckpoint(t.Context(), turn.ContextCheckpointID)
			if err != nil || !runtimecontext.ValidCheckpoint(checkpoint) {
				t.Fatalf("crashed Context is invalid: %v", err)
			}
			if mode == autoSummaryUnknown {
				if original.Status != agent.ModelInvocationPending || original.DispatchAttempt == 0 || checkpoint.Window.WorkingState != nil {
					t.Fatal("receipt-loss crash did not retain the unconsumed physical summary attempt")
				}
			} else {
				wantStatus := agent.ModelInvocationCompleted
				if mode == autoSummaryConsumeACK {
					wantStatus = agent.ModelInvocationConsumed
				}
				if original.Status != wantStatus {
					t.Fatalf("crash missed the intended summary boundary: status=%s want=%s", original.Status, wantStatus)
				}
				assertAutoSummaryCheckpoint(t, checkpoint, original, root.ExecutionRefID)
			}
			assertAutoSummaryCallCounts(t, files, 1, 0, 0)
			summaryRequest := autoSummaryRecords[model.Request](t, filepath.Join(files, "summary.jsonl"))[0]
			if summaryRequest.InvocationID != original.ID || summaryRequest.RunID != root.ExecutionRefID ||
				!strings.Contains(requestMessages(summaryRequest), "shipping_cents=700") {
				t.Fatal("summary did not use the original frozen invocation and complete removed evidence")
			}
			if mode == autoSummaryContextACK {
				runAutoSummaryProcess(t, isolatedDSN, files, "reject-mode", mode, 0, false)
			}
			runAutoSummaryProcess(t, isolatedDSN, files, "resume", mode, 0, true)
			after, err := postgresadapter.NewKernelStore(db).Load(t.Context(), root.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == autoSummaryUnknown {
				retained := autoSummaryInvocation(t, after)
				if after.Run.Revision != before.Run.Revision || !reflect.DeepEqual(retained, original) || !reflect.DeepEqual(after.State, before.State) {
					t.Fatal("unknown summary Resume changed the frozen original attempt")
				}
				assertAutoSummaryCallCounts(t, files, 1, 0, 0)
				t.Log("summary_physical_calls=1 summary_completed_receipts=0 business_calls=0 effects=0 warmup_calls=1; unreceipted usage remains unknown")
				return
			}
			assertAutoSummaryInvoice(t, store, root, &original, after, files, true)
		})
	}
}

// Unlike the hard exits above, these faults let the complete original
// Agent/Harness call return before a separate process resumes. This proves
// ordinary returned-callback recovery, not takeover of an unreturned callback.
func TestRealPostgresAutoWorkingStateAfterReturnedACKLoss(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	for _, mode := range []string{autoSummaryContextACK, autoSummaryConsumeACK} {
		t.Run(mode, func(t *testing.T) {
			db, isolatedDSN := managedRecoveryDatabase(t, dsn)
			if err := postgresadapter.Migrate(db); err != nil {
				t.Fatal(err)
			}
			if err := harnesspostgres.Migrate(db); err != nil {
				t.Fatal(err)
			}
			files := t.TempDir()
			warmup := newAutoSummarySession(t, db, files, "warmup", mode, false)
			if result, err := warmup.Prompt(t.Context(), "old", "Remember the invoice terms."); err != nil || result.Turn.Status != harness.TurnCompleted {
				t.Fatalf("independent source warmup failed: status=%s error=%v", result.Turn.Status, err)
			}
			runAutoSummaryProcess(t, isolatedDSN, files, "start-returned", mode, autoSummaryCrashExit, false)
			returned := autoSummaryRecords[string](t, filepath.Join(files, "returned.jsonl"))
			if len(returned) != 1 || !strings.Contains(returned[0], errAutoSummaryReturnedACK.Error()) {
				t.Fatal("fixture exited without an actual original Prompt return")
			}
			store, err := harnesspostgres.New(db)
			if err != nil {
				t.Fatal(err)
			}
			_, root := managedRecoveryRoot(t, store)
			before, err := postgresadapter.NewKernelStore(db).Load(t.Context(), root.ExecutionRefID)
			if err != nil || before.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("returned ACK fault lost original Running Run: %+v error=%v", before.Run, err)
			}
			original := autoSummaryInvocation(t, before)
			want := agent.ModelInvocationCompleted
			if mode == autoSummaryConsumeACK {
				want = agent.ModelInvocationConsumed
			}
			if original.Status != want {
				t.Fatalf("returned ACK fault missed original summary phase: status=%s want=%s", original.Status, want)
			}
			assertAutoSummaryCallCounts(t, files, 1, 0, 0)
			runAutoSummaryProcess(t, isolatedDSN, files, "resume", mode, 0, false)
			completed, err := postgresadapter.NewKernelStore(db).Load(t.Context(), root.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			assertAutoSummaryInvoice(t, store, root, &original, completed, files, true)
		})
	}
}

func TestRealPostgresAutoWorkingStateCannotAddRecoveryProofAfterHardCrash(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, isolatedDSN := managedRecoveryDatabase(t, dsn)
	if err := postgresadapter.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	files := t.TempDir()
	warmup := newAutoSummarySession(t, db, files, "warmup", autoSummaryContextACK, false)
	if _, err := warmup.Prompt(t.Context(), "old", "Remember the invoice terms."); err != nil {
		t.Fatal(err)
	}
	// The original ordinary Run never opted into native summary recovery.
	// A stronger replacement handle cannot fabricate its missing safe point.
	runAutoSummaryProcess(t, isolatedDSN, files, "start", autoSummaryContextACK, autoSummaryCrashExit, false)
	runAutoSummaryProcess(t, isolatedDSN, files, "reject-mode", autoSummaryContextACK, 0, true)
	store, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	_, root := managedRecoveryRoot(t, store)
	if _, err := postgresadapter.NewKernelStore(db).LoadManagedRecovery(t.Context(), root.ExecutionRefID); !kernel.ManagedRecoveryAbsent(err) {
		t.Fatalf("replacement handle created recovery proof for the ordinary original Run: %v", err)
	}
	assertAutoSummaryCallCounts(t, files, 1, 0, 0)
}

func TestRealPostgresAutoWorkingStatePairedProfiles(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	var sourceHash string
	for _, phase := range []string{"paired-disabled", "paired-success", "paired-invalid"} {
		t.Run(phase, func(t *testing.T) {
			db, _ := managedRecoveryDatabase(t, dsn)
			if err := postgresadapter.Migrate(db); err != nil {
				t.Fatal(err)
			}
			if err := harnesspostgres.Migrate(db); err != nil {
				t.Fatal(err)
			}
			files := t.TempDir()
			warmup := newAutoSummarySession(t, db, files, "warmup", "", false)
			if _, err := warmup.Prompt(t.Context(), "old", "Remember the invoice terms."); err != nil {
				t.Fatal(err)
			}
			session := newAutoSummarySession(t, db, files, phase, "", false)
			result, err := session.Prompt(t.Context(), managedRecoveryTurn, managedRecoveryPrompt)
			if err != nil || result.Turn.Status != harness.TurnCompleted {
				t.Fatalf("paired profile did not complete: status=%s error=%v", result.Turn.Status, err)
			}
			store, err := harnesspostgres.New(db)
			if err != nil {
				t.Fatal(err)
			}
			_, root := managedRecoveryRoot(t, store)
			completed, err := postgresadapter.NewKernelStore(db).Load(t.Context(), root.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			var original *agent.ModelInvocation
			if phase != "paired-disabled" {
				invocation := autoSummaryInvocation(t, completed)
				original = &invocation
			}
			currentSourceHash := assertAutoSummaryInvoice(t, store, root, original, completed, files, phase == "paired-success")
			if sourceHash == "" {
				sourceHash = currentSourceHash
			} else if sourceHash != currentSourceHash {
				t.Fatal("paired profiles used different original source windows")
			}
			t.Log("cost profile uses synthetic Provider-reported usage; request bytes are serialized JSON, not measured tokenizer tokens or model-quality evidence")
		})
	}
}

// Only the parent test selects this helper. Exit occurs at a Store boundary,
// without returning an error or running defers in the crashed process.
func TestAutoWorkingStateRecoveryProcess(t *testing.T) {
	phase := os.Getenv("GAOGE_TEST_AUTO_SUMMARY_PHASE")
	if phase == "" {
		t.Skip("subprocess helper")
	}
	if phase != "start" && phase != "start-returned" && phase != "resume" && phase != "reject-mode" {
		t.Fatalf("invalid phase %q", phase)
	}
	mode := os.Getenv("GAOGE_TEST_AUTO_SUMMARY_FAULT")
	if mode != autoSummaryContextACK && mode != autoSummaryConsumeACK && mode != autoSummaryUnknown {
		t.Fatalf("invalid fault %q", mode)
	}
	db := openManagedRecoveryDB(t, os.Getenv("TEST_POSTGRES_DSN"))
	files := os.Getenv("GAOGE_TEST_AUTO_SUMMARY_FILES")
	nativeRecovery, err := strconv.ParseBool(os.Getenv("GAOGE_TEST_AUTO_SUMMARY_NATIVE_RECOVERY"))
	if err != nil {
		t.Fatal(err)
	}
	session := newAutoSummarySession(t, db, files, phase, mode, nativeRecovery)
	if phase == "start" || phase == "start-returned" {
		_, err := session.Prompt(t.Context(), managedRecoveryTurn, managedRecoveryPrompt)
		if phase == "start-returned" && errors.Is(err, errAutoSummaryReturnedACK) {
			if writeErr := appendManagedRecoveryRecord(filepath.Join(files, "returned.jsonl"), err.Error()); writeErr != nil {
				t.Fatal(writeErr)
			}
			os.Exit(autoSummaryCrashExit)
		}
		t.Fatalf("expected exit at %s, got %v", mode, err)
	}
	store, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	_, root := managedRecoveryRoot(t, store)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if phase == "reject-mode" {
		runs := postgresadapter.NewKernelStore(db)
		before, err := runs.Load(ctx, root.ExecutionRefID)
		if err != nil {
			t.Fatal(err)
		}
		beforeTransfer, beforeTransferErr := runs.LoadManagedRecovery(ctx, root.ExecutionRefID)
		if beforeTransferErr != nil && !kernel.ManagedRecoveryAbsent(beforeTransferErr) {
			t.Fatal(beforeTransferErr)
		}
		if _, err := session.Resume(ctx, managedRecoveryTurn); !errors.Is(err, harness.ErrInvalidRequest) {
			t.Fatalf("replacement mode changed the original frozen recovery selection: %v", err)
		}
		after, err := runs.Load(ctx, root.ExecutionRefID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("rejected replacement mode changed the full original Kernel proof/receipt/usage: %v", err)
		}
		afterTransfer, afterTransferErr := runs.LoadManagedRecovery(ctx, root.ExecutionRefID)
		if afterTransferErr != nil && !kernel.ManagedRecoveryAbsent(afterTransferErr) {
			t.Fatal(afterTransferErr)
		}
		if !reflect.DeepEqual(beforeTransfer, afterTransfer) || kernel.ManagedRecoveryAbsent(beforeTransferErr) != kernel.ManagedRecoveryAbsent(afterTransferErr) {
			t.Fatal("rejected mode changed or fabricated the original recovery transfer")
		}
		assertAutoSummaryCallCounts(t, files, 1, 0, 0)
		return
	}
	// Wait on PostgreSQL's authoritative clock, without repeated Resume calls
	// adding unrelated diagnostic or projection transitions to the fault test.
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		lease := readManagedRecoveryLease(t, ctx, db, root.ExecutionRefID)
		if lease.ExecutionExpiresAt == nil || !lease.StoreNow.Before(*lease.ExecutionExpiresAt) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	result, err := session.Resume(ctx, managedRecoveryTurn)
	if mode == autoSummaryUnknown {
		if !errors.Is(err, agent.ErrModelOutcomeUnknown) {
			logAutoSummaryResumeFacts(t, ctx, db, store, root, session)
			t.Fatalf("unreceipted summary did not retain its native unknown outcome: %v", err)
		}
		return
	}
	if err != nil || result.Turn.Status != harness.TurnCompleted {
		logAutoSummaryResumeFacts(t, ctx, db, store, root, session)
		t.Fatalf("ordinary nil-Transcript recovery failed: status=%s error=%v", result.Turn.Status, err)
	}
	if _, err := session.Resume(ctx, managedRecoveryTurn); err != nil {
		t.Fatalf("completed recovery replay failed: %v", err)
	}
}

func logAutoSummaryResumeFacts(t *testing.T, ctx context.Context, db *gorm.DB, store harness.Store, root harness.Invocation, session *harness.SessionClient) {
	t.Helper()
	turn, turnErr := store.GetTurn(ctx, root.TurnID)
	config, configErr := store.GetConfigSnapshot(ctx, turn.ConfigSnapshotID)
	snapshot, runErr := postgresadapter.NewKernelStore(db).Load(ctx, root.ExecutionRefID)
	view, viewErr := agent.ViewState(snapshot)
	_, loadErr := session.Load(ctx, managedRecoveryTurn)
	t.Logf("recovery diagnostics turn=%s/rev%d turnError=%v configError=%v runError=%v viewError=%v sessionLoadError=%v configSummary=%+v nativeSummary=%+v configCap=%d nativeCap=%d", turn.Status, turn.Revision, turnErr, configErr, runErr, viewErr, loadErr, config.WorkingStateSummary, view.WorkingStateSummary, config.MaxModelOutputTokens, view.MaxModelOutputTokens)
	invocation, found := findAutoSummaryInvocation(snapshot)
	if found && invocation.ContextSummary != nil {
		binding := invocation.ContextSummary
		t.Logf("summary cursor sourceTurn=%s/rev%d expectedTargetRevision=%d frozenUpdate=%s actualUpdate=%s", binding.Cursor.Turn.Status, binding.Cursor.Turn.Revision, binding.Cursor.Turn.Revision+1, binding.Cursor.UpdatedAt.Format(time.RFC3339Nano), turn.UpdatedAt.Format(time.RFC3339Nano))
		source, sourceErr := store.GetContextCheckpoint(ctx, binding.Source.CheckpointID)
		working, workingErr := agent.WorkingStateFromSummaryInvocation(invocation)
		manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
		candidate, compactErr := manager.CompactPortable(runtimecontext.PortableCompactionRequest{Previous: source, RunID: snapshot.Run.ID, Messages: runtimecontext.Materialize(source.Window), Policy: binding.ContextPolicy, OmitExtract: true})
		candidate.Window.WorkingState = working
		target, targetErr := manager.Rollover(ctx, runtimecontext.RolloverRequest{Previous: source, Window: candidate.Window, Artifacts: candidate.Artifacts, Reason: "working_state_summary", ModelWindowFingerprint: binding.ModelWindowFingerprint})
		actual, actualErr := store.GetContextCheckpoint(ctx, turn.ContextCheckpointID)
		t.Logf("summary reconstruction exactTarget=%t sourceError=%v workingError=%v compactError=%v targetError=%v actualError=%v", reflect.DeepEqual(target, actual), sourceErr, workingErr, compactErr, targetErr, actualErr)
	}
}

type autoSummaryCrashKernelStore struct {
	*postgresadapter.KernelStore
	mode        string
	returnError bool
}

func (store *autoSummaryCrashKernelStore) Commit(commitCtx context.Context, commitID string, commitRev uint64, commitMutation kernel.StoreMutation, commitAuth kernel.CommitAuthority) (kernel.Snapshot, bool, error) {
	if commitAuth.Lease != nil {
		proposed := kernel.Snapshot{Run: commitMutation.Record.Run, State: commitMutation.Record.State, Checkpoint: commitMutation.Record.Checkpoint, Result: commitMutation.Record.Result}
		next, found := findAutoSummaryInvocation(proposed)
		if found && store.mode == autoSummaryUnknown && next.Status == agent.ModelInvocationCompleted {
			os.Exit(autoSummaryCrashExit)
		}
		consume := false
		if store.mode == autoSummaryConsumeACK {
			for _, event := range commitMutation.Events {
				consume = consume || event.Type == agent.EventContextSummaryConsumed
			}
		}
		snapshot, _, err := store.KernelStore.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
		if err == nil && consume {
			if store.returnError {
				return kernel.Snapshot{}, false, errAutoSummaryReturnedACK
			}
			os.Exit(autoSummaryCrashExit)
		}
		return snapshot, false, err
	}
	return store.KernelStore.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
}

type autoSummaryCrashHostStore struct {
	*harnesspostgres.Store
	mode        string
	returnError bool
}

func (store *autoSummaryCrashHostStore) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	value, fresh, err := store.Store.AppendItem(ctx, item)
	if err != nil {
		err = fmt.Errorf("auto-summary fixture AppendItem kind=%s id=%s run=%s: %w", item.Kind, item.ID, item.RunID, err)
	}
	return value, fresh, err
}

func (store *autoSummaryCrashHostStore) CommitContextCheckpoint(ctx context.Context, request harness.ContextCheckpointCommit) (harness.Turn, error) {
	turn, err := store.Store.CommitContextCheckpoint(ctx, request)
	state := request.Checkpoint.Window.WorkingState
	if err == nil && store.mode == autoSummaryContextACK && state != nil && state.Derivation != nil {
		if store.returnError {
			return harness.Turn{}, errAutoSummaryReturnedACK
		}
		os.Exit(autoSummaryCrashExit)
	}
	return turn, err
}

func newAutoSummarySession(t *testing.T, db *gorm.DB, files, phase, mode string, nativeRecovery bool) *harness.SessionClient {
	t.Helper()
	backing, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	var store harness.Store = backing
	var runs kernel.Store = postgresadapter.NewKernelStore(db)
	starting := phase == "start" || phase == "start-returned"
	if starting {
		store = &autoSummaryCrashHostStore{Store: backing, mode: mode, returnError: phase == "start-returned"}
		runs = &autoSummaryCrashKernelStore{KernelStore: postgresadapter.NewKernelStore(db), mode: mode, returnError: phase == "start-returned"}
	} else if phase == "resume" || phase == "reject-mode" {
		store = &autoSummaryCrashHostStore{Store: backing}
	}
	runtimeDependencies := kernel.Dependencies{Store: runs}
	if nativeRecovery {
		runtimeDependencies.ManagedRecoveryHost = store.(kernel.ManagedRecoveryHostStore)
	}
	runtime, err := kernel.New(runtimeDependencies)
	if err != nil {
		t.Fatal(err)
	}
	paired := strings.HasPrefix(phase, "paired-")
	options := managedRecoveryConfiguredSessionOptions(starting || paired)
	// Keep the original evidence unchanged. This profile has a window large
	// enough for the separate summary input, and a low soft threshold that
	// requests summarization before the hard limit is reached.
	options.Config.ContextPolicy = runtimecontext.Policy{
		MaxInputTokens: 50000, SoftLimitPercent: 10, MaxSerializedBytes: 65536,
		PreserveRecentTurns: 1, MinimumRecentTurns: 1, MaxCompactionTokens: 128,
	}
	options.Config.Limits = agent.Limits{MaxLLMCalls: 5, MaxToolCalls: 2, MaxTotalTokens: 100}
	if starting || paired && phase != "paired-disabled" {
		options.Config.WorkingStateSummary = &agent.WorkingStateSummaryPolicy{MaxInputTokens: 50000, MaxSerializedBytes: 65536}
	}
	if phase == "warmup" {
		options.Transcript = harness.TranscriptSourceFunc(func(_ context.Context, _, _ harness.HostRef, prompt string) (*harness.ContextSeed, error) {
			return &harness.ContextSeed{SourcePath: []string{"old-user"}, Entries: []runtimecontext.Entry{{
				ID: "old-user", SourceID: "old-user", TurnID: "old", Required: true,
				Message: model.Message{Role: model.RoleUser, Content: model.TextParts(prompt)},
			}}}, nil
		})
	}
	// A recovery handle deliberately supplies no replacement Transcript or
	// summary policy. The original persisted Config remains authoritative.
	if phase == "resume" {
		options.Transcript = nil
	}
	options.Config.ToolKeys = nil
	options.Config.ToolPolicies = nil
	session, err := harness.OpenAgentSession(harness.AgentSessionOptions{
		Runtime: runtime, Store: store, Model: autoSummaryInvoiceModel{files: files, warmup: phase == "warmup", invalid: phase == "paired-invalid"},
		LocalTools: managedRecoverySessionTools(files), Session: options,
		Context: harness.AgentSessionContextOptions{
			ReadArtifacts: true,
			ResolveWindow: func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
				return runtimecontext.ModelWindow{ContextTokens: 65536, ReservedOutputTokens: 512}, nil
			},
		},
		Execution: agent.ExecutionPolicy{
			Model: agent.CallPolicy{Timeout: 3 * time.Second, MaxAttempts: 1, MaxConcurrency: 1},
			Tool:  agent.CallPolicy{Timeout: time.Second, MaxAttempts: 1, MaxConcurrency: 1},
		},
		RunExecution: &agent.RunExecution{OwnerID: "auto-summary-" + phase, LeaseDuration: managedRecoveryLeaseDuration},
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

type autoSummaryInvoiceModel struct {
	files   string
	warmup  bool
	invalid bool
}

func (autoSummaryInvoiceModel) Capabilities(_ context.Context, name string) (model.Capabilities, error) {
	return model.Capabilities{StructuredJSONSchema: name == "fixture-invoice"}, nil
}

func (client autoSummaryInvoiceModel) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	if client.warmup {
		if err := appendManagedRecoveryRecord(filepath.Join(client.files, "warmup.jsonl"), request); err != nil {
			return model.Response{}, err
		}
		return model.Response{Content: "invoice task ready", Usage: &model.Usage{InputTokens: 3, OutputTokens: 2}}, nil
	}
	if request.OutputContract != nil && request.OutputContract.Name == "working_state_summary" {
		if len(request.Tools) != 0 || len(request.HostedTools) != 0 || request.RequireToolCall || request.MaxOutputTokens != 512 {
			return model.Response{}, errors.New("summary request enlarged capabilities or changed the frozen output cap")
		}
		if err := appendManagedRecoveryRecord(filepath.Join(client.files, "summary.jsonl"), request); err != nil {
			return model.Response{}, err
		}
		if client.invalid {
			return model.Response{Content: `{"goal":`, Usage: &model.Usage{InputTokens: 20, OutputTokens: 10, CacheReadTokens: 2, ReasoningTokens: 1}}, nil
		}
		return model.Response{
			Content: `{"goal":"Complete the invoice using verified original evidence","constraints":["Keep all amounts in exact cents"],"completed":["The Host selected the invoice task"],"pending":["Record the subtotal once","Retrieve the original archived shipping evidence"],"decisions":["Verify shipping through the authorized archive reader"]}`,
			Usage:   &model.Usage{InputTokens: 20, OutputTokens: 10, CacheReadTokens: 2, ReasoningTokens: 1},
		}, nil
	}
	response, err := (managedRecoveryModel{logPath: filepath.Join(client.files, "business.jsonl")}).Generate(ctx, request)
	response.Usage = &model.Usage{InputTokens: 7, OutputTokens: 3}
	return response, err
}

func findAutoSummaryInvocation(snapshot kernel.Snapshot) (agent.ModelInvocation, bool) {
	view, err := agent.ViewState(snapshot)
	if err != nil {
		return agent.ModelInvocation{}, false
	}
	for _, invocation := range view.ModelInvocations {
		if invocation.Purpose == agent.ModelInvocationContextSummary {
			return invocation, true
		}
	}
	return agent.ModelInvocation{}, false
}

func autoSummaryInvocation(t *testing.T, snapshot kernel.Snapshot) agent.ModelInvocation {
	t.Helper()
	invocation, found := findAutoSummaryInvocation(snapshot)
	if !found {
		t.Fatal("missing original native summary invocation")
	}
	return invocation
}

func assertAutoSummaryCheckpoint(t *testing.T, checkpoint runtimecontext.Checkpoint, invocation agent.ModelInvocation, runID string) {
	t.Helper()
	state := checkpoint.Window.WorkingState
	if !runtimecontext.ValidCheckpoint(checkpoint) || state == nil || state.Version != 1 || state.Derivation == nil ||
		invocation.ContextSummary == nil || state.Source != invocation.ContextSummary.Source || state.Version != invocation.ContextSummary.NextVersion ||
		checkpoint.ParentCheckpointID != invocation.ContextSummary.Source.CheckpointID ||
		state.Source.CoveredThroughSourceID != "invoice-user" || state.Derivation.Kind != runtimecontext.WorkingStateDerivationSummary ||
		state.Derivation.RunID != runID || state.Derivation.InvocationID != invocation.ID || state.Derivation.RequestHash != invocation.RequestHash ||
		state.Derivation.PolicyVersion != agent.WorkingStateSummaryVersion || len(checkpoint.ArtifactIDs) != 1 {
		t.Fatalf("summary checkpoint lost original source/receipt or exact evidence: %+v", checkpoint)
	}
}

func assertAutoSummaryInvoice(t *testing.T, store *harnesspostgres.Store, root harness.Invocation, original *agent.ModelInvocation, completed kernel.Snapshot, files string, expectState bool) string {
	t.Helper()
	if completed.Run.Status != kernel.RunStatusCompleted || completed.Result == nil {
		t.Fatalf("invoice did not finish: %+v", completed.Run)
	}
	var answer string
	if err := json.Unmarshal(completed.Result.Content, &answer); err != nil || answer != managedRecoveryAnswer {
		t.Fatalf("exact-cent invoice oracle changed: answer=%q error=%v", answer, err)
	}
	summaryCalls := 0
	if original != nil {
		summaryCalls = 1
	}
	view, err := agent.ViewState(completed)
	if err != nil || len(view.ModelInvocations) != 3+summaryCalls || view.Budget.Usage.LLMCalls != 3+summaryCalls || view.Budget.Usage.ToolCalls != 2 ||
		view.Budget.Usage.InputTokens != int64(21+20*summaryCalls) || view.Budget.Usage.OutputTokens != int64(9+10*summaryCalls) || view.Budget.Usage.TotalTokens != int64(30+30*summaryCalls) ||
		view.Budget.Usage.CacheReadTokens != int64(2*summaryCalls) || view.Budget.Usage.ReasoningTokens != int64(summaryCalls) {
		t.Fatalf("summary receipt was not charged exactly once with business usage: %+v error=%v", view.Budget.Usage, err)
	}
	for _, invocation := range view.ModelInvocations {
		if invocation.Status != agent.ModelInvocationConsumed {
			t.Fatalf("completed invoice retained unconsumed receipt: %+v", invocation)
		}
	}
	var retained agent.ModelInvocation
	if original != nil {
		retained = autoSummaryInvocation(t, completed)
		if retained.ID != original.ID || retained.RequestHash != original.RequestHash {
			t.Fatal("recovery replaced the logical summary intent")
		}
	}
	turn, err := store.GetTurn(t.Context(), root.TurnID)
	if err != nil || turn.Status != harness.TurnCompleted {
		t.Fatalf("invoice Turn projection lost: %+v error=%v", turn, err)
	}
	checkpoint, err := store.GetContextCheckpoint(t.Context(), turn.ContextCheckpointID)
	if err != nil {
		t.Fatal(err)
	}
	if expectState {
		assertAutoSummaryCheckpoint(t, checkpoint, retained, root.ExecutionRefID)
	} else if checkpoint.Window.WorkingState != nil {
		t.Fatal("disabled or known-invalid summary installed automatic state")
	}
	source, err := store.GetContextCheckpoint(t.Context(), checkpoint.ParentCheckpointID)
	if err != nil || !runtimecontext.ValidCheckpoint(source) || source.ScopeID != turn.SessionID {
		t.Fatalf("summary receipt no longer identifies its exact durable source: %v", err)
	}
	if original != nil && (retained.ContextSummary == nil || source.ID != retained.ContextSummary.Source.CheckpointID ||
		source.ContentHash != retained.ContextSummary.Source.ContentHash || source.CoveredPathHash != retained.ContextSummary.Source.CoveredPathHash ||
		source.CoveredThroughSourceID != retained.ContextSummary.Source.CoveredThroughSourceID) {
		t.Fatal("summary source changed between planning and business execution")
	}
	archive, err := store.GetContextArtifact(t.Context(), checkpoint.ArtifactIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	var removed []model.Message
	if err := json.Unmarshal(archive.ContentJSON, &removed); err != nil || len(removed) != 2 ||
		model.TextValue(removed[0].Content) != "Remember the invoice terms." ||
		model.TextValue(removed[1].Content) != strings.Repeat("archived invoice discussion; ", 1000)+"shipping_cents=700" {
		t.Fatalf("summary discarded or altered exact original shipping evidence: %v", err)
	}
	assertAutoSummaryCallCounts(t, files, summaryCalls, 3, 1)
	business := autoSummaryRecords[model.Request](t, filepath.Join(files, "business.jsonl"))
	for index, request := range business {
		projections := 0
		for _, message := range request.Messages {
			if strings.Contains(model.TextValue(message.Content), "<host_working_state>") {
				projections++
				if message.Role != model.RoleUser {
					t.Fatal("automatic state became trusted instructions")
				}
			}
		}
		wantProjections := 0
		if expectState {
			wantProjections = 1
		}
		if projections != wantProjections || request.RunID != root.ExecutionRefID || request.MaxOutputTokens != 512 ||
			strings.Contains(requestMessages(request), strings.Repeat("archived invoice discussion; ", 20)) ||
			index < 2 && strings.Contains(requestMessages(request), "shipping_cents=700") {
			t.Fatalf("business request %d lost bounded state/evidence/cap semantics", index)
		}
	}
	if !managedRecoveryHasToolResult(business[2].Messages, managedRecoveryArchiveCall) || !strings.Contains(requestMessages(business[2]), "shipping_cents=700") {
		t.Fatal("invoice did not use exact shipping from the authorized archive reader")
	}
	effects := autoSummaryRecords[managedRecoveryEffect](t, filepath.Join(files, "ledger.jsonl"))
	if effects[0].RunID != root.ExecutionRefID || effects[0].CallID != managedRecoverySubtotalCall || effects[0].SubtotalCents != 5100 {
		t.Fatalf("invoice side effect changed or rebound: %+v", effects)
	}
	for _, item := range []struct{ name, file string }{{"summary", "summary.jsonl"}, {"business", "business.jsonl"}, {"warmup", "warmup.jsonl"}} {
		requests := autoSummaryRecords[model.Request](t, filepath.Join(files, item.file))
		var sizes []int
		total := 0
		for _, request := range requests {
			raw, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			sizes = append(sizes, len(raw))
			total += len(raw)
		}
		t.Logf("%s_physical_calls=%d request_bytes=%v total_request_bytes=%d", item.name, len(requests), sizes, total)
	}
	t.Logf("invoice_logical_calls=%d effects=1 invoice_synthetic_reported_usage=%+v; separate_warmup_logical_calls=1 reported_input=3 reported_output=2; usage_coverage=%d/%d", view.Budget.Usage.LLMCalls, view.Budget.Usage, view.Budget.Usage.LLMCalls, view.Budget.Usage.LLMCalls)
	t.Logf("complete_fixture_logical_calls=%d synthetic_input_tokens=%d synthetic_output_tokens=%d synthetic_total_tokens=%d; counts include summary and warmup", view.Budget.Usage.LLMCalls+1, view.Budget.Usage.InputTokens+3, view.Budget.Usage.OutputTokens+2, view.Budget.Usage.TotalTokens+5)
	return source.ContentHash
}

func assertAutoSummaryCallCounts(t *testing.T, files string, summary, business, effects int) {
	t.Helper()
	if got := len(autoSummaryRecords[model.Request](t, filepath.Join(files, "summary.jsonl"))); got != summary {
		t.Fatalf("summary Provider delivery count=%d want=%d", got, summary)
	}
	if got := len(autoSummaryRecords[model.Request](t, filepath.Join(files, "business.jsonl"))); got != business {
		t.Fatalf("business Provider delivery count=%d want=%d", got, business)
	}
	if got := len(autoSummaryRecords[managedRecoveryEffect](t, filepath.Join(files, "ledger.jsonl"))); got != effects {
		t.Fatalf("business ledger effect count=%d want=%d", got, effects)
	}
	if got := len(autoSummaryRecords[model.Request](t, filepath.Join(files, "warmup.jsonl"))); got != 1 {
		t.Fatalf("independent warmup Provider delivery count=%d want=1", got)
	}
}

func autoSummaryRecords[T any](t *testing.T, path string) []T {
	t.Helper()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return readManagedRecoveryRecords[T](t, path)
}

func runAutoSummaryProcess(t *testing.T, dsn, files, phase, mode string, wantExit int, nativeRecovery bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAutoWorkingStateRecoveryProcess$", "-test.count=1")
	command.Env = append(os.Environ(), "TEST_POSTGRES_DSN="+dsn,
		"GAOGE_TEST_AUTO_SUMMARY_PHASE="+phase, "GAOGE_TEST_AUTO_SUMMARY_FILES="+files, "GAOGE_TEST_AUTO_SUMMARY_FAULT="+mode,
		"GAOGE_TEST_AUTO_SUMMARY_NATIVE_RECOVERY="+strconv.FormatBool(nativeRecovery))
	output, err := command.CombinedOutput()
	if wantExit == 0 && err == nil {
		return
	}
	var exitErr *exec.ExitError
	if wantExit != 0 && errors.As(err, &exitErr) && exitErr.ExitCode() == wantExit {
		return
	}
	t.Fatalf("%s/%s child exit: want=%d error=%v\n%s", mode, phase, wantExit, err, output)
}

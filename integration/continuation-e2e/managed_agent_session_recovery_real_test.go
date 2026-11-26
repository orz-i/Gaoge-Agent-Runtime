package continuatione2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	postgresadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres"
	postgresmodels "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	managedRecoveryCrashExit     = 77
	managedRecoveryLeaseDuration = 5 * time.Second
	managedRecoveryTurn          = "invoice"
	managedRecoveryTool          = "invoice.record_subtotal"
	managedRecoverySubtotalCall  = "record-invoice-subtotal"
	managedRecoveryArchiveCall   = "read-archived-shipping"
	managedRecoveryPrompt        = "Record the subtotal for 3 units at 1700 cents, recover archived shipping_cents, and report the invoice total in cents."
	managedRecoveryAnswer        = "invoice_total_cents=5800"
)

// Both Kernel and Harness/Context live in PostgreSQL. The first child exits
// without defers after the second model intent commits, with the preceding
// Tool receipt already durable. A fresh child resumes with no TranscriptSource,
// first proving that the dead owner's live lease blocks execution, then waiting
// for PostgreSQL's authoritative clock to permit a native fenced takeover.
// The append-only fixture ledger deliberately has NO deduplication, so it
// detects replay of that committed Tool. This is not a claim about effects
// whose receipts were lost, provider quality, or concurrent executor fencing.
func TestRealPostgresManagedAgentSessionRecoveryAcrossProcessCrash(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	for _, facade := range []bool{false, true} {
		name := "explicit"
		if facade {
			name = "agent-session"
		}
		t.Run(name, func(t *testing.T) {
			testManagedAgentSessionRecoveryAcrossProcessCrash(t, dsn, facade, false)
		})
	}
}

// A successor retirement transaction is cut immediately before or after
// commit. Closure, receipt, grant deletion and lease release are one native
// fact. A third process may repair that fact or its Host projection, never
// execute another Model, Tool or managed owner.
func TestRealPostgresManagedClosureRetirementAcrossProcessCrash(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	for _, cut := range []string{"finish-before", "finish-after"} {
		t.Run(cut, func(t *testing.T) {
			db, isolated := managedRecoveryDatabase(t, dsn)
			if err := postgresadapter.Migrate(db); err != nil {
				t.Fatal(err)
			}
			if err := harnesspostgres.Migrate(db); err != nil {
				t.Fatal(err)
			}
			files := t.TempDir()
			runManagedRecoveryProcess(t, isolated, files, "crash", managedRecoveryCrashExit, true)
			host, err := harnesspostgres.New(db)
			if err != nil {
				t.Fatal(err)
			}
			turnID, root := managedRecoveryRoot(t, host)
			ks := postgresadapter.NewKernelStore(db)
			original, err := ks.LoadManagedRecovery(t.Context(), root.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			runManagedRecoveryProcess(t, isolated, files, "resume-"+cut, managedRecoveryCrashExit, true)
			closed, err := ks.LoadManagedRecoveryByID(t.Context(), original.SafePoint.ID)
			if err != nil || closed.SuccessorGrant.ID == "" {
				t.Fatalf("fault missed original successor: state=%s err=%v", closed.Selection.Mode, err)
			}
			before, err := ks.Load(t.Context(), root.ExecutionRefID)
			if err != nil || before.Run.Status != kernel.RunStatusCompleted {
				t.Fatalf("fault missed terminal callback result: %v", err)
			}
			grant, grantErr := ks.LookupChildExecution(t.Context(), root.ExecutionRefID)
			receipt, receiptErr := ks.LoadOwnedRetirement(t.Context(), closed.SuccessorGrant.ID)
			if cut == "finish-before" {
				if closed.Resolved() || closed.Selection.Mode != kernel.ManagedResume || closed.Terminal != nil || grantErr != nil || grant != closed.SuccessorGrant || !errors.Is(receiptErr, kernel.ErrNotFound) {
					t.Fatalf("retirement partially committed: state=%s grant=%+v err=%v retirement=%v", closed.Selection.Mode, grant, grantErr, receiptErr)
				}
			} else if !closed.Resolved() || closed.Selection.Mode != kernel.ManagedResume || closed.Terminal == nil || !errors.Is(grantErr, kernel.ErrNotFound) || receiptErr != nil || !receipt.MatchesManagedClosure(closed, before) {
				t.Fatalf("atomic retirement missing: state=%s grant=%v receipt=%s err=%v", closed.Selection.Mode, grantErr, receipt.Phase, receiptErr)
			}
			if err := host.VerifyManagedOriginalDispatch(t.Context(), original.SafePoint.Host, false); err != nil {
				t.Fatalf("Host dispatch was closed before native retirement: %v", err)
			}
			calls := readManagedRecoveryRecords[model.Request](t, filepath.Join(files, "model.jsonl"))
			effects := readManagedRecoveryRecords[managedRecoveryEffect](t, filepath.Join(files, "ledger.jsonl"))
			if len(calls) != 3 || len(effects) != 1 {
				t.Fatalf("unexpected pre-repair effects: %d/%d", len(calls), len(effects))
			}
			phase := "resume-closed"
			if cut == "finish-after" {
				phase = "refresh-closed"
			}
			runManagedRecoveryProcess(t, isolated, files, phase, 0, true)
			settled, err := ks.LoadManagedRecoveryByID(t.Context(), original.SafePoint.ID)
			if err != nil || !settled.Resolved() || settled.Selection.Mode != kernel.ManagedResume || settled.SuccessorGrant != closed.SuccessorGrant || settled.Terminal == nil || kernel.ManagedDigest(*settled.Terminal) != kernel.ManagedDigest(before) {
				t.Fatalf("original transfer not atomically closed: state=%s err=%v", settled.Selection.Mode, err)
			}
			receipt, err = ks.LoadOwnedRetirement(t.Context(), closed.SuccessorGrant.ID)
			if err != nil || !receipt.MatchesManagedClosure(settled, before) {
				t.Fatalf("original retirement not acknowledged: phase=%s err=%v", receipt.Phase, err)
			}
			if lease := readManagedRecoveryLease(t, t.Context(), db, root.ExecutionRefID); lease.ExecutionOwner != "" || lease.ExecutionLeaseID != "" || lease.ExecutionEpoch != closed.SuccessorGrant.ExecutionEpoch {
				t.Fatalf("repair created or retained execution owner: %+v", lease)
			}
			turn, err := host.GetTurn(t.Context(), turnID)
			if err != nil || turn.Status != harness.TurnCompleted {
				t.Fatalf("Host did not close original dispatch: %s %v", turn.Status, err)
			}
			if got := readManagedRecoveryRecords[model.Request](t, filepath.Join(files, "model.jsonl")); !reflect.DeepEqual(got, calls) {
				t.Fatal("closure replay regenerated a model request")
			}
			if got := readManagedRecoveryRecords[managedRecoveryEffect](t, filepath.Join(files, "ledger.jsonl")); !reflect.DeepEqual(got, effects) {
				t.Fatal("closure replay repeated business effect")
			}
		})
	}
}

func TestRealPostgresManagedOriginalObligationsAcrossProcessCrash(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	testManagedAgentSessionRecoveryAcrossProcessCrash(t, dsn, true, false, true)
}

func testManagedAgentSessionRecoveryAcrossProcessCrash(t *testing.T, dsn string, facade, workingState bool, obligations ...bool) {
	t.Helper()
	db, isolatedDSN := managedRecoveryDatabase(t, dsn)
	if err := postgresadapter.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	files := t.TempDir()
	combined := len(obligations) == 1 && obligations[0]
	if combined {
		if err := appendManagedRecoveryRecord(filepath.Join(files, "inputs.jsonl"), agent.RunInput{Sequence: 1, ID: "first", Text: "first managed steering"}); err != nil {
			t.Fatal(err)
		}
	}
	var expectedWorkingState *runtimecontext.WorkingState
	if workingState {
		expectedWorkingState = prepareManagedRecoveryWorkingState(t, db, files)
	}
	runManagedRecoveryProcess(t, isolatedDSN, files, "crash", managedRecoveryCrashExit, facade)

	store, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	turnID, originalRoot := managedRecoveryRoot(t, store)
	runID := originalRoot.ExecutionRefID
	turn, err := store.GetTurn(t.Context(), turnID)
	if err != nil || turn.ContextRef.Generation < 2 {
		t.Fatalf("compacted Turn did not survive child exit: %+v err=%v", turn, err)
	}
	checkpoint, err := store.GetContextCheckpoint(t.Context(), turn.ContextCheckpointID)
	if err != nil || !runtimecontext.ValidCheckpoint(checkpoint) ||
		checkpoint.ContentHashEncoding != runtimecontext.CheckpointContentHashEncodingLosslessJSON || len(checkpoint.ArtifactIDs) != 1 {
		t.Fatalf("missing exact archive checkpoint: %+v err=%v", checkpoint, err)
	}
	assertManagedRecoveryWorkingState(t, store, checkpoint, expectedWorkingState)
	if expectedWorkingState != nil {
		// New Host-derived input must not replace an already-frozen pending
		// request or the committed Context during nil-Transcript recovery.
		changed := runtimecontext.CloneWorkingState(expectedWorkingState)
		changed.Version++
		changed.Goal = "unadmitted replacement working state"
		writeManagedRecoveryWorkingState(t, files, changed)
	}
	archiveID := checkpoint.ArtifactIDs[0]
	archive, err := store.GetContextArtifact(t.Context(), archiveID)
	if err != nil || !strings.Contains(string(archive.ContentJSON), "shipping_cents=700") {
		t.Fatalf("archived invoice fact not durable: id=%s err=%v", archiveID, err)
	}
	pending, err := postgresadapter.NewKernelStore(db).Load(t.Context(), runID)
	if err != nil || pending.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("pending Kernel Run not durable: %+v err=%v", pending.Run, err)
	}
	before, err := agent.ViewState(pending)
	if err != nil || len(before.ModelInvocations) != 2 ||
		before.ModelInvocations[0].Status != agent.ModelInvocationConsumed ||
		before.ModelInvocations[1].Status != agent.ModelInvocationPending {
		t.Fatalf("crash did not follow committed Tool and frozen model intent: %+v err=%v", before, err)
	}
	frozen := before.ModelInvocations[1]
	if frozen.Request.MaxOutputTokens != 512 {
		t.Fatal("pending post-Tool intent lost its frozen per-model output ceiling")
	}
	originalTransfer, transferErr := postgresadapter.NewKernelStore(db).LoadManagedRecovery(t.Context(), runID)
	if transferErr != nil || originalTransfer.LegacyState != "" ||
		originalTransfer.SafePoint.ModelInvocationID != frozen.ID || originalTransfer.SafePoint.ModelRequestHash != frozen.RequestHash ||
		!reflect.DeepEqual(originalTransfer.SafePoint.Source, pending) {
		t.Fatalf("original process did not atomically commit exact managed source: %+v err=%v", originalTransfer, transferErr)
	}
	if combined {
		proof, ok := originalTransfer.SafePoint.AgentObligations()
		ledger, e := store.LoadBudget(t.Context(), turnID)
		if !ok || e != nil || before.InputCursor != 1 || proof.InputCursor != 1 || proof.LedgerID != turnID ||
			ledger.View("").Usage.LLMCalls != 1 || ledger.View("").Usage.ToolCalls != 1 ||
			ledger.View("").Reserved != (budget.Usage{}) || !strings.Contains(requestMessages(frozen.Request), "first managed steering") {
			t.Fatalf("original obligations missing: proof=%+v ledger=%+v err=%v", proof, ledger.View(""), e)
		}
		if err := appendManagedRecoveryRecord(filepath.Join(files, "inputs.jsonl"), agent.RunInput{Sequence: 2, ID: "late", Text: "late managed steering"}); err != nil {
			t.Fatal(err)
		}
	}
	originalHostAuthority, authorityErr := store.ManagedRecoveryAuthority(t.Context())
	if authorityErr != nil || originalHostAuthority != originalTransfer.SafePoint.Host.HostAuthority {
		t.Fatalf("original Host namespace mismatch: %v", authorityErr)
	}
	if !managedRecoveryHasToolResult(frozen.Request.Messages, managedRecoverySubtotalCall) {
		t.Fatal("frozen next request lost the committed subtotal Tool receipt")
	}
	calls := readManagedRecoveryRecords[model.Request](t, filepath.Join(files, "model.jsonl"))
	effects := readManagedRecoveryRecords[managedRecoveryEffect](t, filepath.Join(files, "ledger.jsonl"))
	if len(calls) != 1 || len(effects) != 1 || effects[0].SubtotalCents != 5100 {
		t.Fatalf("unexpected pre-crash work: model=%d ledger=%+v", len(calls), effects)
	}
	// Drop the observer connection too. The recovery child receives only the
	// database address and fixture files, never an in-memory Store or Runner.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err = sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	runManagedRecoveryProcess(t, isolatedDSN, files, "resume", 0, facade)

	reopened := openManagedRecoveryDB(t, isolatedDSN)
	reopenedStore, err := harnesspostgres.New(reopened)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := postgresadapter.NewKernelStore(reopened).Load(t.Context(), runID)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted || completed.Result == nil {
		t.Fatalf("same Kernel Run did not complete: %+v err=%v", completed.Run, err)
	}
	finalTransfer, reservationErr := postgresadapter.NewKernelStore(reopened).LoadManagedRecovery(t.Context(), runID)
	reservation := finalTransfer.Selection
	if reservationErr != nil || reservation.Mode != kernel.ManagedResume ||
		finalTransfer.SafePoint.ID != originalTransfer.SafePoint.ID ||
		!reflect.DeepEqual(reservation.Cancellation, kernel.ManagedCancellation{}) {
		t.Fatalf("original immutable RESUME reservation lost: %+v err=%v", reservation, reservationErr)
	}
	authority, authorityErr := reopenedStore.ManagedRecoveryAuthority(t.Context())
	if authorityErr != nil || authority != originalHostAuthority {
		t.Fatalf("Host namespace changed on restart: %v", authorityErr)
	}
	closureRT, closureErr := kernel.New(kernel.Dependencies{Store: postgresadapter.NewKernelStore(reopened)})
	if closureErr != nil {
		t.Fatal(closureErr)
	}
	closure, closureErr := closureRT.ManagedRecoveryClosure(t.Context(), finalTransfer.SafePoint.ID)
	if closureErr != nil || !closure.Matches(reservation) || !reflect.DeepEqual(closure.Snapshot(), completed) {
		t.Fatalf("original transfer closure missing: %v", closureErr)
	}
	if closureErr = reopenedStore.CloseManagedRecovery(t.Context(), closure); closureErr != nil {
		t.Fatalf("exact closure replay: %v", closureErr)
	}
	var answer string
	if err = json.Unmarshal(completed.Result.Content, &answer); err != nil || answer != managedRecoveryAnswer {
		t.Fatalf("invoice task result=%q err=%v", answer, err)
	}
	after, err := agent.ViewState(completed)
	if err != nil || len(after.ModelInvocations) != 3 ||
		after.ModelInvocations[1].ID != frozen.ID ||
		after.ModelInvocations[1].RequestHash != frozen.RequestHash {
		t.Fatalf("recovery replaced logical model intent: %+v err=%v", after, err)
	}
	for _, invocation := range after.ModelInvocations {
		if invocation.Status != agent.ModelInvocationConsumed {
			t.Fatalf("unconsumed model receipt: %+v", invocation)
		}
	}
	if after.Budget.Usage.ToolCalls != 2 || after.Budget.Usage.LLMCalls != 3 {
		t.Fatalf("receipt accounting was duplicated: %+v", after.Budget.Usage)
	}
	finalTurn, err := reopenedStore.GetTurn(t.Context(), turnID)
	if err != nil || finalTurn.Status != harness.TurnCompleted ||
		finalTurn.ContextCheckpointID != checkpoint.ID || finalTurn.ContextRef != turn.ContextRef {
		t.Fatalf("root Turn or Context lineage not restored: %+v err=%v", finalTurn, err)
	}
	root, err := reopenedStore.GetInvocation(t.Context(), originalRoot.ID)
	if err != nil || root.ExecutionRefID != runID || root.Attempt != 1 || root.Status != harness.InvocationCompleted {
		t.Fatalf("root invocation did not finish its original attempt: %+v err=%v", root, err)
	}
	restoredArchive, err := reopenedStore.GetContextArtifact(t.Context(), archiveID)
	if err != nil || !reflect.DeepEqual(restoredArchive, archive) {
		t.Fatalf("exact artifact changed across process recovery: id=%s err=%v", archiveID, err)
	}
	effects = readManagedRecoveryRecords[managedRecoveryEffect](t, filepath.Join(files, "ledger.jsonl"))
	calls = readManagedRecoveryRecords[model.Request](t, filepath.Join(files, "model.jsonl"))
	if len(effects) != 1 || effects[0].RunID != runID || effects[0].CallID != managedRecoverySubtotalCall {
		t.Fatalf("committed Tool was repeated or rebound: %+v", effects)
	}
	// JSON log round trips omit empty optional slices; normalize both through
	// the same public clone before comparing every request field.
	if combined {
		ledger, e := reopenedStore.LoadBudget(t.Context(), turnID)
		if e != nil || after.InputCursor != 2 || !ledger.FinishedRuns[runID] ||
			ledger.View("").Usage.LLMCalls != 3 || ledger.View("").Usage.ToolCalls != 2 ||
			ledger.View("").Reserved != (budget.Usage{}) || len(calls) != 3 || frozen.SamplingPlan == nil {
			t.Fatalf("combined recovery lost obligations: cursor=%d ledger=%+v calls=%d err=%v", after.InputCursor, ledger.View(""), len(calls), e)
		}
		hash, e := model.PhysicalRequestHash(calls[1])
		if e != nil || hash != frozen.SamplingPlan.RequestHash || calls[1].InvocationID != frozen.ID ||
			strings.Contains(requestMessages(calls[1]), "late managed steering") || !strings.Contains(requestMessages(calls[2]), "late managed steering") {
			t.Fatalf("frozen sampling plan or late input changed: %v", e)
		}
	} else if len(calls) != 3 || !reflect.DeepEqual(model.CloneRequest(calls[1]), model.CloneRequest(frozen.Request)) {
		t.Fatalf("pending request was re-prepared or redispatched: model calls=%d", len(calls))
	}
	for index, request := range calls {
		if request.MaxOutputTokens != 512 {
			t.Fatalf("request %d lost its frozen per-model output ceiling: %d", index, request.MaxOutputTokens)
		}
		if request.RunID != runID || strings.Contains(requestMessages(request), strings.Repeat("archived invoice discussion; ", 20)) {
			t.Fatalf("request %d changed Run or re-expanded archived history", index)
		}
		if index < 2 && strings.Contains(requestMessages(request), "shipping_cents=700") {
			t.Fatalf("request %d bypassed the archive reader", index)
		}
		assertManagedRecoveryWorkingStateProjection(t, request, expectedWorkingState)
	}
	requestBytes := make([]int, len(calls))
	totalRequestBytes := 0
	for index, request := range calls {
		raw, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		requestBytes[index] = len(raw)
		totalRequestBytes += len(raw)
	}
	t.Logf("workingState=%t facade=%t provider_calls=%d business_ledger_effects=%d request_bytes=%v total_request_bytes=%d",
		workingState, facade, len(calls), len(effects), requestBytes, totalRequestBytes)
	if !managedRecoveryHasToolResult(calls[2].Messages, managedRecoveryArchiveCall) ||
		!strings.Contains(requestMessages(calls[2]), "shipping_cents=700") {
		t.Fatal("resumed Agent did not retrieve the archived fact through its Tool")
	}
	latestTurn, err := reopenedStore.GetTurn(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	latestCheckpoint, err := reopenedStore.GetContextCheckpoint(t.Context(), latestTurn.ContextCheckpointID)
	if err != nil || !runtimecontext.ValidCheckpoint(latestCheckpoint) ||
		latestCheckpoint.ContentHashEncoding != runtimecontext.CheckpointContentHashEncodingLosslessJSON {
		t.Fatalf("recovered Checkpoint lost its lossless content hash: %+v err=%v", latestCheckpoint, err)
	}
	assertManagedRecoveryWorkingState(t, reopenedStore, latestCheckpoint, expectedWorkingState)
}

// The helper is selected explicitly by the parent test and skips in normal runs.
func TestManagedAgentSessionRecoveryProcess(t *testing.T) {
	phase := os.Getenv("GAOGE_TEST_MANAGED_RECOVERY_PHASE")
	if phase == "" {
		t.Skip("subprocess helper")
	}
	starting := phase == "crash" || phase == "opaque-crash"
	retirementCut := ""
	switch phase {
	case "crash", "opaque-crash", "resume", "resume-closed", "refresh-closed":
	case "resume-finish-before", "resume-finish-after":
		retirementCut = strings.TrimPrefix(phase, "resume-")
	default:
		t.Fatalf("unknown helper phase %q", phase)
	}
	db := openManagedRecoveryDB(t, os.Getenv("TEST_POSTGRES_DSN"))
	files := os.Getenv("GAOGE_TEST_MANAGED_RECOVERY_FILES")
	facade, err := strconv.ParseBool(os.Getenv("GAOGE_TEST_MANAGED_RECOVERY_FACADE"))
	if err != nil {
		t.Fatal(err)
	}
	session := newManagedRecoverySessionWithComposition(t, db, files, starting, phase != "opaque-crash", facade, retirementCut)
	if starting {
		_, err := session.Prompt(t.Context(), managedRecoveryTurn, managedRecoveryPrompt)
		t.Fatalf("expected process exit after second model intent commit, got %v", err)
	}
	var completed harness.Snapshot
	switch phase {
	case "refresh-closed":
		completed, err = session.Refresh(t.Context(), managedRecoveryTurn)
		if err != nil {
			t.Fatal(err)
		}
	case "resume-closed":
		completed, err = session.Resume(t.Context(), managedRecoveryTurn)
		if err != nil {
			t.Fatal(err)
		}
	default:
		completed = resumeManagedRecoveryAfterLease(t, db, session, files)
	}
	if completed.Turn.Status != harness.TurnCompleted || completed.Output == nil {
		t.Fatalf("nil-Transcript Session.Resume: turn=%+v", completed.Turn)
	}
	if completed.Config.Model != "fixture-invoice" || completed.Config.MaxModelOutputTokens != 512 {
		t.Fatalf("resume lost the sealed configuration: %+v", completed.Config)
	}
	// The public recovery entry point is safe to call again on a terminal Turn.
	replayed, err := session.Resume(t.Context(), managedRecoveryTurn)
	if err != nil || replayed.Turn.Revision != completed.Turn.Revision {
		t.Fatalf("terminal Session.Resume changed the Turn: %+v err=%v", replayed.Turn, err)
	}
}

// Only a conflict observed while the original lease is still live is eligible
// for this bounded wait. Changed ownership/revision and all other errors fail
// immediately; neither the DB clock nor the persisted expiry is modified.
func resumeManagedRecoveryAfterLease(t *testing.T, db *gorm.DB, session *harness.SessionClient, files string) harness.Snapshot {
	t.Helper()
	store, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	_, root := managedRecoveryRoot(t, store)
	runID := root.ExecutionRefID
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	old := readManagedRecoveryLease(t, ctx, db, runID)
	if old.Status != string(kernel.RunStatusRunning) || old.ExecutionEpoch == 0 ||
		old.ExecutionOwner != "managed-recovery-before" || old.ExecutionLeaseID == "" ||
		old.ExecutionExpiresAt == nil || !old.StoreNow.Before(*old.ExecutionExpiresAt) {
		t.Fatalf("recovery did not observe the crashed process's live lease: %+v", old)
	}
	beforeHost, hostErr := session.Load(ctx, managedRecoveryTurn)
	if hostErr != nil {
		t.Fatal(hostErr)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	blocked := false
	for {
		observed := readManagedRecoveryLease(t, ctx, db, runID)
		if !sameManagedRecoveryLease(old, observed) {
			t.Fatalf("original Run or lease changed before takeover: before=%+v current=%+v", old, observed)
		}
		wasLive := observed.StoreNow.Before(*observed.ExecutionExpiresAt)
		snapshot, err := session.Resume(ctx, managedRecoveryTurn)
		if err == nil {
			taken := readManagedRecoveryLease(t, ctx, db, runID)
			if !blocked || taken.ExecutionEpoch != old.ExecutionEpoch+1 {
				t.Fatalf("recovery did not prove one blocked attempt then one new ownership epoch: blocked=%v before=%+v after=%+v", blocked, old, taken)
			}
			return snapshot
		}
		if !errors.Is(err, kernel.ErrExecutionConflict) || !wasLive {
			t.Fatalf("resume failed outside the confirmed live-lease boundary: %v", err)
		}
		current := readManagedRecoveryLease(t, ctx, db, runID)
		if !sameManagedRecoveryLease(old, current) {
			t.Fatalf("rejected resume mutated Run or ownership: before=%+v current=%+v", old, current)
		}
		calls := readManagedRecoveryRecords[model.Request](t, filepath.Join(files, "model.jsonl"))
		effects := readManagedRecoveryRecords[managedRecoveryEffect](t, filepath.Join(files, "ledger.jsonl"))
		if len(calls) != 1 || len(effects) != 1 {
			t.Fatalf("live lease allowed physical execution: model=%d ledger=%d", len(calls), len(effects))
		}
		afterHost, hostErr := session.Load(ctx, managedRecoveryTurn)
		if hostErr != nil || !reflect.DeepEqual(beforeHost, afterHost) {
			t.Fatalf("liveowner rejection changed full Host state: %v", hostErr)
		}
		blocked = true
		select {
		case <-ctx.Done():
			t.Fatalf("Store lease did not permit recovery within the deadline: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

type managedRecoveryLease struct {
	Status             string
	Revision           uint64
	ExecutionEpoch     uint64
	ExecutionOwner     string
	ExecutionLeaseID   string
	ExecutionExpiresAt *time.Time
	StoreNow           time.Time
}

func readManagedRecoveryLease(t *testing.T, ctx context.Context, db *gorm.DB, runID string) managedRecoveryLease {
	t.Helper()
	var state managedRecoveryLease
	err := db.WithContext(ctx).Table((postgresmodels.KernelRunRecord{}).TableName()).
		Select("status, revision, execution_epoch, execution_owner, execution_lease_id, execution_expires_at, clock_timestamp() AS store_now").
		Where("run_id = ?", runID).Take(&state).Error
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func sameManagedRecoveryLease(old, current managedRecoveryLease) bool {
	return old.Status == current.Status && old.Revision == current.Revision &&
		old.ExecutionEpoch == current.ExecutionEpoch && old.ExecutionOwner == current.ExecutionOwner &&
		old.ExecutionLeaseID == current.ExecutionLeaseID &&
		old.ExecutionExpiresAt != nil && current.ExecutionExpiresAt != nil &&
		old.ExecutionExpiresAt.Equal(*current.ExecutionExpiresAt)
}

type managedRecoveryCrashStore struct {
	*postgresadapter.KernelStore
	modelIntents int
}

func (store *managedRecoveryCrashStore) ApplyExecution(ctx context.Context, lease kernel.ExecutionLease, revision uint64, mutation kernel.StoreMutation) (kernel.Snapshot, error) {
	snapshot, err := store.KernelStore.ApplyExecution(ctx, lease, revision, mutation)
	if err == nil {
		for _, event := range mutation.Events {
			if event.Type == "agent.model_invocation.pending" {
				store.modelIntents++
				if store.modelIntents == 2 {
					os.Exit(managedRecoveryCrashExit)
				}
			}
		}
	}
	return snapshot, err
}

type managedRecoveryRetirementCrashStore struct {
	*postgresadapter.KernelStore
	cut string
}

func (s *managedRecoveryRetirementCrashStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	if g.AdmissionOperation == "managed_resume" && s.cut == "finish-before" {
		os.Exit(managedRecoveryCrashExit)
	}
	err := s.KernelStore.FinishChildExecution(ctx, g, p)
	if err == nil && g.AdmissionOperation == "managed_resume" && s.cut == "finish-after" {
		os.Exit(managedRecoveryCrashExit)
	}
	return err
}

type managedRecoveryClock struct{}

func (managedRecoveryClock) Now() time.Time { return time.Now().UTC() }

func newManagedRecoverySession(t *testing.T, db *gorm.DB, files string, crash bool, managed ...bool) *harness.SessionClient {
	t.Helper()
	return newManagedRecoverySessionWithComposition(t, db, files, crash, len(managed) == 0 || managed[0], false)
}

func newManagedRecoverySessionWithComposition(t *testing.T, db *gorm.DB, files string, crash, managed, facade bool, retirementCut ...string) *harness.SessionClient {
	t.Helper()
	hostStore, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	var kernelStore kernel.Store = postgresadapter.NewKernelStore(db)
	if crash {
		kernelStore = &managedRecoveryCrashStore{KernelStore: postgresadapter.NewKernelStore(db)}
	} else if len(retirementCut) == 1 && retirementCut[0] != "" {
		kernelStore = &managedRecoveryRetirementCrashStore{KernelStore: postgresadapter.NewKernelStore(db), cut: retirementCut[0]}
	}
	runtime, err := kernel.New(kernel.Dependencies{Store: kernelStore, ManagedRecoveryHost: hostStore})
	if err != nil {
		t.Fatal(err)
	}
	registrations := managedRecoveryToolRegistrations(files)
	resolveWindow := func(_ context.Context, request model.Request) (runtimecontext.ModelWindow, error) {
		if request.Model != "fixture-invoice" {
			return runtimecontext.ModelWindow{}, runtimecontext.ErrModelWindowUnknown
		}
		// Deliberately small fixture capacity forces history archival. This is
		// not an asserted production model capacity or token-admission planner.
		return runtimecontext.ModelWindow{ContextTokens: 12000, ReservedOutputTokens: 512}, nil
	}
	owner := "managed-recovery-after"
	if crash {
		owner = "managed-recovery-before"
	}
	execution := agent.ExecutionPolicy{
		Model: agent.CallPolicy{Timeout: time.Second, MaxAttempts: 1, MaxConcurrency: 1},
		Tool:  agent.CallPolicy{Timeout: time.Second, MaxAttempts: 1, MaxConcurrency: 1},
	}
	runExecution := &agent.RunExecution{OwnerID: owner, LeaseDuration: managedRecoveryLeaseDuration}
	options := managedRecoveryConfiguredSessionOptions(crash)
	if crash {
		options.Transcript = managedRecoveryWorkingStateTranscript(t, files, options.Transcript)
	}
	if facade {
		options.Config.ToolKeys = nil
		options.Config.ToolPolicies = nil
		var inputs agent.RunInputSource
		if _, e := os.Stat(filepath.Join(files, "inputs.jsonl")); e == nil {
			inputs = managedRecoveryFileInputs{path: filepath.Join(files, "inputs.jsonl")}
			options.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 6, MaxToolCalls: 5}
		} else if !os.IsNotExist(e) {
			t.Fatal(e)
		}
		session, err := harness.OpenAgentSession(harness.AgentSessionOptions{
			Inputs:  inputs,
			Runtime: runtime, Store: hostStore, Model: managedRecoveryModel{logPath: filepath.Join(files, "model.jsonl")},
			LocalTools: managedRecoverySessionTools(files), Session: options,
			Context:   harness.AgentSessionContextOptions{ResolveWindow: resolveWindow, ManagedRecovery: managed, ReadArtifacts: true},
			Execution: execution, RunExecution: runExecution,
		})
		if err != nil {
			t.Fatal(err)
		}
		return session
	}
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	registrations = append(registrations, harness.ContextArtifactToolRegistration(harness.NewContextArtifactToolHandler(hostStore, nil)))
	registry, err := tools.NewRegistry(registrations)
	if err != nil {
		t.Fatal(err)
	}
	bounded, err := harness.NewBoundedContextAdapter(hostStore, manager, resolveWindow, nil)
	if err != nil {
		t.Fatal(err)
	}
	dependencies := agent.Dependencies{
		Runtime: runtime, Model: managedRecoveryModel{logPath: filepath.Join(files, "model.jsonl")},
		Catalog: registry, Executor: registry, BoundedContext: bounded, ManagedRecovery: managed,
		RunMiddleware: []plugin.RunMiddleware{bounded},
		RunExecution:  runExecution, Execution: execution,
	}
	if !managed {
		ordinary, ordinaryErr := harness.NewContextRunMiddleware(hostStore, nil)
		if ordinaryErr != nil {
			t.Fatal(ordinaryErr)
		}
		dependencies.BoundedContext = nil
		dependencies.RunMiddleware = []plugin.RunMiddleware{ordinary}
		dependencies.PrepareModelRequest = bounded.Prepare
	}
	direct, err := agent.NewRunner(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	host, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: direct, Store: hostStore, Clock: managedRecoveryClock{},
		Context: manager, Catalog: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The explicit recovery path also proves a handle needs no replacement Config.
	if !crash {
		options.Config = harness.ConfigSnapshot{}
	}
	session, err := host.OpenSession(options)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func managedRecoveryToolRegistrations(files string) []tools.Registration {
	return []tools.Registration{
		{
			Definition: tools.Definition{
				Key: managedRecoveryTool, Name: "record_subtotal", Description: "Calculate and record an invoice subtotal in cents.",
				InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["units","unitPriceCents"],"properties":{"units":{"type":"integer","minimum":1},"unitPriceCents":{"type":"integer","minimum":0}}}`),
			},
			Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
				var input struct {
					Units          int `json:"units"`
					UnitPriceCents int `json:"unitPriceCents"`
				}
				if err := json.Unmarshal(request.Call.Arguments, &input); err != nil {
					return tools.ExecutionResult{}, err
				}
				effect := managedRecoveryEffect{RunID: request.RunID, CallID: request.Call.ID, SubtotalCents: input.Units * input.UnitPriceCents}
				if err := appendManagedRecoveryRecord(filepath.Join(files, "ledger.jsonl"), effect); err != nil {
					return tools.ExecutionResult{}, err
				}
				content, err := json.Marshal(effect)
				return tools.ExecutionResult{Content: content, Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, err
			}),
		},
	}
}

func managedRecoverySessionTools(files string) []harness.SessionTool {
	registrations := managedRecoveryToolRegistrations(files)
	return []harness.SessionTool{{
		Registration: registrations[0],
		Policy: harness.ToolPolicySnapshot{
			DefinitionVersion: "fixture-v1", ApprovalMode: "never", SideEffectLevel: "write",
		},
	}}
}

func managedRecoveryConfiguredSessionOptions(crash bool) harness.SessionOptions {
	options := managedRecoverySessionOptions()
	options.Config = harness.ConfigSnapshot{

		ToolPolicies: []harness.ToolPolicySnapshot{
			{Key: managedRecoveryTool, DefinitionVersion: "fixture-v1", ApprovalMode: "never", SideEffectLevel: "write"},
			harness.ContextArtifactToolPolicySnapshot(),
		},
		ContextPolicy: runtimecontext.Policy{PreserveRecentTurns: 1, MinimumRecentTurns: 1, MaxCompactionTokens: 128}, AgentSpec: harness.AgentSpec{Model: "fixture-invoice", ToolKeys: []string{managedRecoveryTool, harness.ContextArtifactToolKey},
			MaxModelOutputTokens: 512},
	}
	if crash {
		options.Transcript = harness.TranscriptSourceFunc(func(_ context.Context, _, _ harness.HostRef, prompt string) (*harness.ContextSeed, error) {
			return &harness.ContextSeed{
				SourcePath: []string{"old-user", "old-answer", "invoice-user"},
				Entries: []runtimecontext.Entry{
					{ID: "old-user", SourceID: "old-user", TurnID: "old", Message: model.Message{Role: model.RoleUser, Content: model.TextParts("Remember the invoice terms.")}},
					{ID: "old-answer", SourceID: "old-answer", TurnID: "old", Message: model.Message{Role: model.RoleAssistant, Content: model.TextParts(strings.Repeat("archived invoice discussion; ", 1000) + "shipping_cents=700")}},
					{ID: "invoice-user", SourceID: "invoice-user", TurnID: managedRecoveryTurn, Required: true, Message: model.Message{Role: model.RoleUser, Content: model.TextParts(prompt)}},
				},
			}, nil
		})
	}
	return options
}

func managedRecoverySessionOptions() harness.SessionOptions {
	return harness.SessionOptions{
		HostThread: harness.HostRef{Kind: "conversation", ID: "managed-invoice-recovery"},
		Actor:      kernel.ActorRef{TenantID: "test-tenant", ActorID: "test-actor"},
		Thread:     kernel.ThreadRef{Kind: "conversation", ID: "managed-invoice-recovery"},
	}
}

func managedRecoveryRoot(t *testing.T, store harness.Store) (string, harness.Invocation) {
	t.Helper()
	sessionID, err := harness.SessionID(managedRecoverySessionOptions().HostThread)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := harness.TurnID(sessionID, harness.HostRef{Kind: "conversation_turn", ID: managedRecoveryTurn})
	if err != nil {
		t.Fatal(err)
	}
	invocations, err := store.ListInvocations(t.Context(), turnID)
	if err != nil || len(invocations) != 1 || invocations[0].ParentItemID != "" ||
		invocations[0].ExecutionRefID == "" {
		t.Fatalf("expected one durable direct Agent root: %+v err=%v", invocations, err)
	}
	// The stored root is authoritative. Session.Prompt supplies the host-stable
	// turn ID as RequestID, which need not equal the derived durable Turn ID.
	return turnID, invocations[0]
}

type managedRecoveryEffect struct {
	RunID         string `json:"runID"`
	CallID        string `json:"callID"`
	SubtotalCents int    `json:"subtotalCents"`
}

type managedRecoveryModel struct{ logPath string }

func (client managedRecoveryModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	if err := appendManagedRecoveryRecord(client.logPath, request); err != nil {
		return model.Response{}, err
	}
	var subtotal *managedRecoveryEffect
	var excerpt string
	for _, message := range request.Messages {
		if message.Role != model.RoleTool {
			continue
		}
		text, textOnly := model.PlainText(message.Content)
		if !textOnly {
			return model.Response{}, errors.New("managed recovery Tool reply was not text-only")
		}
		switch message.ToolCallID {
		case managedRecoverySubtotalCall:
			subtotal = &managedRecoveryEffect{}
			if err := json.Unmarshal([]byte(text), subtotal); err != nil {
				return model.Response{}, err
			}
		case managedRecoveryArchiveCall:
			var page struct {
				Content string `json:"content"`
				Found   bool   `json:"found"`
			}
			if err := json.Unmarshal([]byte(text), &page); err != nil {
				return model.Response{}, err
			}
			if !page.Found {
				return model.Response{}, errors.New("archived shipping term was not found")
			}
			excerpt = page.Content
		}
	}
	if subtotal == nil {
		return model.Response{ToolCalls: []tools.Call{{ID: managedRecoverySubtotalCall, ToolKey: managedRecoveryTool, Arguments: json.RawMessage(`{"units":3,"unitPriceCents":1700}`)}}}, nil
	}
	if excerpt == "" {
		match := regexp.MustCompile(`artifact_id=([^\s>]+)`).FindStringSubmatch(requestMessages(request))
		if len(match) != 2 {
			return model.Response{}, errors.New("model request has no exact archived context reference")
		}
		arguments, err := json.Marshal(map[string]any{"artifactID": match[1], "query": "shipping_cents=", "limit": 64})
		return model.Response{ToolCalls: []tools.Call{{ID: managedRecoveryArchiveCall, ToolKey: harness.ContextArtifactToolKey, Arguments: arguments}}}, err
	}
	match := regexp.MustCompile(`shipping_cents=(\d+)`).FindStringSubmatch(excerpt)
	if len(match) != 2 {
		return model.Response{}, errors.New("archive reader omitted the exact invoice shipping amount")
	}
	shipping, err := strconv.Atoi(match[1])
	return model.Response{Content: fmt.Sprintf("invoice_total_cents=%d", subtotal.SubtotalCents+shipping)}, err
}

func managedRecoveryHasToolResult(messages []model.Message, callID string) bool {
	for _, message := range messages {
		if message.Role == model.RoleTool && message.ToolCallID == callID {
			return true
		}
	}
	return false
}

func requestMessages(request model.Request) string {
	var content strings.Builder
	for _, message := range request.Messages {
		text, textOnly := model.PlainText(message.Content)
		if !textOnly {
			// A diagnostic transcript comparison must never silently drop
			// the frozen media evidence even when this fixture uses text.
			raw, _ := json.Marshal(message.Content)
			content.Write(raw)
		} else {
			content.WriteString(text)
		}
		content.WriteByte('\n')
	}
	return content.String()
}

func appendManagedRecoveryRecord(path string, value any) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	encodeErr := json.NewEncoder(file).Encode(value)
	syncErr := file.Sync()
	return errors.Join(encodeErr, syncErr, file.Close())
}

func readManagedRecoveryRecords[T any](t *testing.T, path string) []T {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close fixture record file: %v", err)
		}
	}()
	decoder := json.NewDecoder(file)
	var records []T
	for {
		var record T
		err := decoder.Decode(&record)
		if errors.Is(err, io.EOF) {
			return records
		}
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
}

func runManagedRecoveryProcess(t *testing.T, dsn, files, phase string, wantExit int, facade ...bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManagedAgentSessionRecoveryProcess$", "-test.count=1")
	command.Env = append(os.Environ(), "TEST_POSTGRES_DSN="+dsn,
		"GAOGE_TEST_MANAGED_RECOVERY_PHASE="+phase, "GAOGE_TEST_MANAGED_RECOVERY_FILES="+files,
		"GAOGE_TEST_MANAGED_RECOVERY_FACADE="+strconv.FormatBool(len(facade) == 1 && facade[0]))
	output, err := command.CombinedOutput()
	if wantExit == 0 && err == nil {
		return
	}
	var exitErr *exec.ExitError
	if wantExit != 0 && errors.As(err, &exitErr) && exitErr.ExitCode() == wantExit {
		return
	}
	t.Fatalf("%s child exit: want=%d err=%v\n%s", phase, wantExit, err, output)
}

func openManagedRecoveryDB(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func managedRecoveryDatabase(t *testing.T, dsn string) (*gorm.DB, string) {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	admin := openManagedRecoveryDB(t, dsn)
	schema := "managed_agent_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err = admin.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`).Error; err != nil {
			t.Errorf("drop isolated schema: %v", err)
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	isolatedDSN := parsed.String()
	return openManagedRecoveryDB(t, isolatedDSN), isolatedDSN
}

// Test-only append-only durable input source, independently reopened in the
// successor process. The fixture directory is exclusive to one root Run.
type managedRecoveryFileInputs struct{ path string }

func (s managedRecoveryFileInputs) BindingID() string { return "managed-recovery-fixture-inputs" }
func (s managedRecoveryFileInputs) ReadRunInputs(ctx context.Context, _ kernel.Run, cursor uint64, limit int) ([]agent.RunInput, error) {
	f, e := os.Open(s.path)
	if e != nil {
		return nil, e
	}
	defer func() { _ = f.Close() }()
	d := json.NewDecoder(f)
	var out []agent.RunInput
	var last uint64
	for {
		if e = ctx.Err(); e != nil {
			return nil, e
		}
		var in agent.RunInput
		e = d.Decode(&in)
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return nil, e
		}
		if in.Sequence != last+1 {
			return nil, errors.New("invalid input sequence")
		}
		last = in.Sequence
		if in.Sequence > cursor && len(out) < limit {
			out = append(out, in)
		}
	}
	if last < cursor {
		return nil, errors.New("input source lost committed cursor")
	}
	return out, nil
}

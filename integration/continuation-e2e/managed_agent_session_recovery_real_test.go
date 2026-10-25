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
	db, isolatedDSN := managedRecoveryDatabase(t, dsn)
	if err := postgresadapter.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	files := t.TempDir()
	runManagedRecoveryProcess(t, isolatedDSN, files, "crash", managedRecoveryCrashExit)

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
	if err != nil || !runtimecontext.ValidCheckpoint(checkpoint) || len(checkpoint.ArtifactIDs) != 1 {
		t.Fatalf("missing exact archive checkpoint: %+v err=%v", checkpoint, err)
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
	runManagedRecoveryProcess(t, isolatedDSN, files, "resume", 0)

	reopened := openManagedRecoveryDB(t, isolatedDSN)
	reopenedStore, err := harnesspostgres.New(reopened)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := postgresadapter.NewKernelStore(reopened).Load(t.Context(), runID)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted || completed.Result == nil {
		t.Fatalf("same Kernel Run did not complete: %+v err=%v", completed.Run, err)
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
	if len(calls) != 3 || !reflect.DeepEqual(model.CloneRequest(calls[1]), model.CloneRequest(frozen.Request)) {
		t.Fatalf("pending request was re-prepared or redispatched: model calls=%d", len(calls))
	}
	for index, request := range calls {
		if request.RunID != runID || strings.Contains(requestMessages(request), strings.Repeat("archived invoice discussion; ", 20)) {
			t.Fatalf("request %d changed Run or re-expanded archived history", index)
		}
		if index < 2 && strings.Contains(requestMessages(request), "shipping_cents=700") {
			t.Fatalf("request %d bypassed the archive reader", index)
		}
	}
	if !managedRecoveryHasToolResult(calls[2].Messages, managedRecoveryArchiveCall) ||
		!strings.Contains(requestMessages(calls[2]), "shipping_cents=700") {
		t.Fatal("resumed Agent did not retrieve the archived fact through its Tool")
	}
}

// The helper is selected explicitly by the parent test and skips in normal runs.
func TestManagedAgentSessionRecoveryProcess(t *testing.T) {
	phase := os.Getenv("GAOGE_TEST_MANAGED_RECOVERY_PHASE")
	if phase == "" {
		t.Skip("subprocess helper")
	}
	if phase != "crash" && phase != "resume" {
		t.Fatalf("unknown helper phase %q", phase)
	}
	db := openManagedRecoveryDB(t, os.Getenv("TEST_POSTGRES_DSN"))
	files := os.Getenv("GAOGE_TEST_MANAGED_RECOVERY_FILES")
	session := newManagedRecoverySession(t, db, files, phase == "crash")
	if phase == "crash" {
		_, err := session.Prompt(t.Context(), managedRecoveryTurn, managedRecoveryPrompt)
		t.Fatalf("expected process exit after second model intent commit, got %v", err)
	}
	completed := resumeManagedRecoveryAfterLease(t, db, session, files)
	if completed.Turn.Status != harness.TurnCompleted || completed.Output == nil {
		t.Fatalf("nil-Transcript Session.Resume: turn=%+v", completed.Turn)
	}
	if completed.Config.Model != "fixture-invoice" {
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

type managedRecoveryClock struct{}

func (managedRecoveryClock) Now() time.Time { return time.Now().UTC() }

func newManagedRecoverySession(t *testing.T, db *gorm.DB, files string, crash bool) *harness.SessionClient {
	t.Helper()
	hostStore, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	var kernelStore kernel.Store = postgresadapter.NewKernelStore(db)
	if crash {
		kernelStore = &managedRecoveryCrashStore{KernelStore: postgresadapter.NewKernelStore(db)}
	}
	runtime, err := kernel.New(kernel.Dependencies{Store: kernelStore})
	if err != nil {
		t.Fatal(err)
	}
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	registrations := []tools.Registration{
		harness.ContextArtifactToolRegistration(harness.NewContextArtifactToolHandler(hostStore, nil)),
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
	registry, err := tools.NewRegistry(registrations)
	if err != nil {
		t.Fatal(err)
	}
	prepare, err := harness.NewManagedContextPreparer(hostStore, manager, func(_ context.Context, request model.Request) (runtimecontext.ModelWindow, error) {
		if request.Model != "fixture-invoice" {
			return runtimecontext.ModelWindow{}, runtimecontext.ErrModelWindowUnknown
		}
		// Deliberately small fixture capacity forces history archival. This is
		// not an asserted production model capacity or token-admission planner.
		return runtimecontext.ModelWindow{ContextTokens: 12000, ReservedOutputTokens: 512}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	hydrate, err := harness.NewContextRunMiddleware(hostStore, nil)
	if err != nil {
		t.Fatal(err)
	}
	owner := "managed-recovery-after"
	if crash {
		owner = "managed-recovery-before"
	}
	direct, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: managedRecoveryModel{logPath: filepath.Join(files, "model.jsonl")},
		Catalog: registry, Executor: registry, PrepareModelRequest: prepare,
		RunMiddleware: []plugin.RunMiddleware{hydrate},
		RunExecution:  &agent.RunExecution{OwnerID: owner, LeaseDuration: managedRecoveryLeaseDuration},
		Execution: agent.ExecutionPolicy{
			Model: agent.CallPolicy{Timeout: time.Second, MaxAttempts: 1, MaxConcurrency: 1},
			Tool:  agent.CallPolicy{Timeout: time.Second, MaxAttempts: 1, MaxConcurrency: 1},
		},
	})
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
	options := managedRecoverySessionOptions()
	if crash {
		options.Config = harness.ConfigSnapshot{
			Model: "fixture-invoice", ToolKeys: []string{managedRecoveryTool, harness.ContextArtifactToolKey},
			ToolPolicies: []harness.ToolPolicySnapshot{
				{Key: managedRecoveryTool, DefinitionVersion: "fixture-v1", ApprovalMode: "never", SideEffectLevel: "write"},
				harness.ContextArtifactToolPolicySnapshot(),
			},
			ContextPolicy: runtimecontext.Policy{PreserveRecentTurns: 1, MinimumRecentTurns: 1, MaxCompactionTokens: 128},
		}
		options.Transcript = harness.TranscriptSourceFunc(func(_ context.Context, _, _ harness.HostRef, prompt string) (*harness.ContextSeed, error) {
			return &harness.ContextSeed{
				SourcePath: []string{"old-user", "old-answer", "invoice-user"},
				Entries: []runtimecontext.Entry{
					{ID: "old-user", SourceID: "old-user", TurnID: "old", Message: model.Message{Role: model.RoleUser, Content: "Remember the invoice terms."}},
					{ID: "old-answer", SourceID: "old-answer", TurnID: "old", Message: model.Message{Role: model.RoleAssistant, Content: strings.Repeat("archived invoice discussion; ", 1000) + "shipping_cents=700"}},
					{ID: "invoice-user", SourceID: "invoice-user", TurnID: managedRecoveryTurn, Required: true, Message: model.Message{Role: model.RoleUser, Content: prompt}},
				},
			}, nil
		})
	}
	// Recovery supplies neither TranscriptSource nor a replacement Config.
	session, err := host.OpenSession(options)
	if err != nil {
		t.Fatal(err)
	}
	return session
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
		invocations[0].ExecutionClass != harness.ExecutionAgent || invocations[0].ExecutionRefID == "" {
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
		switch message.ToolCallID {
		case managedRecoverySubtotalCall:
			subtotal = &managedRecoveryEffect{}
			if err := json.Unmarshal([]byte(message.Content), subtotal); err != nil {
				return model.Response{}, err
			}
		case managedRecoveryArchiveCall:
			var page struct {
				Content string `json:"content"`
				Found   bool   `json:"found"`
			}
			if err := json.Unmarshal([]byte(message.Content), &page); err != nil {
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
		content.WriteString(message.Content)
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

func runManagedRecoveryProcess(t *testing.T, dsn, files, phase string, wantExit int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManagedAgentSessionRecoveryProcess$", "-test.count=1")
	command.Env = append(os.Environ(), "TEST_POSTGRES_DSN="+dsn,
		"GAOGE_TEST_MANAGED_RECOVERY_PHASE="+phase, "GAOGE_TEST_MANAGED_RECOVERY_FILES="+files)
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

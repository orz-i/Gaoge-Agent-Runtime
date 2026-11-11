package harnesspostgres_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	kernelmemory "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"gorm.io/gorm"
)

// These fault-injection suites deliberately live in the SQL adapter module:
// the core Harness test module must not depend on its own downstream adapter.
type pr103ApplicationSQLExecutor struct {
	calls int
}

func (e *pr103ApplicationSQLExecutor) ExecuteApplicationCapability(_ context.Context, req harness.ApplicationCapabilityRequest) (harness.ApplicationCapabilityResult, error) {
	e.calls++
	if req.Invocation.ExecutionClass != harness.ExecutionApplication || req.Session.Actor.ActorID == "" {
		return harness.ApplicationCapabilityResult{}, harness.ErrConflict
	}
	return harness.ApplicationCapabilityResult{
		Interaction: &harness.ApplicationInteractionRequest{
			ApplicationRef: &harness.HostRef{Kind: "story", ID: "sql-app-recovery"},
			ArtifactRefs:   []harness.HostRef{{Kind: "story_candidate_portfolio", ID: "sql-portfolio"}},
			Key:            "story.author_decision", Kind: harness.InteractionChoice,
			Schema: json.RawMessage("{\"type\":\"object\"}"),
		},
	}, nil
}

type pr103ApplicationSQLHandler struct{ calls int }

func (h *pr103ApplicationSQLHandler) HandleInteractionResponse(context.Context, harness.InteractionResponseContext) (harness.InteractionResponseResult, error) {
	h.calls++
	return harness.InteractionResponseResult{OutputRefs: []harness.HostRef{{Kind: "story_author_decision", ID: "sql-decision"}}}, nil
}

type pr103ApplicationSQLAgent struct {
	runtime *kernel.Runtime
	resumes int
}

func (*pr103ApplicationSQLAgent) StartRun(context.Context, agent.StartRequest) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, harness.ErrInvalidRequest
}

func (a *pr103ApplicationSQLAgent) Resume(ctx context.Context, id string, revision uint64) (kernel.Snapshot, error) {
	a.resumes++
	return a.runtime.Load(ctx, id)
}

func (*pr103ApplicationSQLAgent) ResolveApproval(context.Context, string, uint64, plugin.ApprovalResponse) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, harness.ErrInvalidRequest
}

type pr103ApplicationSQLFault struct {
	harness.Store
	terminal    atomic.Bool
	childReopen atomic.Bool
}

func (s *pr103ApplicationSQLFault) UpdateTurn(ctx context.Context, turn harness.Turn, rev uint64) (harness.Turn, error) {
	if turn.Status == harness.TurnCompleted && s.terminal.CompareAndSwap(false, true) {
		return harness.Turn{}, errors.New("original terminal Turn SQL CAS interrupted")
	}
	if turn.Status == harness.TurnRunning && !s.childReopen.Load() {
		owners, err := s.ListInvocations(ctx, turn.ID)
		if err == nil {
			for _, owner := range owners {
				if owner.ParentItemID != "" && owner.ExecutionClass == harness.ExecutionApplication &&
					owner.Status == harness.InvocationCompleted && s.childReopen.CompareAndSwap(false, true) {
					return harness.Turn{}, errors.New("child complete before parent reopen SQL CAS")
				}
			}
		}
	}
	return s.Store.UpdateTurn(ctx, turn, rev)
}

func pr103SQLBacking(t *testing.T, db *gorm.DB) harness.Store {
	t.Helper()
	store, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func pr103SQLRuntime(t *testing.T) *kernel.Runtime {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: kernelmemory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func pr103SQLRunner(t *testing.T, store harness.Store, runtime *kernel.Runtime,
	feature *pr103ApplicationSQLAgent, executor *pr103ApplicationSQLExecutor, handler *pr103ApplicationSQLHandler,
) *harness.Runner {
	t.Helper()
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: feature, Store: store, Applications: executor, Interactions: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func pr103SQLResponse() harness.ResolveInteractionRequest {
	return harness.ResolveInteractionRequest{Response: json.RawMessage("{\"outcome\":\"select\",\"selectedCandidateIDs\":[\"candidate_1\"]}")}
}

func pr103SQLRootRecovery(t *testing.T, db *gorm.DB) {
	t.Helper()
	base := pr103SQLBacking(t, db)
	fault := &pr103ApplicationSQLFault{Store: base}
	runtime := pr103SQLRuntime(t)
	feature := &pr103ApplicationSQLAgent{runtime: runtime}
	executor := &pr103ApplicationSQLExecutor{}
	handler := &pr103ApplicationSQLHandler{}
	runner := pr103SQLRunner(t, fault, runtime, feature, executor, handler)
	waiting, err := runner.StartApplicationTurn(t.Context(), harness.ApplicationTurnRequest{
		StartRequest: harness.StartRequest{
			HostThread: harness.HostRef{Kind: "conversation", ID: "sql-app-repair-thread"},
			HostTurn:   harness.HostRef{Kind: "conversation_turn", ID: "sql-app-repair-turn"},
			Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
			Thread:     kernel.ThreadRef{Kind: "conversation", ID: "sql-app-repair-thread"},
			RequestID:  "sql-original-application", Goal: "verify terminal recovery",
		}, CapabilityKey: "story.author_decision", DefinitionVersion: "v1", Input: json.RawMessage("{}"),
	})
	if err != nil || len(waiting.Interactions) != 1 {
		t.Fatalf("SQL initial callback: %v %#v", err, waiting.Interactions)
	}
	interaction := waiting.Interactions[0]
	_, err = runner.ResolveInteraction(t.Context(), waiting.Turn.ID, interaction.ID, pr103SQLResponse())
	if err == nil || !fault.terminal.Load() {
		t.Fatalf("original terminal SQL CAS failure was not injected: %v", err)
	}
	owners, err := base.ListInvocations(t.Context(), waiting.Turn.ID)
	if err != nil || len(owners) != 1 || owners[0].Status != harness.InvocationCompleted {
		t.Fatalf("original Application completion missing: %v %#v", err, owners)
	}
	items, err := base.ListItems(t.Context(), waiting.Turn.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var receipt harness.Item
	for _, item := range items {
		if strings.HasPrefix(item.ID, "hbizverified_") {
			receipt = item
		}
	}
	if receipt.ID == "" {
		t.Fatal("missing committed Host success receipt")
	}
	// A second SQL Store object models the recovery process; Turn and Item
	// authorities remain in the original PostgreSQL/SQLite transaction store.
	restarted := pr103SQLRunner(t, pr103SQLBacking(t, db), runtime, feature, executor, handler)
	corrupted := json.RawMessage("{\"interactionID\":\"forged\",\"boundary\":\"host_response_verified\"}")
	if err = db.Exec("UPDATE agent_harness_items SET payload_json = ? WHERE id = ?", string(corrupted), receipt.ID).Error; err != nil {
		t.Fatal(err)
	}
	_, err = restarted.Refresh(t.Context(), waiting.Turn.ID)
	if !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("corrupt original SQL Host receipt authorized Turn: %v", err)
	}
	if err = db.Exec("UPDATE agent_harness_items SET payload_json = ? WHERE id = ?", string(receipt.Payload), receipt.ID).Error; err != nil {
		t.Fatal(err)
	}
	// A product response replay is often the first recovery call after a
	// process crash. It must repair the committed Application result without
	// invoking a nonexistent Kernel Run or redispatching Host work.
	replayedFirst, err := restarted.ResolveInteraction(t.Context(), waiting.Turn.ID, interaction.ID, pr103SQLResponse())
	if err != nil || replayedFirst.Turn.Status != harness.TurnCompleted {
		t.Fatalf("first SQL response replay could not repair completed Application: status=%s err=%v", replayedFirst.Turn.Status, err)
	}
	recovered, err := restarted.Refresh(t.Context(), waiting.Turn.ID)
	if err != nil || recovered.Turn.Status != harness.TurnCompleted {
		t.Fatalf("valid original SQL callback did not repair Turn: status=%s err=%v", recovered.Turn.Status, err)
	}
	reconciled, err := restarted.ReconcileInteraction(t.Context(), waiting.Turn.ID, interaction.ID)
	if err != nil || reconciled.Turn.Status != harness.TurnCompleted {
		t.Fatalf("SQL terminal recovery not idempotent: %s %v", reconciled.Turn.Status, err)
	}
	_, err = restarted.ResolveInteraction(t.Context(), waiting.Turn.ID, interaction.ID, pr103SQLResponse())
	if err != nil || executor.calls != 1 || handler.calls != 1 {
		t.Fatalf("SQL root replay duplicated Host callback: err=%v executor=%d handler=%d", err, executor.calls, handler.calls)
	}
}

func pr103SQLSeedRootAgent(t *testing.T, store harness.Store, runtime *kernel.Runtime) (string, string) {
	t.Helper()
	now := time.Now().UTC()
	hostThread := harness.HostRef{Kind: "conversation", ID: "sql-child-parent-repair-thread"}
	sid, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	hostTurn := harness.HostRef{Kind: "conversation_turn", ID: "sql-child-parent-repair-turn"}
	tid, err := harness.TurnID(sid, hostTurn)
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: hostThread.ID}
	if _, _, err = store.CreateSession(t.Context(), harness.Session{
		ID: sid, HostThread: hostThread, Actor: actor, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	config, err := harness.SealConfigSnapshot(tid, harness.ConfigSnapshot{AdmissionHash: strings.Repeat("a", 64)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutConfigSnapshot(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CreateTurn(t.Context(), harness.Turn{
		ID: tid, SessionID: sid, HostTurn: hostTurn, ConfigSnapshotID: config.ID,
		Status: harness.TurnRunning, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	parentRunID := "parent-agent-run"
	if _, err = runtime.Create(t.Context(), kernel.CreateRequest{
		ID: parentRunID, Kind: agent.RunKind, Actor: actor, Thread: thread,
		RequestID: "parent", Goal: "coordinate", State: json.RawMessage("{}"),
	}); err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]any{"goal": "coordinate", "actor": actor, "thread": thread})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(input)
	parentID, err := harness.InvocationID(tid, "", harness.CapabilityAgent, "parent")
	if err != nil {
		t.Fatal(err)
	}
	parent := harness.Invocation{
		ID: parentID, TurnID: tid, CapabilityKey: harness.CapabilityAgent,
		DefinitionVersion: "v1", ExecutionClass: harness.ExecutionAgent,
		Input: input, InputHash: fmt.Sprintf("%x", sum), ExecutionRefID: parentRunID,
		Status: harness.InvocationRunning, Attempt: 1, OutputRefs: []harness.HostRef{},
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err = store.CreateInvocation(t.Context(), parent); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.AppendItem(t.Context(), harness.Item{
		ID: "parent-invocation-item", TurnID: tid, Kind: harness.ItemInvocation,
		Status: harness.ItemStarted, RunID: parentRunID, InvocationID: parent.ID,
		Payload:   json.RawMessage("{}"),
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return tid, "parent-invocation-item"
}

func pr103SQLChildRecovery(t *testing.T, db *gorm.DB) {
	t.Helper()
	base := pr103SQLBacking(t, db)
	fault := &pr103ApplicationSQLFault{Store: base}
	runtime := pr103SQLRuntime(t)
	feature := &pr103ApplicationSQLAgent{runtime: runtime}
	executor := &pr103ApplicationSQLExecutor{}
	handler := &pr103ApplicationSQLHandler{}
	runner := pr103SQLRunner(t, fault, runtime, feature, executor, handler)
	tid, parentItem := pr103SQLSeedRootAgent(t, base, runtime)
	waiting, err := runner.StartApplicationInvocation(t.Context(), tid, harness.ApplicationInvocationRequest{
		ParentItemID: parentItem, RequestID: "sql-application-child", Goal: "ask host",
		CapabilityKey: "story.author_decision", DefinitionVersion: "v1", Input: json.RawMessage("{}"),
	})
	if err != nil || len(waiting.Interactions) != 1 {
		t.Fatalf("SQL child waiting setup: %v %#v", err, waiting.Interactions)
	}
	interaction := waiting.Interactions[0]
	_, err = runner.ResolveInteraction(t.Context(), tid, interaction.ID, pr103SQLResponse())
	if err == nil || !fault.childReopen.Load() {
		t.Fatalf("SQL child post-commit interruption missing: %v", err)
	}
	restarted := pr103SQLRunner(t, pr103SQLBacking(t, db), runtime, feature, executor, handler)
	observed, err := restarted.Refresh(t.Context(), tid)
	if err != nil || observed.Turn.Status == harness.TurnCompleted || feature.resumes != 0 {
		t.Fatalf("SQL read-only recovery issued unapproved physical Resume: %s %v calls=%d", observed.Turn.Status, err, feature.resumes)
	}
	_, err = restarted.ReconcileInteraction(t.Context(), tid, interaction.ID)
	if !errors.Is(err, harness.ErrApplicationParentContinuationUnverified) {
		t.Fatalf("SQL parent Resume incorrectly reported confirmed: %v", err)
	}
	_, err = restarted.ResolveInteraction(t.Context(), tid, interaction.ID, pr103SQLResponse())
	if !errors.Is(err, harness.ErrApplicationParentContinuationUnverified) {
		t.Fatalf("SQL Host response replay incorrectly reported parent progress: %v", err)
	}
	_, err = restarted.ResumeTurn(t.Context(), tid)
	if err != nil || feature.resumes != 1 {
		t.Fatalf("explicit SQL original parent Resume failed: %v calls=%d", err, feature.resumes)
	}
	_, err = restarted.ReconcileInteraction(t.Context(), tid, interaction.ID)
	if err != nil {
		t.Fatalf("SQL parent settled grant not recognized: %v", err)
	}
	_, err = restarted.ResolveInteraction(t.Context(), tid, interaction.ID, pr103SQLResponse())
	if err != nil || feature.resumes != 1 || handler.calls != 1 || executor.calls != 1 {
		t.Fatalf("SQL replay duplicated physical work: %v parent=%d host=%d app=%d", err, feature.resumes, handler.calls, executor.calls)
	}
}

func TestSQLApplicationOriginalResponseTerminalRepairAndParentContinuation(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		_, db := newStoreWithDB(t)
		pr103SQLRootRecovery(t, db)
	})
	t.Run("child", func(t *testing.T) {
		_, db := newStoreWithDB(t)
		pr103SQLChildRecovery(t, db)
	})
}

func TestRealPostgresApplicationOriginalResponseTerminalRepairAndParentContinuation(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	t.Run("root", func(t *testing.T) {
		db, _ := openIsolatedHarnessPostgres(t, dsn)
		if err := harnesspostgres.Migrate(db); err != nil {
			t.Fatal(err)
		}
		pr103SQLRootRecovery(t, db)
	})
	t.Run("child", func(t *testing.T) {
		db, _ := openIsolatedHarnessPostgres(t, dsn)
		if err := harnesspostgres.Migrate(db); err != nil {
			t.Fatal(err)
		}
		pr103SQLChildRecovery(t, db)
	})
}

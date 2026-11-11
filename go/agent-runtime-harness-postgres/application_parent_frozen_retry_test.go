package harnesspostgres_test

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
)

// These are the same deterministic IDs used by the first-party Runner; the
// Test's new Kernel Attempt must have its real ownership metadata, otherwise
// a read-only Refresh could fail before reaching the parent receipt observer.
func pr103RetryID(prefix string, values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte(strings.TrimSpace(value)))
		_, _ = hash.Write([]byte{0})
	}
	return prefix + "_" + fmt.Sprintf("%x", hash.Sum(nil))[:24]
}

// A genuine Store Retry plus a new first-party physical parent Resume may
// not prove an already completed child whose frozen harnessRoot belongs to
// the old parent Run/Attempt. Test both SQLite and real PostgreSQL; the
// original callback must never dispatch a second Host side effect.
func pr103SQLParentRetryOriginalAttemptRecheck(t *testing.T, db *gorm.DB) {
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
		ParentItemID: parentItem, RequestID: "sql-frozen-attempt-child",
		Goal: "original child question", CapabilityKey: "story.author_decision",
		DefinitionVersion: "v1", Input: json.RawMessage(`{}`),
	})
	if err != nil || len(waiting.Interactions) != 1 {
		t.Fatalf("create original child interaction: %v %#v", err, waiting.Interactions)
	}
	interaction := waiting.Interactions[0]
	_, err = runner.ResolveInteraction(t.Context(), tid, interaction.ID, pr103SQLResponse())
	if err == nil || !fault.childReopen.Load() {
		t.Fatalf("original child callback didn't stop after completion: %v", err)
	}
	if handler.calls != 1 || executor.calls != 1 || feature.resumes != 0 {
		t.Fatalf("original Host call count incorrect: callback=%d executor=%d resumes=%d", handler.calls, executor.calls, feature.resumes)
	}
	oldRoot, err := base.GetInvocation(t.Context(), pr103RootID(t, base, tid))
	if err != nil {
		t.Fatal(err)
	}
	if oldRoot.Attempt != 1 || oldRoot.Status != harness.InvocationRunning {
		t.Fatalf("invalid original root: %+v", oldRoot)
	}
	owners, err := base.ListInvocations(t.Context(), tid)
	if err != nil {
		t.Fatal(err)
	}
	var child harness.Invocation
	for _, value := range owners {
		if value.ID != oldRoot.ID && value.ExecutionClass == harness.ExecutionApplication {
			child = value
		}
	}
	if child.Status != harness.InvocationCompleted {
		t.Fatalf("original Host callback not terminal: %+v", child)
	}
	// Validate the frozen original child source without reconstructing it
	// from the current root after retry.
	var envelope struct {
		HarnessRoot struct {
			InvocationID   string `json:"invocationID"`
			ExecutionRefID string `json:"executionRefID"`
			Attempt        int    `json:"attempt"`
		} `json:"harnessRoot"`
	}
	if err = json.Unmarshal(child.Input, &envelope); err != nil ||
		envelope.HarnessRoot.InvocationID != oldRoot.ID ||
		envelope.HarnessRoot.ExecutionRefID != oldRoot.ExecutionRefID ||
		envelope.HarnessRoot.Attempt != 1 {
		t.Fatalf("original frozen provenance is inconsistent: %+v %v", envelope, err)
	}
	originalSnapshot, err := runtime.Load(t.Context(), oldRoot.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Apply(t.Context(), oldRoot.ExecutionRefID, originalSnapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusFailed, State: originalSnapshot.State, ErrorCode: "kernel.failed",
	})
	if err != nil {
		t.Fatalf("cannot terminate original physical Run: %v", err)
	}
	// Fault injection: a terminal Kernel projection reached durable Harness
	// state while child had already committed. Run failure is verified above.
	if err = db.Exec("UPDATE agent_harness_invocations SET status = ?, revision = revision + 1 WHERE id = ?",
		string(harness.InvocationFailed), oldRoot.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("UPDATE agent_harness_turns SET status = ?, revision = revision + 1 WHERE id = ?",
		string(harness.TurnFailed), tid).Error; err != nil {
		t.Fatal(err)
	}
	failedRoot, err := base.GetInvocation(t.Context(), oldRoot.ID)
	if err != nil || failedRoot.Status != harness.InvocationFailed {
		t.Fatalf("failed root fixture invalid: %+v %v", failedRoot, err)
	}
	retried, err := base.RetryInvocation(t.Context(), oldRoot.ID, failedRoot.Revision,
		"parent-agent-run-attempt-2", time.Now().UTC())
	if err != nil || retried.Attempt != 2 || retried.ExecutionRefID == oldRoot.ExecutionRefID {
		t.Fatalf("real SQL Store Retry failed: %+v %v", retried, err)
	}
	retried.Status = harness.InvocationRunning
	retried.UpdatedAt = time.Now().UTC()
	retried, err = base.UpdateInvocation(t.Context(), retried, retried.Revision)
	if err != nil {
		t.Fatalf("real SQL Retry Accepted→Running failed: %v", err)
	}
	// The second physical Kernel Run must be a fully owner-verified Agent
	// Attempt, not a fake Item. ResumeTurn makes its original Grant/Receipt.
	_, err = runtime.Create(t.Context(), kernel.CreateRequest{
		ID: retried.ExecutionRefID, Kind: agent.RunKind,
		Actor:     kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread:    kernel.ThreadRef{Kind: "conversation", ID: "sql-child-parent-repair-thread"},
		RequestID: pr103RetryID("hretry", retried.ID, "2"),
		Goal:      "coordinate", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted := pr103SQLRunner(t, pr103SQLBacking(t, db), runtime, feature, executor, handler)
	resumed, err := restarted.ResumeTurn(t.Context(), tid)
	if err != nil || feature.resumes != 1 {
		t.Fatalf("first-party Attempt 2 Resume failed: err=%v calls=%d turn=%s", err, feature.resumes, resumed.Turn.Status)
	}
	// The Return receipt itself is valid, but the original child was
	// admitted under Attempt 1 and cannot adopt the new physical effect.
	receiptFound := false
	items, err := base.ListItems(t.Context(), tid, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.InvocationID == oldRoot.ID && item.RunID == retried.ExecutionRefID &&
			strings.HasPrefix(item.ID, "hdispatchdone_") {
			receiptFound = true
		}
	}
	if !receiptFound {
		t.Fatal("new Attempt did not persist a physical-return receipt")
	}
	observed, err := restarted.ReconcileInteraction(t.Context(), tid, interaction.ID)
	if !errors.Is(err, harness.ErrConflict) || observed.Turn.ID != tid {
		t.Fatalf("Attempt 2 receipt falsely settled Attempt 1 child: status=%s err=%v", observed.Turn.Status, err)
	}
	_, err = restarted.ResolveInteraction(t.Context(), tid, interaction.ID, pr103SQLResponse())
	if !errors.Is(err, harness.ErrConflict) || handler.calls != 1 || executor.calls != 1 || feature.resumes != 1 {
		t.Fatalf("child replay bypassed original identity: err=%v callback=%d executor=%d resume=%d",
			err, handler.calls, executor.calls, feature.resumes)
	}
}

func pr103RootID(t *testing.T, store harness.Store, turnID string) string {
	t.Helper()
	owners, err := store.ListInvocations(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range owners {
		if owner.ParentItemID == "" {
			return owner.ID
		}
	}
	t.Fatal("missing root Invocation")
	return ""
}

func TestSQLApplicationParentFrozenAttemptRecheck(t *testing.T) {
	_, db := newStoreWithDB(t)
	pr103SQLParentRetryOriginalAttemptRecheck(t, db)
}

func TestRealPostgresApplicationParentFrozenAttemptRecheck(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, _ := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	pr103SQLParentRetryOriginalAttemptRecheck(t, db)
}

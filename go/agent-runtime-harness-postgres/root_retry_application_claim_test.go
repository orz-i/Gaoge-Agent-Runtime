package harnesspostgres_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
)

// An Application Accepted->Running claim uses the same Turn Store lock as
// retry, not the Kernel physical-dispatch grant. The memory and SQL stores
// must both refuse rotating any Invocation in the Turn while a child Host
// effect has no verified outcome.
func TestRetryRefusesUnresolvedApplicationChildAcrossStores(t *testing.T) {
	for name, create := range rootInvocationStores() {
		t.Run(name, func(t *testing.T) {
			assertApplicationClaimFencesRetry(t, create(t), "retry-claimed-"+name)
		})
	}
}

func assertApplicationClaimFencesRetry(t *testing.T, store harness.Store, turnID string) {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	turn := harness.Turn{
		ID: turnID, SessionID: "session-" + turnID,
		HostTurn:         harness.HostRef{Kind: "turn", ID: turnID},
		ConfigSnapshotID: "config-" + turnID, Status: harness.TurnRunning,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.CreateTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"goal":"frozen"}`)
	digest := sha256.Sum256(input)
	root := harness.Invocation{
		ID: "root-" + turnID, TurnID: turnID, CapabilityKey: "app.root",
		ExecutionClass: harness.ExecutionApplication, ExecutionRefID: "root-run-1-" + turnID,
		Input: input, InputHash: hex.EncodeToString(digest[:]), Status: harness.InvocationRunning,
		Attempt: 1, Revision: 1, OutputRefs: []harness.HostRef{},
		CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.CreateInvocation(ctx, root); err != nil {
		t.Fatal(err)
	}
	child := harness.Invocation{
		ID: "child-" + turnID, TurnID: turnID, ParentItemID: "host-artifact",
		CapabilityKey: "app.effect", ExecutionClass: harness.ExecutionApplication,
		ExecutionRefID: "child-run-1-" + turnID, Input: input, InputHash: hex.EncodeToString(digest[:]),
		Status: harness.InvocationAccepted, Attempt: 1, Revision: 1,
		OutputRefs: []harness.HostRef{}, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.CreateInvocation(ctx, child); err != nil {
		t.Fatal(err)
	}
	child.Status = harness.InvocationRunning // durable external-effect claim
	child, err := store.UpdateInvocation(ctx, child, child.Revision)
	if err != nil {
		t.Fatal(err)
	}
	root.Status = harness.InvocationFailed
	root, err = store.UpdateInvocation(ctx, root, root.Revision)
	if err != nil {
		t.Fatal(err)
	}
	turn.Status = harness.TurnFailed
	if _, err := store.UpdateTurn(ctx, turn, turn.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetryInvocation(ctx, root.ID, root.Revision, "root-run-2-"+turnID, now.Add(time.Second)); !errors.Is(err, harness.ErrApplicationOutcomeUnknown) {
		t.Fatalf("rotated root despite a child Application external-effect claim: %v", err)
	}
	unchanged, err := store.GetInvocation(ctx, root.ID)
	if err != nil || unchanged.ExecutionRefID != root.ExecutionRefID ||
		unchanged.Attempt != 1 || unchanged.Revision != root.Revision {
		t.Fatalf("rejected retry mutated root Attempt: %+v %v", unchanged, err)
	}
	child.Status = harness.InvocationCompleted // externally verified outcome
	if _, err := store.UpdateInvocation(ctx, child, child.Revision); err != nil {
		t.Fatal(err)
	}
	retried, err := store.RetryInvocation(ctx, root.ID, root.Revision, "root-run-2-"+turnID, now.Add(time.Second))
	if err != nil || retried.Attempt != 2 || retried.Status != harness.InvocationAccepted {
		t.Fatalf("verified child did not release retry: %+v %v", retried, err)
	}
}

// Exercise the same CAS fence over actual PostgreSQL transactions rather
// than relying on SQLite's transaction semantics alone.
func TestRealPostgresRetryRefusesUnresolvedApplicationChild(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, _ := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	store, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	assertApplicationClaimFencesRetry(t, store, "real-postgres-app-claim")
}

// An in-flight grandchild grant also fences retry of an intermediate
// Invocation. It is unsafe to check pending grants only by the parent ID:
// the child may pass its pre-effect fence and still be about to StartRun.
func TestIntermediateRetryRefusesPendingDescendantGrantAcrossStores(t *testing.T) {
	for name, create := range rootInvocationStores() {
		t.Run(name, func(t *testing.T) {
			assertPendingDescendantGrantFencesRetry(t, create(t), "descendant-grant-"+name)
		})
	}
}

func assertPendingDescendantGrantFencesRetry(t *testing.T, store harness.Store, turnID string) {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	turn := harness.Turn{
		ID: turnID, SessionID: "session-" + turnID,
		HostTurn:         harness.HostRef{Kind: "turn", ID: turnID},
		ConfigSnapshotID: "config-" + turnID, Status: harness.TurnRunning,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.CreateTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"goal":"physical attempt"}`)
	digest := sha256.Sum256(input)
	parent := harness.Invocation{
		ID: "middle-" + turnID, TurnID: turnID, ParentItemID: "host-parent",
		CapabilityKey: "app.middle", ExecutionClass: harness.ExecutionApplication,
		ExecutionRefID: "middle-run-1-" + turnID, Input: input, InputHash: hex.EncodeToString(digest[:]),
		Status: harness.InvocationFailed, Attempt: 1, Revision: 1, OutputRefs: []harness.HostRef{},
		CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.CreateInvocation(ctx, parent); err != nil {
		t.Fatal(err)
	}
	child := harness.Invocation{
		ID: "grandchild-" + turnID, TurnID: turnID, ParentItemID: "middle-lifecycle",
		CapabilityKey: harness.CapabilityTeam, ExecutionClass: harness.ExecutionTeam,
		ExecutionRefID: "child-run-1-" + turnID, Input: input, InputHash: hex.EncodeToString(digest[:]),
		Status: harness.InvocationAccepted, Attempt: 1, Revision: 1, OutputRefs: []harness.HostRef{},
		CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.CreateInvocation(ctx, child); err != nil {
		t.Fatal(err)
	}
	grant := harness.Item{
		ID: "unsettled-grandchild-" + turnID, TurnID: turnID, InvocationID: child.ID, RunID: child.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		ParentItemID: harness.ExecutionDispatchParentItemID,
		Payload:      json.RawMessage(`{"phase":"start","physicalRunID":"unknown"}`),
		CreatedAt:    now, UpdatedAt: now,
	}
	if _, _, err := store.AppendItem(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetryInvocation(ctx, parent.ID, parent.Revision, "middle-run-2-"+turnID, now.Add(time.Second)); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("parent retry crossed grandchild's still-open physical grant: %v", err)
	}
	settled := harness.Item{
		ID: "settled-grandchild-" + turnID, TurnID: turnID,
		InvocationID: child.ID, RunID: child.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
		ParentItemID: grant.ID, Payload: json.RawMessage(`{"boundary":"physical_dispatch_rejected"}`),
		CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.AppendItem(ctx, settled); err != nil {
		t.Fatal(err)
	}
	retried, err := store.RetryInvocation(ctx, parent.ID, parent.Revision, "middle-run-2-"+turnID, now.Add(time.Second))
	if err != nil || retried.Attempt != 2 {
		t.Fatalf("settled grant did not release intermediate retry: %+v %v", retried, err)
	}
}

func TestRealPostgresIntermediateRetryRefusesPendingDescendantGrant(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, _ := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	store, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	assertPendingDescendantGrantFencesRetry(t, store, "real-postgres-descendant-grant")
}

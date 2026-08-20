package harnesspostgres_test

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge/sdk/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge/sdk/go/agent-runtime-harness-postgres"
	runtimecontext "github.com/orz-i/Gaoge/sdk/go/agent-runtime/context"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const testHarnessExecutionRefID = "hr_pg"

func TestStorePersistsHarnessLifecycleAndCAS(t *testing.T) {
	store := newStore(t)
	now := time.Date(2026, 8, 15, 3, 0, 0, 0, time.UTC)
	session := harness.Session{
		ID: "hs_pg", HostThread: harness.HostRef{Kind: "conversation", ID: "thread_pg"},
		Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	assertSessionLifecycle(t, store, session)
	config, err := harness.SealConfigSnapshot("ht_pg", harness.ConfigSnapshot{
		Environment: harness.VersionRef{ID: "general", Revision: 7}, Model: "model",
		Commands: harness.FirstPartyCommandDescriptors(),
	}, now)
	if err != nil {
		t.Fatalf("seal config: %v", err)
	}
	assertConfigLifecycle(t, store, config)
	contextSnapshot := runtimecontext.Snapshot{
		ID: "ctx_pg", RunID: testHarnessExecutionRefID, Revision: 1, ThreadPathHash: "path_pg",
		Content: json.RawMessage(`{"instructions":"sealed"}`), ContentHash: "content_pg",
	}
	assertContextSnapshotLifecycle(t, store, contextSnapshot)
	turn := harness.Turn{
		ID: "ht_pg", SessionID: session.ID, HostTurn: harness.HostRef{Kind: "conversation_turn", ID: "turn_pg"},
		ConfigSnapshotID: config.ID, ContextSnapshotID: contextSnapshot.ID,
		ContextRef: harness.ContextRef{
			ID: contextSnapshot.ID, Revision: contextSnapshot.Revision,
			ThreadPathHash: contextSnapshot.ThreadPathHash, ContentHash: contextSnapshot.ContentHash,
		},
		Status: harness.TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	updated := assertTurnLifecycle(t, store, turn, now)
	if _, err = store.UpdateTurn(t.Context(), updated, 1); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("stale turn update error = %v", err)
	}
	invocation := assertInvocationLifecycle(t, store, turn.ID, now)
	assertInteractionLifecycle(t, store, turn.ID, invocation.ID, now)
	assertItemLifecycle(t, store, turn.ID, invocation.ID, now)
}

func TestStoreRetriesInvocationWithAtomicAttemptRotation(t *testing.T) {
	store := newStore(t)
	now := time.Date(2026, 8, 20, 4, 20, 0, 0, time.UTC)
	value := createRetryInvocationFixture(t, store, now)
	retried, err := store.RetryInvocation(
		t.Context(), value.ID, value.Revision, "run_retry_pg_2", now.Add(time.Second),
	)
	assertRetriedInvocation(t, store, value, retried, err)
}

func TestStoreRetriesTopLevelInvocationAndReopensTurnAtomically(t *testing.T) {
	store := newStore(t)
	now := time.Date(2026, 8, 20, 4, 25, 0, 0, time.UTC)
	turn, invocation := createTopLevelRetryFixture(t, store, now)
	retried, err := store.RetryInvocation(t.Context(), invocation.ID, invocation.Revision, "run_retry_root_pg_2", now.Add(time.Second))
	assertTopLevelInvocationRetried(t, retried, err)
	assertTopLevelTurnReopened(t, store, turn.ID)
}

func createTopLevelRetryFixture(
	t *testing.T,
	store *harnesspostgres.Store,
	now time.Time,
) (harness.Turn, harness.Invocation) {
	t.Helper()
	turn := harness.Turn{
		ID: "turn_retry_root_pg", SessionID: "session_retry_root_pg",
		HostTurn:         harness.HostRef{Kind: "conversation_turn", ID: "retry-root"},
		ConfigSnapshotID: "config_retry_root_pg", Status: harness.TurnFailed, Revision: 1,
		ErrorCode: "fixture.failed", ErrorDetail: "first attempt failed", CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := store.CreateTurn(t.Context(), turn); err != nil || !fresh {
		t.Fatalf("create root retry turn fresh=%v err=%v", fresh, err)
	}
	input := json.RawMessage(`{"goal":"retry root"}`)
	hash := sha256.Sum256(input)
	invocation := harness.Invocation{
		ID: "hiv_retry_root_pg", TurnID: turn.ID, CapabilityKey: harness.CapabilityWorkflow,
		DefinitionVersion: harness.RuntimeCapabilityVersion, ExecutionClass: harness.ExecutionWorkflow,
		Input: input, InputHash: fmt.Sprintf("%x", hash[:]), ExecutionRefID: "run_retry_root_pg_1",
		Status: harness.InvocationFailed, Attempt: 1, OutputRefs: []harness.HostRef{}, Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := store.CreateInvocation(t.Context(), invocation); err != nil || !fresh {
		t.Fatalf("create root retry invocation fresh=%v err=%v", fresh, err)
	}
	return turn, invocation
}

func assertTopLevelInvocationRetried(t *testing.T, retried harness.Invocation, err error) {
	t.Helper()
	if err != nil || retried.Attempt != 2 || retried.Status != harness.InvocationAccepted {
		t.Fatalf("retry root invocation = %#v err=%v", retried, err)
	}
}

func assertTopLevelTurnReopened(t *testing.T, store *harnesspostgres.Store, turnID string) {
	t.Helper()
	reopened, err := store.GetTurn(t.Context(), turnID)
	if err != nil || reopened.Status != harness.TurnRunning || reopened.Revision != 2 ||
		reopened.ErrorCode != "" || reopened.ErrorDetail != "" {
		t.Fatalf("reopened root turn = %#v err=%v", reopened, err)
	}
}

func createRetryInvocationFixture(
	t *testing.T,
	store *harnesspostgres.Store,
	now time.Time,
) harness.Invocation {
	t.Helper()
	input := json.RawMessage(`{"goal":"retry"}`)
	hash := sha256.Sum256(input)
	value := harness.Invocation{
		ID: "hiv_retry_pg", TurnID: "turn_retry_pg", ParentItemID: "parent-item", CapabilityKey: harness.CapabilityTeam,
		DefinitionVersion: harness.RuntimeCapabilityVersion, ExecutionClass: harness.ExecutionTeam,
		Input: input, InputHash: fmt.Sprintf("%x", hash[:]), ExecutionRefID: "run_retry_pg_1",
		Status: harness.InvocationFailed, Attempt: 1, OutputRefs: []harness.HostRef{}, Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := store.CreateInvocation(t.Context(), value); err != nil || !fresh {
		t.Fatalf("create retry fixture fresh=%v err=%v", fresh, err)
	}
	return value
}

func assertRetriedInvocation(
	t *testing.T,
	store *harnesspostgres.Store,
	original harness.Invocation,
	retried harness.Invocation,
	err error,
) {
	t.Helper()
	if err != nil || retried.Attempt != 2 || retried.Status != harness.InvocationAccepted ||
		retried.ExecutionRefID != "run_retry_pg_2" || string(retried.Input) != string(original.Input) ||
		retried.InputHash != original.InputHash {
		t.Fatalf("retry invocation=%#v err=%v", retried, err)
	}
	byRef, loadErr := store.GetInvocationByExecutionRefID(t.Context(), retried.ExecutionRefID)
	if loadErr != nil || byRef.ID != original.ID {
		t.Fatalf("retry execution lookup=%#v err=%v", byRef, loadErr)
	}
}

func assertSessionLifecycle(t *testing.T, store *harnesspostgres.Store, session harness.Session) {
	t.Helper()
	created, fresh, err := store.CreateSession(t.Context(), session)
	if err != nil || !fresh || created.ID != session.ID {
		t.Fatalf("create session: %#v fresh=%v err=%v", created, fresh, err)
	}
	replayed, fresh, err := store.CreateSession(t.Context(), session)
	if err != nil || fresh || replayed.ID != session.ID {
		t.Fatalf("replay session: %#v fresh=%v err=%v", replayed, fresh, err)
	}
}

func assertConfigLifecycle(t *testing.T, store *harnesspostgres.Store, config harness.ConfigSnapshot) {
	t.Helper()
	created, fresh, err := store.PutConfigSnapshot(t.Context(), config)
	if err != nil || !fresh || created.ContentHash != config.ContentHash {
		t.Fatalf("put config: %#v fresh=%v err=%v", created, fresh, err)
	}
	loaded, err := store.GetConfigSnapshot(t.Context(), config.ID)
	if err != nil || loaded.ContentHash != config.ContentHash || loaded.Model != config.Model ||
		len(loaded.Commands) != len(config.Commands) || loaded.Commands[0].ID != config.Commands[0].ID {
		t.Fatalf("load config: %#v err=%v", loaded, err)
	}
}

func assertContextSnapshotLifecycle(t *testing.T, store *harnesspostgres.Store, snapshot runtimecontext.Snapshot) {
	t.Helper()
	created, fresh, err := store.PutContextSnapshot(t.Context(), snapshot)
	if err != nil || !fresh || created.ID != snapshot.ID {
		t.Fatalf("put context snapshot: %#v fresh=%v err=%v", created, fresh, err)
	}
	replayed, fresh, err := store.PutContextSnapshot(t.Context(), snapshot)
	if err != nil || fresh || replayed.ContentHash != snapshot.ContentHash {
		t.Fatalf("replay context snapshot: %#v fresh=%v err=%v", replayed, fresh, err)
	}
	loaded, err := store.GetContextSnapshot(t.Context(), snapshot.ID)
	if err != nil || loaded.RunID != snapshot.RunID || loaded.ContentHash != snapshot.ContentHash {
		t.Fatalf("load context snapshot: %#v err=%v", loaded, err)
	}
}

func assertTurnLifecycle(t *testing.T, store *harnesspostgres.Store, turn harness.Turn, now time.Time) harness.Turn {
	t.Helper()
	created, fresh, err := store.CreateTurn(t.Context(), turn)
	if err != nil || !fresh {
		t.Fatalf("create turn: %#v fresh=%v err=%v", created, fresh, err)
	}
	created.Status = harness.TurnRunning
	created.UpdatedAt = now.Add(time.Second)
	updated, err := store.UpdateTurn(t.Context(), created, 1)
	if err != nil || updated.Revision != 2 {
		t.Fatalf("update turn: %#v err=%v", updated, err)
	}
	loaded, err := store.GetTurn(t.Context(), turn.ID)
	if err != nil || loaded.Revision != 2 || loaded.Status != harness.TurnRunning ||
		loaded.ContextSnapshotID != turn.ContextSnapshotID || loaded.ContextRef != turn.ContextRef {
		t.Fatalf("load turn: %#v err=%v", loaded, err)
	}
	return updated
}

func assertInvocationLifecycle(t *testing.T, store *harnesspostgres.Store, turnID string, now time.Time) harness.Invocation {
	t.Helper()
	created := createInvocationFixture(t, store, turnID, now)
	assertInvocationLookupByExecutionRef(t, store, created)
	updated := updateInvocationFixture(t, store, created, now)
	assertInvocationList(t, store, turnID, updated)
	return updated
}

func createInvocationFixture(t *testing.T, store *harnesspostgres.Store, turnID string, now time.Time) harness.Invocation {
	t.Helper()
	value := harness.Invocation{
		ID: "hiv_pg", TurnID: turnID, CapabilityKey: "runtime.agent", DefinitionVersion: "v1",
		ExecutionClass: harness.ExecutionAgent, InputHash: strings.Repeat("a", 64), ExecutionRefID: testHarnessExecutionRefID,
		Status: harness.InvocationAccepted, Attempt: 1, OutputRefs: []harness.HostRef{}, Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	created, fresh, err := store.CreateInvocation(t.Context(), value)
	if err != nil || !fresh || created.ExecutionRefID != testHarnessExecutionRefID {
		t.Fatalf("create invocation: %#v fresh=%v err=%v", created, fresh, err)
	}
	return created
}

func assertInvocationLookupByExecutionRef(t *testing.T, store *harnesspostgres.Store, created harness.Invocation) {
	t.Helper()
	byExecution, err := store.GetInvocationByExecutionRefID(t.Context(), testHarnessExecutionRefID)
	if err != nil || byExecution.ID != created.ID {
		t.Fatalf("load invocation by execution ref: %#v err=%v", byExecution, err)
	}
}

func updateInvocationFixture(
	t *testing.T,
	store *harnesspostgres.Store,
	created harness.Invocation,
	now time.Time,
) harness.Invocation {
	t.Helper()
	created.Status = harness.InvocationRunning
	created.UpdatedAt = now.Add(time.Second)
	updated, err := store.UpdateInvocation(t.Context(), created, 1)
	if err != nil || updated.Revision != 2 || updated.Status != harness.InvocationRunning {
		t.Fatalf("update invocation: %#v err=%v", updated, err)
	}
	return updated
}

func assertInvocationList(t *testing.T, store *harnesspostgres.Store, turnID string, updated harness.Invocation) {
	t.Helper()
	listed, err := store.ListInvocations(t.Context(), turnID)
	if err != nil || len(listed) != 1 || listed[0].ID != updated.ID {
		t.Fatalf("list invocations: %#v err=%v", listed, err)
	}
}

func assertInteractionLifecycle(
	t *testing.T,
	store *harnesspostgres.Store,
	turnID, invocationID string,
	now time.Time,
) {
	t.Helper()
	created := createInteractionFixture(t, store, turnID, invocationID, now)
	assertInteractionLoad(t, store, invocationID, created)
	updated := updateInteractionFixture(t, store, created, now)
	assertInteractionList(t, store, turnID, updated)
}

func createInteractionFixture(
	t *testing.T,
	store *harnesspostgres.Store,
	turnID, invocationID string,
	now time.Time,
) harness.Interaction {
	t.Helper()
	value := harness.Interaction{
		ID: "hinteraction_pg", TurnID: turnID, InvocationID: invocationID, ParentItemID: "parent-item",
		Key: "candidate-choice", Kind: harness.InteractionChoice,
		Schema: json.RawMessage(`{"type":"object"}`), Presentation: json.RawMessage(`{"title":"Choose"}`),
		Status: harness.InteractionWaiting, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	turn, err := store.GetTurn(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := store.GetInvocation(t.Context(), invocationID)
	if err != nil {
		t.Fatal(err)
	}
	created, fresh, err := store.CreateInteraction(t.Context(), value, turn.Revision, invocation.Revision)
	if err != nil || !fresh || created.Status != harness.InteractionWaiting {
		t.Fatalf("create interaction: %#v fresh=%v err=%v", created, fresh, err)
	}
	replayed, fresh, err := store.CreateInteraction(t.Context(), value, turn.Revision, invocation.Revision)
	if err != nil || fresh || replayed.ID != created.ID {
		t.Fatalf("replay interaction: %#v fresh=%v err=%v", replayed, fresh, err)
	}
	conflict := value
	conflict.ID = "hinteraction_pg_conflict"
	conflict.Key = "other-choice"
	if _, _, err = store.CreateInteraction(t.Context(), conflict, turn.Revision, invocation.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("second waiting interaction error = %v", err)
	}
	listed, err := store.ListInteractions(t.Context(), turnID)
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("conflicting interaction left durable state: %#v err=%v", listed, err)
	}
	return created
}

func assertInteractionLoad(
	t *testing.T,
	store *harnesspostgres.Store,
	invocationID string,
	created harness.Interaction,
) {
	t.Helper()
	loaded, err := store.GetInteraction(t.Context(), created.ID)
	if err != nil || loaded.InvocationID != invocationID || string(loaded.Schema) != string(created.Schema) {
		t.Fatalf("load interaction: %#v err=%v", loaded, err)
	}
}

func updateInteractionFixture(
	t *testing.T,
	store *harnesspostgres.Store,
	created harness.Interaction,
	now time.Time,
) harness.Interaction {
	t.Helper()
	created.Status = harness.InteractionResolved
	created.Response = json.RawMessage(`{"candidateID":"candidate-2"}`)
	created.UpdatedAt = now.Add(2 * time.Second)
	updated, err := store.UpdateInteraction(t.Context(), created, 1)
	if err != nil || updated.Revision != 2 || updated.Status != harness.InteractionResolved {
		t.Fatalf("update interaction: %#v err=%v", updated, err)
	}
	return updated
}

func assertInteractionList(
	t *testing.T,
	store *harnesspostgres.Store,
	turnID string,
	updated harness.Interaction,
) {
	t.Helper()
	listed, err := store.ListInteractions(t.Context(), turnID)
	if err != nil || len(listed) != 1 || string(listed[0].Response) != string(updated.Response) {
		t.Fatalf("list interactions: %#v err=%v", listed, err)
	}
}

func assertItemLifecycle(t *testing.T, store *harnesspostgres.Store, turnID, invocationID string, now time.Time) {
	t.Helper()
	for _, id := range []string{"hi_pg_1", "hi_pg_2"} {
		item := harness.Item{
			ID: id, TurnID: turnID, Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
			InvocationID: invocationID,
			Payload:      json.RawMessage(`{"ok":true}`), CreatedAt: now, UpdatedAt: now,
		}
		created, fresh, err := store.AppendItem(t.Context(), item)
		if err != nil || !fresh || created.Seq == 0 {
			t.Fatalf("append item: %#v fresh=%v err=%v", created, fresh, err)
		}
	}
	items, err := store.ListItems(t.Context(), turnID, 0, 10)
	if err != nil || len(items) != 2 || items[0].Seq != 1 || items[1].Seq != 2 || items[0].InvocationID != invocationID {
		t.Fatalf("list items: %#v err=%v", items, err)
	}
}

func newStore(t *testing.T) *harnesspostgres.Store {
	t.Helper()
	dsn := fmt.Sprintf("file:harness-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err = harnesspostgres.Migrate(db); err != nil {
		t.Fatalf("migrate harness: %v", err)
	}
	store, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	return store
}

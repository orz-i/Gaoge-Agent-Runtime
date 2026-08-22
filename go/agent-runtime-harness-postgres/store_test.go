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
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
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
	contextCheckpoint := newContextCheckpoint(t, session.ID)
	assertContextCheckpointLifecycle(t, store, contextCheckpoint)
	turn := harness.Turn{
		ID: "ht_pg", SessionID: session.ID, HostTurn: harness.HostRef{Kind: "conversation_turn", ID: "turn_pg"},
		ConfigSnapshotID: config.ID, ContextCheckpointID: contextCheckpoint.ID,
		ContextRef: harness.ContextCheckpointRef{
			ID: contextCheckpoint.ID, Generation: contextCheckpoint.Generation, Revision: contextCheckpoint.Revision,
			LineageHash: contextCheckpoint.LineageHash, CoveredThroughSourceID: contextCheckpoint.CoveredThroughSourceID,
			ContentHash: contextCheckpoint.ContentHash,
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

func TestStoreContextCheckpointRequiresDurableArtifactsAndTracksLatest(t *testing.T) {
	store := newStore(t)
	base := newContextCheckpoint(t, "scope-artifacts")
	if _, fresh, err := store.PutContextCheckpoint(t.Context(), base); err != nil || !fresh {
		t.Fatalf("put base checkpoint fresh=%v err=%v", fresh, err)
	}
	artifact, err := runtimecontext.NewArtifact(
		runtimecontext.ArtifactCompaction, base.ScopeID, base.Generation+1, base.CoveredThroughSourceID,
		"compacted durable history", json.RawMessage(`{"strategy":"portable"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	rollover, err := manager.Rollover(t.Context(), runtimecontext.RolloverRequest{
		Previous: base, Window: base.Window, Artifacts: []runtimecontext.Artifact{artifact}, Reason: "soft_limit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutContextCheckpoint(t.Context(), rollover); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("checkpoint with missing artifact error = %v", err)
	}
	if _, fresh, err := store.PutContextArtifact(t.Context(), artifact); err != nil || !fresh {
		t.Fatalf("put context artifact fresh=%v err=%v", fresh, err)
	}
	if _, fresh, err := store.PutContextCheckpoint(t.Context(), rollover); err != nil || !fresh {
		t.Fatalf("put rollover fresh=%v err=%v", fresh, err)
	}
	latest, err := store.GetLatestContextCheckpoint(t.Context(), base.ScopeID)
	if err != nil || latest.ID != rollover.ID || latest.Generation != base.Generation+1 {
		t.Fatalf("latest checkpoint=%#v err=%v", latest, err)
	}
	loadedArtifact, err := store.GetContextArtifact(t.Context(), artifact.ID)
	if err != nil || loadedArtifact.ContentHash != artifact.ContentHash {
		t.Fatalf("loaded artifact=%#v err=%v", loadedArtifact, err)
	}
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

func TestStorePersistsInteractionResolutionBeforeOwnersResume(t *testing.T) {
	store := newStore(t)
	turn, invocation, interaction := createWaitingInteractionFixture(t, store, "resolve")
	interaction.Status = harness.InteractionResolved
	interaction.Response = json.RawMessage(`{"candidateID":"candidate-2"}`)
	interaction.UpdatedAt = interaction.UpdatedAt.Add(time.Second)

	resolution, err := store.ResolveInteraction(t.Context(), interaction, interaction.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Interaction.Status != harness.InteractionResolved || resolution.Interaction.Revision != 2 ||
		resolution.Invocation.Status != harness.InvocationWaitingInput || resolution.Invocation.Revision != invocation.Revision ||
		resolution.Turn.Status != harness.TurnWaitingInput || resolution.Turn.Revision != turn.Revision {
		t.Fatalf("interaction resolution resumed owners before continuation = %#v", resolution)
	}
}

func TestStoreRejectsInteractionResolutionAfterCancellation(t *testing.T) {
	store := newStore(t)
	turn, invocation, interaction := createWaitingInteractionFixture(t, store, "cancel")
	invocation.Status = harness.InvocationCancelled
	invocation.UpdatedAt = invocation.UpdatedAt.Add(time.Second)
	if _, err := store.UpdateInvocation(t.Context(), invocation, invocation.Revision); err != nil {
		t.Fatal(err)
	}
	turn.Status = harness.TurnCancelled
	turn.UpdatedAt = turn.UpdatedAt.Add(time.Second)
	if _, err := store.UpdateTurn(t.Context(), turn, turn.Revision); err != nil {
		t.Fatal(err)
	}
	interaction.Status = harness.InteractionResolved
	interaction.Response = json.RawMessage(`{"candidateID":"candidate-2"}`)
	interaction.UpdatedAt = interaction.UpdatedAt.Add(2 * time.Second)

	if _, err := store.ResolveInteraction(t.Context(), interaction, interaction.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("late interaction resolution error = %v", err)
	}
	persisted, err := store.GetInteraction(t.Context(), interaction.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != harness.InteractionWaiting || len(persisted.Response) != 0 || persisted.Revision != 1 {
		t.Fatalf("late resolution mutated interaction: %#v", persisted)
	}
}

func createWaitingInteractionFixture(
	t *testing.T,
	store *harnesspostgres.Store,
	suffix string,
) (harness.Turn, harness.Invocation, harness.Interaction) {
	t.Helper()
	now := time.Date(2026, 8, 20, 5, 0, 0, 0, time.UTC)
	turn := harness.Turn{
		ID: "turn_interaction_" + suffix, SessionID: "session_interaction_" + suffix,
		HostTurn:         harness.HostRef{Kind: "conversation_turn", ID: "host_" + suffix},
		ConfigSnapshotID: "config_interaction_" + suffix, Status: harness.TurnRunning,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := store.CreateTurn(t.Context(), turn); err != nil || !fresh {
		t.Fatalf("create interaction turn fresh=%v err=%v", fresh, err)
	}
	invocation := harness.Invocation{
		ID: "invocation_interaction_" + suffix, TurnID: turn.ID, CapabilityKey: "runtime.agent",
		DefinitionVersion: "v1", ExecutionClass: harness.ExecutionAgent,
		ExecutionRefID: "run_interaction_" + suffix, Status: harness.InvocationRunning,
		Attempt: 1, OutputRefs: []harness.HostRef{}, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := store.CreateInvocation(t.Context(), invocation); err != nil || !fresh {
		t.Fatalf("create interaction invocation fresh=%v err=%v", fresh, err)
	}
	interaction := harness.Interaction{
		ID: "interaction_" + suffix, TurnID: turn.ID, InvocationID: invocation.ID,
		Key: "candidate-choice", Kind: harness.InteractionChoice, Schema: json.RawMessage(`{"type":"object"}`),
		Status: harness.InteractionWaiting, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := store.CreateInteraction(t.Context(), interaction, turn.Revision, invocation.Revision); err != nil || !fresh {
		t.Fatalf("create interaction fresh=%v err=%v", fresh, err)
	}
	invocation.Status = harness.InvocationWaitingInput
	invocation.UpdatedAt = now.Add(time.Second)
	var err error
	invocation, err = store.UpdateInvocation(t.Context(), invocation, invocation.Revision)
	if err != nil {
		t.Fatal(err)
	}
	turn.Status = harness.TurnWaitingInput
	turn.UpdatedAt = now.Add(time.Second)
	turn, err = store.UpdateTurn(t.Context(), turn, turn.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return turn, invocation, interaction
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

func newContextCheckpoint(t *testing.T, scopeID string) runtimecontext.Checkpoint {
	t.Helper()
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	checkpoint, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
		ScopeID: scopeID, StaticFingerprint: runtimecontext.StaticFingerprint("sealed"),
		SourcePath: []string{"message-pg"}, Instructions: "sealed",
		Entries: []runtimecontext.Entry{{
			ID: "entry-pg-" + scopeID, SourceID: "message-pg", TurnID: "turn-pg",
			Message: model.Message{Role: model.RoleUser, Content: "postgres context"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return checkpoint
}

func assertContextCheckpointLifecycle(t *testing.T, store *harnesspostgres.Store, checkpoint runtimecontext.Checkpoint) {
	t.Helper()
	created, fresh, err := store.PutContextCheckpoint(t.Context(), checkpoint)
	if err != nil || !fresh || created.ID != checkpoint.ID {
		t.Fatalf("put context checkpoint: %#v fresh=%v err=%v", created, fresh, err)
	}
	replayed, fresh, err := store.PutContextCheckpoint(t.Context(), checkpoint)
	if err != nil || fresh || replayed.ContentHash != checkpoint.ContentHash {
		t.Fatalf("replay context checkpoint: %#v fresh=%v err=%v", replayed, fresh, err)
	}
	loaded, err := store.GetContextCheckpoint(t.Context(), checkpoint.ID)
	if err != nil || loaded.ScopeID != checkpoint.ScopeID || loaded.ContentHash != checkpoint.ContentHash {
		t.Fatalf("load context checkpoint: %#v err=%v", loaded, err)
	}
	latest, err := store.GetLatestContextCheckpoint(t.Context(), checkpoint.ScopeID)
	if err != nil || latest.ID != checkpoint.ID {
		t.Fatalf("latest context checkpoint: %#v err=%v", latest, err)
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
		loaded.ContextCheckpointID != turn.ContextCheckpointID || loaded.ContextRef != turn.ContextRef {
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
		ApplicationRef: &harness.HostRef{Kind: "story", ID: "story_pg"},
		ArtifactRefs:   []harness.HostRef{{Kind: "story_candidate_portfolio", ID: "portfolio_pg"}},
		Key:            "candidate-choice", Kind: harness.InteractionChoice,
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
	if err != nil || loaded.InvocationID != invocationID || string(loaded.Schema) != string(created.Schema) ||
		loaded.ApplicationRef == nil || *loaded.ApplicationRef != *created.ApplicationRef ||
		len(loaded.ArtifactRefs) != 1 || loaded.ArtifactRefs[0] != created.ArtifactRefs[0] {
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

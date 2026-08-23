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

const (
	testHarnessExecutionRefID = "hr_pg"
	testContextBaseSourceID   = "message-pg"
)

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

func TestStoreConcurrentContextOwnerCommitPersistsDetachedBranch(t *testing.T) {
	store := newStore(t)
	now := time.Date(2026, 8, 23, 2, 15, 0, 0, time.UTC)
	base := newContextCheckpoint(t, "scope-context-concurrent")
	rootTurn, fresh, err := store.CreateTurn(t.Context(), harness.Turn{
		ID: "turn-context-root", SessionID: base.ScopeID,
		HostTurn: harness.HostRef{Kind: "conversation_turn", ID: "context-root"}, ConfigSnapshotID: "config-context-root",
		Status: harness.TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil || !fresh {
		t.Fatalf("create root turn fresh=%v err=%v", fresh, err)
	}
	rootTurn, err = store.CommitContextCheckpoint(t.Context(), harness.ContextCheckpointCommit{
		TurnID: rootTurn.ID, ExpectedTurnRevision: rootTurn.Revision, Checkpoint: base, UpdatedAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	openBranch := func(turnID, sourceID, content string) runtimecontext.Checkpoint {
		t.Helper()
		checkpoint, openErr := manager.Open(t.Context(), runtimecontext.OpenRequest{
			ScopeID: base.ScopeID, StaticFingerprint: base.StaticFingerprint, Instructions: base.Window.Instructions,
			SourcePath: []string{sourceID}, Entries: []runtimecontext.Entry{{
				ID: "entry-" + sourceID, SourceID: sourceID, TurnID: turnID,
				Message: model.Message{Role: model.RoleUser, Content: content},
			}}, SourceDelta: true, Previous: &base,
		})
		if openErr != nil {
			t.Fatal(openErr)
		}
		return checkpoint
	}
	branchA := openBranch("turn-context-a", "message-context-a", "branch a")
	branchB := openBranch("turn-context-b", "message-context-b", "branch b")
	turnA, fresh, err := store.CreateTurn(t.Context(), harness.Turn{
		ID: "turn-context-a", SessionID: base.ScopeID,
		HostTurn: harness.HostRef{Kind: "conversation_turn", ID: "context-a"}, ConfigSnapshotID: "config-context-a",
		Status: harness.TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil || !fresh {
		t.Fatalf("create branch A turn fresh=%v err=%v", fresh, err)
	}
	turnB, fresh, err := store.CreateTurn(t.Context(), harness.Turn{
		ID: "turn-context-b", SessionID: base.ScopeID,
		HostTurn: harness.HostRef{Kind: "conversation_turn", ID: "context-b"}, ConfigSnapshotID: "config-context-b",
		Status: harness.TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil || !fresh {
		t.Fatalf("create branch B turn fresh=%v err=%v", fresh, err)
	}
	turnA, err = store.CommitContextCheckpoint(t.Context(), harness.ContextCheckpointCommit{
		TurnID: turnA.ID, ExpectedTurnRevision: turnA.Revision, ExpectedHeadCheckpointID: base.ID,
		Checkpoint: branchA, UpdatedAt: now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("advance branch A: %v", err)
	}
	turnB, err = store.CommitContextCheckpoint(t.Context(), harness.ContextCheckpointCommit{
		TurnID: turnB.ID, ExpectedTurnRevision: turnB.Revision, ExpectedHeadCheckpointID: base.ID,
		Checkpoint: branchB, UpdatedAt: now.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatalf("detached branch B commit: %v", err)
	}
	if turnB.ContextCheckpointID != branchB.ID {
		t.Fatalf("detached turn did not advance: %#v", turnB)
	}
	requireActiveContextCheckpoint(t, store, base.ScopeID, branchA.ID, branchA.Generation)
	if _, err = store.GetContextCheckpoint(t.Context(), branchB.ID); err != nil {
		t.Fatalf("detached branch checkpoint was not persisted: %v", err)
	}
	_ = turnA
}

func requirePutContextCheckpoint(t *testing.T, store *harnesspostgres.Store, checkpoint runtimecontext.Checkpoint) {
	t.Helper()
	created, fresh, err := store.PutContextCheckpoint(t.Context(), checkpoint)
	if err != nil || !fresh || created.ID != checkpoint.ID {
		t.Fatalf("put context checkpoint=%#v fresh=%v err=%v", created, fresh, err)
	}
}

func createContextCommitTurn(
	t *testing.T,
	store *harnesspostgres.Store,
	scopeID string,
	suffix string,
	now time.Time,
) harness.Turn {
	t.Helper()
	turn, fresh, err := store.CreateTurn(t.Context(), harness.Turn{
		ID: "turn-" + suffix, SessionID: scopeID,
		HostTurn: harness.HostRef{Kind: "conversation_turn", ID: suffix}, ConfigSnapshotID: "config-" + suffix,
		Status: harness.TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil || !fresh {
		t.Fatalf("create Context commit turn fresh=%v err=%v", fresh, err)
	}
	return turn
}

func requireCommitContextCheckpoint(
	t *testing.T,
	store *harnesspostgres.Store,
	turn harness.Turn,
	expectedTurnCheckpointID string,
	expectedHeadCheckpointID string,
	checkpoint runtimecontext.Checkpoint,
	updatedAt time.Time,
) harness.Turn {
	t.Helper()
	updated, err := store.CommitContextCheckpoint(t.Context(), harness.ContextCheckpointCommit{
		TurnID: turn.ID, ExpectedTurnRevision: turn.Revision,
		ExpectedTurnCheckpointID: expectedTurnCheckpointID, ExpectedHeadCheckpointID: expectedHeadCheckpointID,
		Checkpoint: checkpoint, UpdatedAt: updatedAt,
	})
	if err != nil || updated.ContextCheckpointID != checkpoint.ID {
		t.Fatalf("commit Context checkpoint turn=%#v err=%v", updated, err)
	}
	return updated
}

func requireActiveContextCheckpoint(
	t *testing.T,
	store *harnesspostgres.Store,
	scopeID string,
	wantID string,
	wantGeneration int,
) {
	t.Helper()
	active, err := store.GetActiveContextCheckpoint(t.Context(), scopeID)
	if err != nil || active.ID != wantID || active.Generation != wantGeneration {
		t.Fatalf("active checkpoint=%#v want=%s/%d err=%v", active, wantID, wantGeneration, err)
	}
}

func TestStoreContextCheckpointRequiresDurableArtifactsAndCommitsActiveHead(t *testing.T) {
	store := newStore(t)
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	base := newContextCheckpoint(t, "scope-artifacts")
	requirePutContextCheckpoint(t, store, base)
	if _, err := store.GetActiveContextCheckpoint(t.Context(), base.ScopeID); !errors.Is(err, harness.ErrNotFound) {
		t.Fatalf("immutable PutContextCheckpoint advanced active head: %v", err)
	}
	turn := createContextCommitTurn(t, store, base.ScopeID, "context-head", now)
	turn = requireCommitContextCheckpoint(t, store, turn, "", "", base, now.Add(time.Second))
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
	requirePutContextCheckpoint(t, store, rollover)
	requireActiveContextCheckpoint(t, store, base.ScopeID, base.ID, base.Generation)
	requireCommitContextCheckpoint(t, store, turn, base.ID, base.ID, rollover, now.Add(2*time.Second))
	requireActiveContextCheckpoint(t, store, base.ScopeID, rollover.ID, base.Generation+1)
	loadedArtifact, err := store.GetContextArtifact(t.Context(), artifact.ID)
	if err != nil || loadedArtifact.ContentHash != artifact.ContentHash {
		t.Fatalf("loaded artifact=%#v err=%v", loadedArtifact, err)
	}
}

func TestStoreFailedContextCommitDoesNotAdvanceHeadOrPersistCheckpoint(t *testing.T) {
	store := newStore(t)
	now := time.Date(2026, 8, 22, 9, 10, 0, 0, time.UTC)
	base := newContextCheckpoint(t, "scope-context-cas")
	turn, fresh, err := store.CreateTurn(t.Context(), harness.Turn{
		ID: "turn-context-cas", SessionID: base.ScopeID,
		HostTurn: harness.HostRef{Kind: "conversation_turn", ID: "context-cas"}, ConfigSnapshotID: "config-context-cas",
		Status: harness.TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil || !fresh {
		t.Fatalf("create turn fresh=%v err=%v", fresh, err)
	}
	turn, err = store.CommitContextCheckpoint(t.Context(), harness.ContextCheckpointCommit{
		TurnID: turn.ID, ExpectedTurnRevision: turn.Revision, Checkpoint: base, UpdatedAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("commit base: %v", err)
	}
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	entries := runtimecontext.CloneEntries(base.Window.Entries)
	entries = append(entries, runtimecontext.Entry{
		ID: "entry-context-cas-next", SourceID: "message-cas-next", TurnID: "turn-cas-next",
		Message: model.Message{Role: model.RoleAssistant, Content: "next context"},
	})
	candidate, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
		ScopeID: base.ScopeID, StaticFingerprint: base.StaticFingerprint,
		SourcePath: []string{testContextBaseSourceID, "message-cas-next"}, Instructions: base.Window.Instructions,
		Entries: entries, Previous: &base,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CommitContextCheckpoint(t.Context(), harness.ContextCheckpointCommit{
		TurnID: turn.ID, ExpectedTurnRevision: turn.Revision + 1,
		ExpectedTurnCheckpointID: base.ID, ExpectedHeadCheckpointID: base.ID,
		Checkpoint: candidate, UpdatedAt: now.Add(2 * time.Second),
	})
	if !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("stale Context commit error=%v", err)
	}
	active, err := store.GetActiveContextCheckpoint(t.Context(), base.ScopeID)
	if err != nil || active.ID != base.ID {
		t.Fatalf("failed commit polluted active head=%#v err=%v", active, err)
	}
	if _, err = store.GetContextCheckpoint(t.Context(), candidate.ID); !errors.Is(err, harness.ErrNotFound) {
		t.Fatalf("failed commit left speculative checkpoint durable: %v", err)
	}
}

func TestStoreFindContextCheckpointForPathChoosesNearestSourceAlignedAncestor(t *testing.T) {
	store := newStore(t)
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	base := newContextCheckpoint(t, "scope-path-reuse")
	requirePutContextCheckpoint(t, store, base)

	branchA1, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
		ScopeID: base.ScopeID, StaticFingerprint: base.StaticFingerprint, Instructions: base.Window.Instructions,
		SourcePath: []string{"a1"}, Entries: []runtimecontext.Entry{{
			ID: "entry-a1", SourceID: "a1", TurnID: "turn-a1", Message: model.Message{Role: model.RoleAssistant, Content: "a1"},
		}}, SourceDelta: true, Previous: &base,
	})
	if err != nil {
		t.Fatal(err)
	}
	requirePutContextCheckpoint(t, store, branchA1)
	branchA2, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
		ScopeID: base.ScopeID, StaticFingerprint: base.StaticFingerprint, Instructions: base.Window.Instructions,
		SourcePath: []string{"a2"}, Entries: []runtimecontext.Entry{{
			ID: "entry-a2", SourceID: "a2", TurnID: "turn-a2", Message: model.Message{Role: model.RoleUser, Content: "a2"},
		}}, SourceDelta: true, Previous: &branchA1,
	})
	if err != nil {
		t.Fatal(err)
	}
	requirePutContextCheckpoint(t, store, branchA2)

	runtimeTail, err := manager.Capture(t.Context(), runtimecontext.CaptureRequest{
		Previous: branchA2, StaticFingerprint: branchA2.StaticFingerprint, RunID: "run-a2",
		Messages: append(runtimecontext.Materialize(branchA2.Window), model.Message{Role: model.RoleAssistant, Content: "runtime tail"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	requirePutContextCheckpoint(t, store, runtimeTail)

	found, err := store.FindContextCheckpointForPath(t.Context(), harness.ContextCheckpointPathQuery{
		ScopeID: base.ScopeID, StaticFingerprint: base.StaticFingerprint,
		SourcePath: []string{testContextBaseSourceID, "a1", "a2", "a3"},
	})
	if err != nil || found.ID != branchA2.ID {
		t.Fatalf("nearest source-aligned checkpoint=%#v want=%s err=%v", found, branchA2.ID, err)
	}
}

func TestStoreFindContextCheckpointForPathDoesNotScaleSQLArgumentsWithAncestry(t *testing.T) {
	store := newStore(t)
	const ancestrySize = 40_000
	path := make([]string, ancestrySize)
	for index := range path {
		path[index] = fmt.Sprintf("message-%05d", index)
	}
	_, err := store.FindContextCheckpointForPath(t.Context(), harness.ContextCheckpointPathQuery{
		ScopeID: "scope-large-path-query", StaticFingerprint: runtimecontext.StaticFingerprint("large-path"), SourcePath: path,
	})
	if !errors.Is(err, harness.ErrNotFound) {
		t.Fatalf("large ancestry lookup should execute without an ancestor-sized IN clause, got %v", err)
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
		SourcePath: []string{testContextBaseSourceID}, Instructions: "sealed",
		Entries: []runtimecontext.Entry{{
			ID: "entry-pg-" + scopeID, SourceID: testContextBaseSourceID, TurnID: "turn-pg",
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
	if active, err := store.GetActiveContextCheckpoint(t.Context(), checkpoint.ScopeID); !errors.Is(err, harness.ErrNotFound) || active.ID != "" {
		t.Fatalf("immutable checkpoint became active without commit: %#v err=%v", active, err)
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

package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge/sdk/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
)

const (
	testTenant      = "tenant"
	testActor       = "actor"
	testThreadKind  = "conversation"
	testThreadID    = "conversation_1"
	testTurnID      = "conversation_turn_1"
	testEnvironment = "general"
)

func TestMinimalModelOnlyHarnessCompletesDirectAgentTurn(t *testing.T) {
	t.Parallel()
	runner := newHarnessRunner(t)
	snapshot, err := runner.Start(t.Context(), testStartRequest())
	assertCompletedHarnessSnapshot(t, snapshot, err)
	replayed, err := runner.Start(t.Context(), testStartRequest())
	assertHarnessReplay(t, snapshot, replayed, err)
}

func assertCompletedHarnessSnapshot(t *testing.T, snapshot harness.Snapshot, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("start harness turn: %v", err)
	}
	if snapshot.Turn.Status != harness.TurnCompleted || snapshot.Output == nil {
		t.Fatalf("unexpected harness snapshot: %#v", snapshot)
	}
	var output string
	if decodeErr := json.Unmarshal(snapshot.Output.Content, &output); decodeErr != nil || output != "direct answer" {
		t.Fatalf("unexpected harness output: %#v err=%v", snapshot.Output, decodeErr)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].Kind != harness.ItemAgentRun || snapshot.Items[0].Status != harness.ItemCompleted {
		t.Fatalf("unexpected durable items: %#v", snapshot.Items)
	}
}

func assertHarnessReplay(t *testing.T, first, replayed harness.Snapshot, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("replay harness turn: %v", err)
	}
	if replayed.Turn.ID != first.Turn.ID || len(replayed.Items) != 1 {
		t.Fatalf("replay changed durable identity: %#v", replayed)
	}
}

func assertCreatedSession(t *testing.T, store *harness.MemoryStore, session harness.Session) {
	t.Helper()
	_, created, err := store.CreateSession(t.Context(), session)
	if err != nil || !created {
		t.Fatalf("create session: created=%v err=%v", created, err)
	}
}

func assertCreatedConfig(t *testing.T, store *harness.MemoryStore, config harness.ConfigSnapshot) {
	t.Helper()
	_, created, err := store.PutConfigSnapshot(t.Context(), config)
	if err != nil || !created {
		t.Fatalf("put config: created=%v err=%v", created, err)
	}
}

func assertTurnCAS(t *testing.T, store *harness.MemoryStore, turn harness.Turn, now time.Time) harness.Turn {
	t.Helper()
	createdTurn, created, err := store.CreateTurn(t.Context(), turn)
	if err != nil || !created {
		t.Fatalf("create turn: created=%v err=%v", created, err)
	}
	createdTurn.Status = harness.TurnRunning
	createdTurn.UpdatedAt = now.Add(time.Second)
	updated, err := store.UpdateTurn(t.Context(), createdTurn, 1)
	if err != nil || updated.Revision != 2 {
		t.Fatalf("update turn: %#v err=%v", updated, err)
	}
	return updated
}

func assertStaleTurnCAS(t *testing.T, store *harness.MemoryStore, updated harness.Turn) {
	t.Helper()
	if _, err := store.UpdateTurn(t.Context(), updated, 1); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("stale CAS error = %v", err)
	}
}

func assertAppendedItem(t *testing.T, store *harness.MemoryStore, item harness.Item) {
	t.Helper()
	_, created, err := store.AppendItem(t.Context(), item)
	if err != nil || !created {
		t.Fatalf("append item %s: created=%v err=%v", item.ID, created, err)
	}
}

func TestMemoryStoreUsesCASAndMonotonicItemSequence(t *testing.T) {
	t.Parallel()
	store := harness.NewMemoryStore()
	now := time.Date(2026, 8, 15, 2, 0, 0, 0, time.UTC)
	session := harness.Session{
		ID: "hs_test", HostThread: harness.HostRef{Kind: testThreadKind, ID: testThreadID},
		Actor: kernel.ActorRef{TenantID: testTenant, ActorID: testActor}, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	assertCreatedSession(t, store, session)
	config, err := harness.SealConfigSnapshot("ht_test", harness.ConfigSnapshot{
		Environment: harness.VersionRef{ID: testEnvironment, Revision: 1},
	}, now)
	if err != nil {
		t.Fatalf("seal config: %v", err)
	}
	assertCreatedConfig(t, store, config)
	turn := harness.Turn{
		ID: "ht_test", SessionID: session.ID, HostTurn: harness.HostRef{Kind: "conversation_turn", ID: testTurnID},
		ConfigSnapshotID: config.ID, Status: harness.TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	updated := assertTurnCAS(t, store, turn, now)
	assertStaleTurnCAS(t, store, updated)
	for _, id := range []string{"item_1", "item_2"} {
		assertAppendedItem(t, store, harness.Item{
			ID: id, TurnID: turn.ID, Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
			Payload: json.RawMessage(`{"ok":true}`), CreatedAt: now, UpdatedAt: now,
		})
	}
	items, err := store.ListItems(t.Context(), turn.ID, 0, 10)
	if err != nil || len(items) != 2 || items[0].Seq != 1 || items[1].Seq != 2 {
		t.Fatalf("unexpected item sequence: %#v err=%v", items, err)
	}
}

func TestConfigSnapshotIsDeterministicAndIsolated(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 15, 2, 0, 0, 0, time.UTC)
	input := harness.ConfigSnapshot{
		Environment:  harness.VersionRef{ID: "general", Revision: 3},
		ModelOptions: json.RawMessage(`{ "temperature": 0, "max_output_tokens": 512 }`),
		ToolKeys:     []string{"lookup", "lookup", "artifact"},
		Skills:       []harness.VersionRef{{ID: "writing", Revision: 2}, {ID: "analysis", Revision: 1}},
	}
	first, err := harness.SealConfigSnapshot("ht_config", input, now)
	if err != nil {
		t.Fatalf("seal config: %v", err)
	}
	second, err := harness.SealConfigSnapshot("ht_config", input, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("reseal config: %v", err)
	}
	if first.ID != second.ID || first.ContentHash != second.ContentHash || len(first.ToolKeys) != 2 || first.Skills[0].ID != "analysis" {
		t.Fatalf("config is not deterministic: first=%#v second=%#v", first, second)
	}
}

func newHarnessRunner(t *testing.T) *harness.Runner {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatalf("create kernel: %v", err)
	}
	agentRunner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: directModel{}})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: agentRunner, Store: harness.NewMemoryStore(), Clock: fixedClock{},
	})
	if err != nil {
		t.Fatalf("create harness: %v", err)
	}
	return runner
}

func testStartRequest() harness.StartRequest {
	return harness.StartRequest{
		HostThread: harness.HostRef{Kind: testThreadKind, ID: testThreadID},
		HostTurn:   harness.HostRef{Kind: "conversation_turn", ID: testTurnID},
		Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
		Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: testThreadID},
		Goal:       "answer directly",
		Config: harness.ConfigSnapshot{
			Environment: harness.VersionRef{ID: testEnvironment, Revision: 1}, Model: "model",
		},
	}
}

type directModel struct{}

func (directModel) Generate(_ context.Context, _ model.Request) (model.Response, error) {
	return model.Response{Content: "direct answer"}, nil
}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 8, 15, 2, 0, 0, 0, time.UTC) }

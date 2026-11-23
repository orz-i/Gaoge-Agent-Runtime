package harnesspostgres_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	kernelmemory "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"gorm.io/gorm"
)

// Minimal native Agent fixture for SQL physical-grant corruption tests.
// The retired Application interpreter is not required for these predicates.
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
	parentID, err := harness.AgentInvocationID(tid, "", "parent")
	if err != nil {
		t.Fatal(err)
	}
	parent := harness.Invocation{
		ID: parentID, TurnID: tid, Input: input, InputHash: fmt.Sprintf("%x", sum), ExecutionRefID: parentRunID,
		Status: harness.InvocationRunning, Attempt: 1, OutputRefs: []harness.HostRef{},
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err = store.CreateInvocation(t.Context(), parent); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.AppendItem(t.Context(), harness.Item{
		ID: "parent-invocation-item", TurnID: tid, Kind: harness.ItemAgentInvocation,
		Status: harness.ItemStarted, RunID: parentRunID, InvocationID: parent.ID,
		Payload:   json.RawMessage("{}"),
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return tid, "parent-invocation-item"
}

func nativeSQLRootID(t *testing.T, store harness.Store, turnID string) string {
	t.Helper()
	id, err := harness.AgentInvocationID(turnID, "", "parent")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

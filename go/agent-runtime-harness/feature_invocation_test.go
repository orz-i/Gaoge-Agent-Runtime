package harness_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

func newFeatureInvocationRuntime(t *testing.T) *kernel.Runtime {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func seedFeatureInvocationEnvelope(t *testing.T, store *harness.MemoryStore, sessionID, turnID string, hostThread, hostTurn harness.HostRef, actor kernel.ActorRef, now time.Time) {
	t.Helper()
	if _, _, err := store.CreateSession(t.Context(), harness.Session{ID: sessionID, HostThread: hostThread, Actor: actor, Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	config, err := harness.SealConfigSnapshot(turnID, harness.ConfigSnapshot{AdmissionHash: strings.Repeat("a", 64)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutConfigSnapshot(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CreateTurn(t.Context(), harness.Turn{ID: turnID, SessionID: sessionID, HostTurn: hostTurn, ConfigSnapshotID: config.ID, Status: harness.TurnRunning, Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
}

func featureParentInvocationID(turnID string) string {
	id, err := harness.InvocationID(turnID, "", harness.CapabilityAgent, "parent")
	if err != nil {
		panic(err)
	}
	return id
}
func seedFeatureInvocationParent(t *testing.T, runtime *kernel.Runtime, store *harness.MemoryStore, turnID string, actor kernel.ActorRef, thread kernel.ThreadRef, now time.Time) (string, string) {
	t.Helper()
	parentRunID := "parent-agent-run"
	if _, err := runtime.Create(t.Context(), kernel.CreateRequest{ID: parentRunID, Kind: agent.RunKind, Actor: actor, Thread: thread, RequestID: "parent", Goal: "coordinate", State: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	parentInput, err := json.Marshal(struct {
		Goal   string           `json:"goal"`
		Actor  kernel.ActorRef  `json:"actor"`
		Thread kernel.ThreadRef `json:"thread"`
	}{Goal: "coordinate", Actor: actor, Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	parentHash := sha256.Sum256(parentInput)
	parent := harness.Invocation{ID: featureParentInvocationID(turnID), TurnID: turnID, CapabilityKey: "runtime.agent", DefinitionVersion: "v1", Input: parentInput, InputHash: fmt.Sprintf("%x", parentHash), ExecutionRefID: parentRunID, Status: harness.InvocationRunning, Attempt: 1, OutputRefs: []harness.HostRef{}, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, _, err := store.CreateInvocation(t.Context(), parent); err != nil {
		t.Fatal(err)
	}
	parentItemID := "parent-invocation-item"
	if _, _, err := store.AppendItem(t.Context(), harness.Item{ID: parentItemID, TurnID: turnID, Kind: harness.ItemInvocation, Status: harness.ItemStarted, RunID: parentRunID, InvocationID: parent.ID, Payload: json.RawMessage(`{"capabilityKey":"runtime.agent"}`), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return parentRunID, parentItemID
}

type unusedFeatureAgent struct{}

func (unusedFeatureAgent) StartRun(context.Context, agent.StartRequest) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, harness.ErrInvalidRequest
}
func (unusedFeatureAgent) Resume(context.Context, string, uint64) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, harness.ErrInvalidRequest
}
func (unusedFeatureAgent) ResolveApproval(context.Context, string, uint64, plugin.ApprovalResponse) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, harness.ErrInvalidRequest
}

type loadingFeatureAgent struct {
	unusedFeatureAgent
	runtime *kernel.Runtime
}

func (feature loadingFeatureAgent) Resume(ctx context.Context, runID string, _ uint64) (kernel.Snapshot, error) {
	return feature.runtime.Load(ctx, runID)
}

type featureInvocationClock struct{}

func (featureInvocationClock) Now() time.Time {
	return time.Date(2026, time.August, 19, 4, 0, 0, 0, time.UTC)
}

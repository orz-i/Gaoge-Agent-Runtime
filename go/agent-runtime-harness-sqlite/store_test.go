package harnesssqlite_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestDispatchAndHandoffConformance(t *testing.T) {
	t.Run("original-return", func(t *testing.T) {
		_, host := newHost(t)
		conformance.DispatchSettlement(t, host, "durable-sqlite")
	})
	for name, scenario := range map[string]conformance.RemoteCancellationCase{
		"child":           {MemberRevision: true},
		"remote-shadow":   {Shadow: true, CancelRoot: true},
		"local-collision": {LocalCollision: true},
	} {
		t.Run(name, func(t *testing.T) {
			path, host := newHost(t)
			conformance.RemoteHandoffCancellation(t, host, scenario, func() harness.Store {
				if err := host.Close(); err != nil {
					t.Fatal(err)
				}
				host = reopenHost(t, path)
				return host
			})
		})
	}
}

func TestContextCheckpointAndArtifactsReopenAtomically(t *testing.T) {
	path, host := newHost(t)
	ctx := t.Context()
	now := time.Now().UTC()
	turn := harness.Turn{ID: "turn", SessionID: "session", HostTurn: request().HostTurn, ConfigSnapshotID: "config", Status: harness.TurnRunning, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, _, err := host.CreateTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	base, err := manager.Open(ctx, runtimecontext.OpenRequest{ScopeID: turn.SessionID, StaticFingerprint: runtimecontext.StaticFingerprint("sealed"), SourcePath: []string{"message"}, Instructions: "sealed", Entries: []runtimecontext.Entry{{ID: "entry", SourceID: "message", TurnID: turn.ID, Message: model.Message{Role: model.RoleUser, Content: model.TextParts("initial")}}}})
	if err != nil {
		t.Fatal(err)
	}
	commit := harness.ContextCheckpointCommit{TurnID: turn.ID, ExpectedTurnRevision: 1, Checkpoint: base, UpdatedAt: now}
	saved, err := host.CommitContextCheckpoint(ctx, commit)
	if err != nil || saved.ContextCheckpointID != base.ID {
		t.Fatalf("commit base: %+v %v", saved, err)
	}
	artifact, err := runtimecontext.NewArtifact(runtimecontext.ArtifactCompaction, base.ScopeID, base.Generation+1, base.CoveredThroughSourceID, "original context", json.RawMessage(`{"source":"user"}`))
	if err != nil {
		t.Fatal(err)
	}
	next, err := manager.Rollover(ctx, runtimecontext.RolloverRequest{Previous: base, Window: base.Window, Artifacts: []runtimecontext.Artifact{artifact}, Reason: "soft_limit"})
	if err != nil {
		t.Fatal(err)
	}
	commit = harness.ContextCheckpointCommit{TurnID: turn.ID, ExpectedTurnRevision: saved.Revision, ExpectedTurnCheckpointID: base.ID, ExpectedHeadCheckpointID: base.ID, Checkpoint: next, UpdatedAt: now}
	if _, err = host.CommitContextCheckpoint(ctx, commit); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("committed missing artifact: %v", err)
	}
	if _, err = host.GetContextCheckpoint(ctx, next.ID); !errors.Is(err, harness.ErrNotFound) {
		t.Fatalf("partial checkpoint: %v", err)
	}
	if _, fresh, err := host.PutContextArtifact(ctx, artifact); err != nil || !fresh {
		t.Fatalf("persist artifact: %t %v", fresh, err)
	}
	updated, err := host.CommitContextCheckpoint(ctx, commit)
	if err != nil || updated.Revision != saved.Revision+1 {
		t.Fatalf("advance checkpoint: %+v %v", updated, err)
	}
	if err = host.Close(); err != nil {
		t.Fatal(err)
	}
	host = reopenHost(t, path)
	active, err := host.GetActiveContextCheckpoint(ctx, base.ScopeID)
	if err != nil || active.ID != next.ID || active.ContentHash != next.ContentHash {
		t.Fatalf("active checkpoint: %+v %v", active, err)
	}
	reloaded, err := host.GetContextArtifact(ctx, artifact.ID)
	if err != nil || reloaded.ID != artifact.ID || string(reloaded.ContentJSON) != string(artifact.ContentJSON) {
		t.Fatalf("artifact after reopen: %+v %v", reloaded, err)
	}
	owner, err := host.GetTurn(ctx, turn.ID)
	if err != nil || owner.ContextRef != updated.ContextRef || owner.Revision != updated.Revision {
		t.Fatalf("turn checkpoint: %+v %v", owner, err)
	}
}

func TestBudgetReplaysOriginalSettlementAfterReopen(t *testing.T) {
	path, host := newHost(t)
	ctx := t.Context()
	c := budget.Coordinator{Store: host}
	if _, err := c.Ensure(ctx, "turn", budget.Limits{MaxLLMCalls: 1, MaxTotalTokens: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RegisterRun(ctx, "turn", "run", budget.RunBudget{}); err != nil {
		t.Fatal(err)
	}
	usage := budget.Usage{LLMCalls: 1, InputTokens: 3, OutputTokens: 2, TotalTokens: 5}
	r := budget.Reservation{ID: "charge", RunID: "run", RequestHash: "original", Requested: usage}
	if _, err := c.Reserve(ctx, "turn", r, true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Dispatch(ctx, "turn", r.ID); err != nil {
		t.Fatal(err)
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	host = reopenHost(t, path)
	c.Store = host
	if _, err := c.Release(ctx, "turn", r.ID); !errors.Is(err, budget.ErrLedgerConflict) {
		t.Fatalf("released dispatched call: %v", err)
	}
	receipt := json.RawMessage(`{"call":"original"}`)
	if _, err := c.Settle(ctx, "turn", r.ID, usage, receipt); err != nil {
		t.Fatal(err)
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	host = reopenHost(t, path)
	c.Store = host
	if _, err := c.Settle(ctx, "turn", r.ID, usage, receipt); err != nil {
		t.Fatalf("identical receipt: %v", err)
	}
	if _, err := c.Settle(ctx, "turn", r.ID, usage, json.RawMessage(`{"call":"other"}`)); !errors.Is(err, budget.ErrLedgerConflict) {
		t.Fatalf("changed receipt: %v", err)
	}
	ledger, err := host.LoadBudget(ctx, "turn")
	settled := ledger.Reservations[r.ID]
	if err != nil || len(ledger.Reservations) != 1 || settled.Status != budget.ReservationSettled || settled.Usage != usage || string(settled.Receipt) != string(receipt) {
		t.Fatalf("durable settlement: %+v %v", ledger, err)
	}
}

func TestInvocationOrderUsesInstantsAcrossTimeZones(t *testing.T) {
	path, host := newHost(t)
	base := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	east := time.FixedZone("east", 8*3600)
	west := time.FixedZone("west", -8*3600)
	for _, stamp := range []struct {
		id string
		at time.Time
	}{
		{"later", base.Add(time.Nanosecond).In(west)}, {"tie-b", base.In(east)}, {"tie-a", base.In(west)},
	} {
		input := json.RawMessage(`{"goal":"ordered child"}`)
		digest := sha256.Sum256(input)
		inv := harness.Invocation{ID: stamp.id, TurnID: "ordered", ParentItemID: "parent",
			Input: input, InputHash: hex.EncodeToString(digest[:]),
			ExecutionRefID: "run-" + stamp.id, Status: harness.InvocationAccepted,
			Attempt: 1, Revision: 1, CreatedAt: stamp.at, UpdatedAt: stamp.at}
		if _, _, err := host.CreateInvocation(t.Context(), inv); err != nil {
			t.Fatal(err)
		}
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	host = reopenHost(t, path)
	items, err := host.ListInvocations(t.Context(), "ordered")
	if err != nil || len(items) != 3 {
		t.Fatalf("ordered invocations: %+v %v", items, err)
	}
	for index, want := range []string{"tie-a", "tie-b", "later"} {
		if items[index].ID != want {
			t.Fatalf("position %d got %s, want %s", index, items[index].ID, want)
		}
	}
}

package harnesspostgres_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

func TestSQLRetryStoreRejectsCorruptFrozenBudget(t *testing.T) {
	testSQLRetryStoreConfigIntegrity(t, false)
}
func TestSQLRetryStoreRejectsMissingFrozenConfig(t *testing.T) {
	testSQLRetryStoreConfigIntegrity(t, true)
}
func testSQLRetryStoreConfigIntegrity(t *testing.T, missing bool) {
	t.Helper()
	s, db := newStoreWithDB(t)
	ctx := t.Context()
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	config, err := harness.SealConfigSnapshot("turn-budget-authority", harness.ConfigSnapshot{
		SharedBudget: &budget.Limits{MaxLLMCalls: 1, MaxTotalTokens: 100}, AgentSpec: harness.AgentSpec{Model: "specialist"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.PutConfigSnapshot(ctx, config); err != nil {
		t.Fatal(err)
	}
	turn := harness.Turn{ID: config.TurnID, SessionID: "session", HostTurn: harness.HostRef{Kind: "conversation_turn", ID: "turn"},
		ConfigSnapshotID: config.ID, Status: harness.TurnCancelled, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, _, err = s.CreateTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	input := []byte("{\"goal\":\"cancelled run\"}")
	digest := sha256.Sum256(input)
	root := harness.Invocation{ID: "root", TurnID: turn.ID, Input: input, InputHash: hex.EncodeToString(digest[:]),
		ExecutionRefID: "original-run", Status: harness.InvocationCancelled,
		Attempt: 1, Revision: 1, CreatedAt: now, UpdatedAt: now, OutputRefs: []harness.HostRef{}}
	if _, _, err = s.CreateInvocation(ctx, root); err != nil {
		t.Fatal(err)
	}
	if missing {
		err = db.Exec("DELETE FROM agent_harness_config_snapshots WHERE id = ?", config.ID).Error
	} else {
		broken := config
		broken.SharedBudget = nil
		raw, _ := json.Marshal(broken)
		err = db.Exec("UPDATE agent_harness_config_snapshots SET payload_json = ? WHERE id = ?", string(raw), config.ID).Error
	}
	if err != nil {
		t.Fatal(err)
	}
	retried, err := s.RetryInvocation(ctx, root.ID, root.Revision, "new-physical-run", now.Add(time.Second))
	after, _ := s.GetTurn(ctx, turn.ID)
	t.Logf("corrupted immutable Config kept original content hash=%s; retry err=%v attempt=%d Turn=%s", config.ContentHash, err, retried.Attempt, after.Status)
	if !errors.Is(err, harness.ErrConflict) || retried.ID != "" || after.Status != harness.TurnCancelled || after.Revision != turn.Revision {
		t.Fatal("corrupt frozen budget reopened original Turn")
	}
}

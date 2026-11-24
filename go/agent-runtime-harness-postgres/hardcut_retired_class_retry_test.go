package harnesspostgres

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

// An Application no longer has a selector capable of entering Agent Retry.
// PostgreSQL rejects all old non-Agent rows at the explicit DDL cutover;
// after the cut, the original Agent input/attempt remains mandatory.
func TestAgentOnlySQLRetryRejectsChildAndCorruptedOriginalInput(t *testing.T) {
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	input := []byte(`{"goal":"original Agent"}`)
	digest := sha256.Sum256(input)
	original := harness.Invocation{ID: "original-agent", TurnID: "turn", Input: input,
		InputHash: hex.EncodeToString(digest[:]), ExecutionRefID: "original-run",
		Revision: 1, Attempt: 1, Status: harness.InvocationFailed,
		CreatedAt: now, UpdatedAt: now}
	if _, err := invocationToRecord(original); err != nil || !validStoredInvocationRetry(original, 1) {
		t.Fatalf("valid agent refused: %v", err)
	}
	child := original
	child.ParentItemID = "parent-item"
	if validStoredInvocationRetry(child, 1) {
		t.Fatal("native Child could rotate root Attempt")
	}
	wrong := original
	wrong.InputHash = "invalid-original-hash"
	if _, err := invocationToRecord(wrong); err == nil {
		t.Fatal("tampered original Agent input accepted for SQL persistence")
	}
}

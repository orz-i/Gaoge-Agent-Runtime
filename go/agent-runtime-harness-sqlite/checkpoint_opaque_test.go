package harnesssqlite_test

import (
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness/conformance"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestSQLiteCheckpointOpaqueEvidenceReopen(t *testing.T) {
	path, store := newHost(t)
	conformance.CheckpointOpaqueEvidence(t, store, func() harness.Store {
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		return reopenHost(t, path)
	})
}

func TestSQLiteCheckpointCorruptOpaqueEvidenceFailsAfterRestart(t *testing.T) {
	path, store := newHost(t)
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	original, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
		ScopeID: "corrupt-opaque", StaticFingerprint: runtimecontext.StaticFingerprint("original"),
		Instructions: "original", SourcePath: []string{"user", "assistant", "tool"},
		Entries: []runtimecontext.Entry{
			{ID: "user", SourceID: "user", Message: model.Message{Role: model.RoleUser, Content: model.TextParts("hi")}},
			{ID: "assistant", SourceID: "assistant", Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tools.Call{{ID: "call", ToolKey: "lookup", Arguments: json.RawMessage(`{"html":"<>&"}`)}}}},
			{ID: "tool", SourceID: "tool", Message: model.Message{Role: model.RoleTool, ToolCallID: "call", Content: model.TextParts("ok")}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutContextCheckpoint(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	pool := rawPool(t, path)
	if _, err = pool.ExecContext(t.Context(), `UPDATE agent_harness_context_checkpoints SET payload_json = replace(payload_json, '"opaqueHash":"', '"opaqueHash":"f') WHERE id = ?`, original.ID); err != nil {
		t.Fatal(err)
	}
	reopened := reopenHost(t, path)
	if _, err = reopened.GetContextCheckpoint(t.Context(), original.ID); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("corrupted physical evidence was accepted after restart: %v", err)
	}
	if _, _, err = reopened.PutContextCheckpoint(t.Context(), original); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("original Store replay overwrote corrupt evidence: %v", err)
	}
}

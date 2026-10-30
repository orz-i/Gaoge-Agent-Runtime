package context_test

import (
	"errors"
	"testing"

	runtimectx "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestCapturePreservesEmptyAssistantNativeContinuationAcrossRestart(t *testing.T) {
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	checkpoint, err := manager.Open(t.Context(), openRequest("native-user"))
	requireNoError(t, err)
	messages := append(runtimectx.Materialize(checkpoint.Window), model.Message{Role: model.RoleAssistant})
	captured, err := manager.Capture(t.Context(), runtimectx.CaptureRequest{
		Previous: checkpoint, StaticFingerprint: checkpoint.StaticFingerprint,
		RunID: "native-run", Messages: messages,
	})
	requireNoError(t, err)
	if !runtimectx.ValidCheckpoint(captured) {
		t.Fatal("native continuation placeholder corrupted the checkpoint")
	}
	restarted := runtimectx.NewManager(runtimectx.Dependencies{})
	finalMessages := append(runtimectx.Materialize(captured.Window), model.Message{Role: model.RoleAssistant, Content: model.TextParts("finished using native evidence")})
	_, err = restarted.Capture(t.Context(), runtimectx.CaptureRequest{
		Previous: captured, StaticFingerprint: captured.StaticFingerprint,
		RunID: "native-run", Messages: finalMessages,
	})
	requireNoError(t, err)
	messages[len(messages)-1].ToolCallID = "unmatched-result"
	_, err = manager.Capture(t.Context(), runtimectx.CaptureRequest{
		Previous: checkpoint, StaticFingerprint: checkpoint.StaticFingerprint,
		RunID: "native-run", Messages: messages,
	})
	if !errors.Is(err, runtimectx.ErrLineageConflict) {
		t.Fatalf("assistant placeholder authorized an unmatched tool result: %v", err)
	}
}

package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// The standard Session must exercise the same frozen Skill Context as an
// explicitly assembled Runner, including after a repeated admission.
func TestAgentSessionProjectsSelectedSkillInstructions(t *testing.T) {
	client := &agentSessionModel{generate: func(request model.Request) (model.Response, error) {
		seen := false
		for _, message := range request.Messages {
			if message.Role == model.RoleSystem && strings.Contains(model.TextValue(message.Content), "Check signed release artifacts") {
				seen = true
			}
		}
		if !seen {
			t.Fatal("frozen selected Skill was not presented to model")
		}
		return model.Response{Content: "release verified"}, nil
	}}
	options := agentSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), client)
	options.Session.Config.Skills = []harness.SkillSnapshot{{ID: "release-review", Revision: 2, Title: "Release review", Markdown: "Check signed release artifacts"}}
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := session.Prompt(t.Context(), "release-1", "Review the release")
	if err != nil || completed.Turn.Status != harness.TurnCompleted || len(client.requests) != 1 {
		t.Fatalf("selected Skill execution: status=%s err=%v requests=%d", completed.Turn.Status, err, len(client.requests))
	}
	if _, err := session.Prompt(t.Context(), "release-1", "Review the release"); err != nil || len(client.requests) != 1 {
		t.Fatalf("idempotent Skill replay caused a second model effect: %v", err)
	}
}

func TestAgentSessionToolApprovalAndOwnerBoundResolution(t *testing.T) {
	runStore := memory.NewStore()
	store := harness.NewMemoryStore()
	calls := 0
	client := &agentSessionModel{generate: func(request model.Request) (model.Response, error) {
		for _, message := range request.Messages {
			if message.Role == model.RoleTool {
				return model.Response{Content: "approved result"}, nil
			}
		}
		return model.Response{ToolCalls: []tools.Call{{ID: "write-1", ToolKey: "write-record", Arguments: json.RawMessage(`{}`)}}}, nil
	}}
	options := agentSessionOptions(t, runStore, store, client)
	options.LocalTools = []harness.SessionTool{{Registration: agentSessionTool("write-record", &calls), Policy: harness.ToolPolicySnapshot{DefinitionVersion: "v1", ApprovalMode: "always", ApprovalCapability: "per_call", SideEffectLevel: "write"}}}
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := session.Prompt(t.Context(), "write-1", "Write a record")
	if err != nil || pending.Turn.Status != harness.TurnWaitingInput || calls != 0 {
		t.Fatalf("approval was not enforced before Tool effect: status=%s err=%v calls=%d", pending.Turn.Status, err, calls)
	}
	root, ok := harness.TopLevelInvocation(pending)
	if !ok {
		t.Fatal("missing frozen root")
	}
	run, err := options.Runtime.Load(t.Context(), root.ExecutionRefID)
	if err != nil || run.Checkpoint == nil || run.Checkpoint.Status != kernel.CheckpointPending {
		t.Fatalf("missing pending checkpoint: %v", err)
	}
	request := harness.ResolveApprovalRequest{CheckpointID: run.Checkpoint.ID, Decision: harness.ApprovalApprove}
	impostor := options
	impostor.Session.Actor.ActorID = "foreign-actor"
	foreign, err := harness.OpenAgentSession(impostor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreign.ResolveApproval(t.Context(), "write-1", request); !errors.Is(err, harness.ErrConflict) || calls != 0 {
		t.Fatalf("foreign Session approved original effect: %v calls=%d", err, calls)
	}
	done, err := session.ResolveApproval(t.Context(), "write-1", request)
	if err != nil || done.Turn.Status != harness.TurnCompleted || calls != 1 || len(client.requests) != 2 {
		t.Fatalf("approval resolution status=%s err=%v tools=%d models=%d", done.Turn.Status, err, calls, len(client.requests))
	}
	if _, err := session.ResolveApproval(context.Background(), "write-1", request); err == nil || calls != 1 {
		t.Fatalf("replayed approval dispatched an effect: %v tools=%d", err, calls)
	}
}

package harness_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestAgentSessionReopenCannotChangeOriginalToolPolicyBeforeApproval(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mutate      func(*harness.AgentSessionOptions)
		policyDrift bool
	}{
		{"definition-version", func(o *harness.AgentSessionOptions) { o.LocalTools[0].Policy.DefinitionVersion = "new-v2" }, false},
		{"approval-downgrade", func(o *harness.AgentSessionOptions) {
			o.LocalTools[0].Policy.ApprovalMode = "never"
			o.LocalTools[0].Policy.ApprovalCapability = ""
		}, false},
		{"side-effect-drift", func(o *harness.AgentSessionOptions) { o.LocalTools[0].Policy.SideEffectLevel = "read" }, false},
		{"budget-added-on-reopen", func(o *harness.AgentSessionOptions) {
			o.Session.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 3}
		}, false},
		{"definition-fingerprint", func(o *harness.AgentSessionOptions) {
			o.LocalTools[0].Registration.Definition.Name = "new_tool_definition"
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &agentSessionModel{generate: func(r model.Request) (model.Response, error) {
				for _, m := range r.Messages {
					if m.Role == model.RoleTool {
						return model.Response{Content: "done"}, nil
					}
				}
				return model.Response{ToolCalls: []tools.Call{{ID: "write-1", ToolKey: "write", Arguments: json.RawMessage(`{}`)}}}, nil
			}}
			o := agentSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), client)
			o.LocalTools = []harness.SessionTool{{Registration: agentSessionTool("write", &calls), Policy: harness.ToolPolicySnapshot{
				DefinitionVersion: "v1", ApprovalMode: "always", ApprovalCapability: "per_call", SideEffectLevel: "write",
			}}}
			original, err := harness.OpenAgentSession(o)
			if err != nil {
				t.Fatal(err)
			}
			waiting, err := original.Prompt(t.Context(), "pending", "Requires authorization")
			if err != nil || waiting.Turn.Status != harness.TurnWaitingInput || calls != 0 {
				t.Fatalf("original approval: status=%s err=%v calls=%d", waiting.Turn.Status, err, calls)
			}
			o.Session.Transcript = nil
			tc.mutate(&o)
			changed, err := harness.OpenAgentSession(o)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := changed.Load(t.Context(), "pending"); err != nil {
				t.Fatalf("read-only history should remain available: %v", err)
			}
			root, ok := harness.TopLevelInvocation(waiting)
			if !ok {
				t.Fatal("root missing")
			}
			pending, err := o.Runtime.Load(t.Context(), root.ExecutionRefID)
			if err != nil || pending.Checkpoint == nil {
				t.Fatalf("approval checkpoint missing: %v", err)
			}
			approval := harness.ResolveApprovalRequest{CheckpointID: pending.Checkpoint.ID, Decision: harness.ApprovalApprove}
			_, err = changed.ResolveApproval(t.Context(), "pending", approval)
			if err == nil || (!tc.policyDrift && !errors.Is(err, harness.ErrInvalidRequest)) || calls != 0 || len(client.requests) != 1 {
				t.Fatalf("changed Tool policy/definition reauthorized original side effect: err=%v tools=%d models=%d", err, calls, len(client.requests))
			}
			if !tc.policyDrift {
				if _, err := changed.Resume(t.Context(), "pending"); !errors.Is(err, harness.ErrInvalidRequest) {
					t.Fatalf("changed policy resumed old Run: %v", err)
				}
			}
			if tc.policyDrift {
				// Native Agent intentionally fails a stale Tool-definition Attempt.
				// An explicitly authorized new Attempt, not the old checkpoint,
				// must be used after definition revocation.
				after, loadErr := o.Runtime.Load(t.Context(), root.ExecutionRefID)
				if loadErr != nil || after.Run.Status != kernel.RunStatusFailed || calls != 0 {
					t.Fatalf("definition change consumed an effect or avoided failure: physical=%s err=%v tools=%d", after.Run.Status, loadErr, calls)
				}
				return
			}
			// Original policy must still authorize exactly one original effect.
			completed, err := original.ResolveApproval(t.Context(), "pending", approval)
			if err != nil || completed.Turn.Status != harness.TurnCompleted || calls != 1 {
				t.Fatalf("original owner was blocked: status=%s err=%v calls=%d", completed.Turn.Status, err, calls)
			}
		})
	}
}

func TestAgentSessionExplorationDoesNotRelaxApprovalGatedReads(t *testing.T) {
	calls := 0
	client := &agentSessionModel{generate: func(model.Request) (model.Response, error) {
		return model.Response{ToolCalls: []tools.Call{{ID: "approved-1", ToolKey: "private.read", Arguments: json.RawMessage(`{}`)}}}, nil
	}}
	o := agentSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), client)
	o.ExploreReads = true
	o.LocalTools = []harness.SessionTool{{Registration: agentSessionTool("private.read", &calls), Policy: harness.ToolPolicySnapshot{
		DefinitionVersion: "v1", ApprovalMode: "always", ApprovalCapability: "per_call", SideEffectLevel: "read",
	}}}
	session, err := harness.OpenAgentSession(o)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := session.Prompt(t.Context(), "approved-read", "Ask with consent")
	if err != nil || waiting.Turn.Status != harness.TurnWaitingInput || calls != 0 {
		t.Fatalf("approval lost: status=%s err=%v calls=%d", waiting.Turn.Status, err, calls)
	}
	root, ok := harness.TopLevelInvocation(waiting)
	if !ok {
		t.Fatal("no root")
	}
	run, err := o.Runtime.Load(t.Context(), root.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := agent.ViewState(run)
	if err != nil {
		t.Fatal(err)
	}
	if view.ToolProgressPolicy != nil {
		t.Fatalf("approval-gated Tool was made an autonomous exploration Tool: %+v", view.ToolProgressPolicy)
	}
}

func TestAgentSessionSkillPagesRequireOriginalFrozenContent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		maxBytes int
		content  string
	}{
		{"default Context result ceiling", 0, strings.Repeat("s", 8000) + "终🙂"},
		{"tight frozen Context result ceiling", 512, strings.Repeat("s", 1100) + "终🙂"},
	} {
		t.Run(tc.name, func(t *testing.T) { exerciseSkillPageScenario(t, tc.content, tc.maxBytes) })
	}
}

func exerciseSkillPageScenario(t *testing.T, content string, maxBytes int) {
	t.Helper()
	limit := maxBytes
	if limit == 0 {
		limit = 2048
	}
	calls, lastOffset := 0, 0
	var recovered strings.Builder
	client := &agentSessionModel{generate: func(r model.Request) (model.Response, error) {
		calls++
		if calls == 1 {
			for _, m := range r.Messages {
				if strings.Contains(model.TextValue(m.Content), strings.Repeat("s", 120)) {
					t.Fatal("inlined Skill body")
				}
			}
		} else {
			// Each requested Skill page must survive Context compaction without
			// requiring the unrelated archive-reader capability.
			var latest struct {
				Offset, NextOffset int
				Done               bool
				Markdown           string
			}
			found := false
			for _, message := range r.Messages {
				if message.Role == model.RoleTool {
					if err := json.Unmarshal([]byte(model.TextValue(message.Content)), &latest); err == nil {
						found = true
					}
				}
			}
			if !found || latest.Offset != lastOffset || latest.NextOffset <= latest.Offset ||
				len([]byte(model.TextValue(r.Messages[len(r.Messages)-1].Content))) > limit {
				t.Fatalf("Skill page was archived or cursor drifted: %+v found=%t expected=%d", latest, found, lastOffset)
			}
			recovered.WriteString(latest.Markdown)
			lastOffset = latest.NextOffset
			if latest.Done {
				if recovered.String() != content {
					t.Fatalf("Skill paging lost original Unicode data: recovered=%d want=%d", recovered.Len(), len(content))
				}
				return model.Response{Content: "verified every Skill page"}, nil
			}
			available := false
			for _, d := range r.Tools {
				if d.Key == harness.SkillReadToolKey {
					available = true
				}
			}
			if !available {
				t.Fatal("Skill reader disabled before reaching final page")
			}
		}
		if calls > 12 {
			t.Fatal("Skill paging did not converge")
		}
		return model.Response{ToolCalls: []tools.Call{{
			ID: fmt.Sprintf("page-%d", calls), ToolKey: harness.SkillReadToolKey,
			Arguments: json.RawMessage(fmt.Sprintf(`{"id":"doc","offset":%d,"limit":8000}`, lastOffset)),
		}}}, nil
	}}
	options := agentSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), client)
	options.Limits = agent.Limits{MaxLLMCalls: 14, MaxToolCalls: 13}
	options.Session.Config.ContextPolicy = runtimecontext.Policy{MaxToolResultBytes: maxBytes}
	options.Session.Config.Skills = []harness.SkillSnapshot{{ID: "doc", Revision: 1, Title: "Long original", Markdown: content}}
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	done, err := session.Prompt(t.Context(), "paged", "Read complete exact document")
	if err != nil || done.Turn.Status != harness.TurnCompleted || recovered.String() != content || calls <= 2 {
		t.Fatalf("original selected Skill paging failed: status=%s err=%v calls=%d recovered=%d", done.Turn.Status, err, calls, recovered.Len())
	}
}

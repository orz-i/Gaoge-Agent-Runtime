package workflow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestIssue107EffectChildStateMatrix(t *testing.T) {
	cases := []struct {
		name         string
		effectStatus EffectStatus
		childStatus  kernel.RunStatus
		receipt      string
		output       string
		childOutput  string
		want         bool
	}{
		{"pending waiting", EffectPending, kernel.RunStatusWaitingInput, "", "", "", true},
		{"pending completed before parent observed", EffectPending, kernel.RunStatusCompleted, "", "", `{"ok":true}`, true},
		{"completed matching receipt", EffectCompleted, kernel.RunStatusCompleted, "effect", `{"ok":true}`, `{"ok":true}`, true},
		{"completed matching object different order", EffectCompleted, kernel.RunStatusCompleted, "effect", `{"a":1,"b":2}`, `{"b":2,"a":1}`, true},
		{"completed but waiting", EffectCompleted, kernel.RunStatusWaitingInput, "effect", `{"ok":true}`, "", false},
		{"completed but running", EffectCompleted, kernel.RunStatusRunning, "effect", `{"ok":true}`, "", false},
		{"completed but failed", EffectCompleted, kernel.RunStatusFailed, "effect", `{"ok":true}`, "", false},
		{"completed stale receipt", EffectCompleted, kernel.RunStatusCompleted, "other", `{"ok":true}`, `{"ok":true}`, false},
		{"completed absent receipt", EffectCompleted, kernel.RunStatusCompleted, "", `{"ok":true}`, `{"ok":true}`, false},
		{"completed absent result", EffectCompleted, kernel.RunStatusCompleted, "effect", `{"ok":true}`, "", false},
		{"completed differing result", EffectCompleted, kernel.RunStatusCompleted, "effect", `{"ok":true}`, `{"ok":false}`, false},
		{"completed corrupted result", EffectCompleted, kernel.RunStatusCompleted, "effect", `{"ok":true}`, "not-json", false},
		{"completed mismatched null output", EffectCompleted, kernel.RunStatusCompleted, "effect", "null", `{"ok":true}`, false},
		{"failed parent never authorizes", EffectFailed, kernel.RunStatusFailed, "effect", `{"ok":true}`, `{"ok":true}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLocalAgentProofFixture(t)
			mutateLocalAgentProofState(t, &f, func(state *executionState) {
				state.Effects[0].Status = tc.effectStatus
				state.Effects[0].ReceiptID = tc.receipt
				state.Effects[0].Output = json.RawMessage(tc.output)
			})
			f.child.Run.Status = tc.childStatus
			if tc.childOutput != "" {
				f.child.Result = &kernel.Result{ContentType: "application/json", Content: json.RawMessage(tc.childOutput)}
			}
			got := f.runner.VerifyLocalChild(f.runtime, f.parent, f.relation, f.child, false)
			if got != tc.want {
				t.Fatalf("effect=%s child=%s receipt=%q got=%v want=%v", tc.effectStatus, tc.childStatus, tc.receipt, got, tc.want)
			}
		})
	}
}

func TestIssue107AgentTaskDoesNotRequireSubworkflowDefinitionRegistry(t *testing.T) {
	f := newLocalAgentProofFixture(t)
	f.runner.registry = nil
	if !f.runner.VerifyLocalChild(f.runtime, f.parent, f.relation, f.child, false) {
		t.Fatal("AgentTask-only Workflow needs no definition registry")
	}
	if f.runner.VerifyLocalChild(f.runtime, f.parent, f.relation, kernel.Snapshot{
		Run: kernel.Run{ID: f.child.Run.ID, Kind: RunKind, Actor: f.child.Run.Actor, Thread: f.child.Run.Thread},
	}, false) {
		t.Fatal("Subworkflow proof accidentally bypassed its required registry")
	}
}

func TestIssue107HostedTerminalOwnershipIsReadOnlyNotNewCancellation(t *testing.T) {
	f := newLocalAgentProofFixture(t)
	hosted, err := agent.NewRunner(agent.Dependencies{
		Runtime: f.runtime, Model: localAgentProofModel{},
		PrepareModelRequest: func(_ context.Context, r model.Request) (model.Request, error) { return r, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	f.executor.original = hosted
	router, err := NewStaticEffectRouter(nil, EffectRoute{Class: EffectClassAgent, Executor: f.executor})
	if err != nil {
		t.Fatal(err)
	}
	f.runner.effects = router
	if hosted.OwnsKernelRuntime(f.runtime) {
		t.Fatal("Host callback incorrectly granted native physical cancellation")
	}
	if f.runner.VerifyLocalChild(f.runtime, f.parent, f.relation, f.child, true) {
		t.Fatal("waiting Host Agent with no approved Harness fence permitted physical cancel")
	}
	for _, status := range []kernel.RunStatus{kernel.RunStatusCancelled, kernel.RunStatusFailed, kernel.RunStatusCompleted} {
		f.child.Run.Status = status
		if status == kernel.RunStatusCompleted {
			f.child.Result = &kernel.Result{ContentType: "application/json", Content: json.RawMessage(`{"ok":true}`)}
		}
		if !f.runner.VerifyLocalChild(f.runtime, f.parent, f.relation, f.child, true) {
			t.Fatalf("terminal status %s could not be read for safe settlement", status)
		}
	}
	f.child.Result = nil
	if f.runner.VerifyLocalChild(f.runtime, f.parent, f.relation, f.child, true) {
		t.Fatal("malformed completed child without result accepted as terminal")
	}
}

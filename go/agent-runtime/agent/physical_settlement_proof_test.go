package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestIssue107TerminalPhysicalSettlementStateMatrix(t *testing.T) {
	base := runState{Budget: budget.Snapshot{Limits: budget.Limits{
		MaxLLMCalls: 4, MaxToolCalls: 4,
	}}}
	makeSnapshot := func(status kernel.RunStatus, state runState) kernel.Snapshot {
		t.Helper()
		encoded, err := encodeState(state)
		if err != nil {
			t.Fatal(err)
		}
		result := (*kernel.Result)(nil)
		if status == kernel.RunStatusCompleted {
			result = &kernel.Result{ContentType: "application/json", Content: json.RawMessage(`{"ok":true}`)}
		}
		return kernel.Snapshot{Run: kernel.Run{ID: "agent-run", Kind: RunKind, Status: status},
			State: encoded, Result: result}
	}
	for _, status := range []kernel.RunStatus{
		kernel.RunStatusCompleted, kernel.RunStatusFailed, kernel.RunStatusCancelled,
	} {
		t.Run(string(status)+" settled", func(t *testing.T) {
			if !TerminalPhysicalEffectsSettled(makeSnapshot(status, base)) {
				t.Fatal("settled terminal state could not be reconciled")
			}
		})
	}
	for _, status := range []kernel.RunStatus{
		kernel.RunStatusRunning, kernel.RunStatusWaitingInput, kernel.RunStatusPausedBudget,
	} {
		if TerminalPhysicalEffectsSettled(makeSnapshot(status, base)) {
			t.Fatalf("nonterminal %s promoted to settled", status)
		}
	}
	for name, change := range map[string]func(*runState){
		"unknown physical Tool dispatch": func(s *runState) { s.ToolDispatchID = "unsettled-call" },
		"uncertain model invocation": func(s *runState) {
			s.ModelInvocations = []ModelInvocation{{ID: "unsettled", Status: ModelInvocationPending}}
		},
		"completed but unconsumed model": func(s *runState) {
			s.ModelInvocations = []ModelInvocation{{ID: "receipt", Status: ModelInvocationCompleted}}
		},
		"unsettled Tool wait": func(s *runState) {
			s.ToolDispatchID = "pending-tool"
			s.ToolReturns = []returnedTool{{CallID: "pending-tool", Wait: &PendingTool{}}}
		},
		"unsettled deferred children": func(s *runState) { s.ToolReturns = []returnedTool{{Wait: &PendingTool{}}} },
		"unconsumed external signals": func(s *runState) { s.ExternalSignals = []ExternalToolResultSignal{{}} },
	} {
		t.Run(name, func(t *testing.T) {
			state := base
			change(&state)
			for _, status := range []kernel.RunStatus{kernel.RunStatusCancelled, kernel.RunStatusFailed} {
				if TerminalPhysicalEffectsSettled(makeSnapshot(status, state)) {
					t.Fatalf("unknown physical effect was certified settled after %s", status)
				}
			}
		})
	}
	// A valid signal is historical only with the exact original consumed
	// model call and durable Tool result. Duplicate/foreign/unfinished facts
	// must never be treated as settlement.
	call := tools.Call{ID: "call", ToolKey: "external"}
	signal := ExternalToolResultSignal{RunID: "agent-run", CallID: call.ID, OperationID: "op", InputDigest: strings.Repeat("e", 64), SignalID: "signal"}
	history := base
	history.ExternalSignals = []ExternalToolResultSignal{signal}
	now := time.Now().UTC()
	history.ModelInvocations = []ModelInvocation{{ID: "model", RunID: signal.RunID, SourceRevision: 1, RequestHash: strings.Repeat("a", 64), CreatedAt: now, CompletedAt: &now, ConsumedAt: &now, Status: ModelInvocationConsumed, Response: model.Response{ToolCalls: []tools.Call{call}}}}
	history.Messages = []model.Message{{Role: model.RoleTool, ToolCallID: call.ID, Content: model.TextParts("42")}}
	if !TerminalPhysicalEffectsSettled(makeSnapshot(kernel.RunStatusCompleted, history)) {
		t.Fatal("consumed original external signal blocked terminal observation")
	}
	for name, alter := range map[string]func(*runState){
		"missing result":        func(s *runState) { s.Messages = nil },
		"missing original call": func(s *runState) { s.ModelInvocations = nil },
		"foreign original call": func(s *runState) {
			s.ModelInvocations = append([]ModelInvocation(nil), s.ModelInvocations...)
			s.ModelInvocations[0].RunID = "foreign"
		},
		"duplicate signal": func(s *runState) { s.ExternalSignals = append(s.ExternalSignals, signal) },
		"duplicate result": func(s *runState) { s.Messages = append(s.Messages, s.Messages[0]) },
		"staged result":    func(s *runState) { s.ToolReturns = []returnedTool{{CallID: call.ID, Outcome: &toolOutcomeReceipt{}}} },
	} {
		t.Run(name, func(t *testing.T) {
			state := history
			alter(&state)
			if TerminalPhysicalEffectsSettled(makeSnapshot(kernel.RunStatusCompleted, state)) {
				t.Fatal("invalid original signal history treated as settled")
			}
		})
	}
	completed := makeSnapshot(kernel.RunStatusCompleted, base)
	completed.Result = nil
	if TerminalPhysicalEffectsSettled(completed) {
		t.Fatal("missing completed receipt authorized physical settlement")
	}
	if TerminalPhysicalEffectsSettled(kernel.Snapshot{Run: kernel.Run{ID: "other", Kind: "workflow", Status: kernel.RunStatusCancelled},
		State: makeSnapshot(kernel.RunStatusCancelled, base).State}) {
		t.Fatal("foreign feature Run was certified as Agent terminal")
	}
}

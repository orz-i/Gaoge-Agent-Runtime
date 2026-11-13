package agent

import (
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestIssue107TerminalPhysicalSettlementStateMatrix(t *testing.T) {
	base := runState{Budget: budget.Snapshot{Limits: budget.Limits{
		MaxLLMCalls: 4, MaxToolCalls: 4,
	}}}
	makeSnapshot := func(status kernel.RunStatus, state runState) kernel.Snapshot {
		t.Helper()
		encoded, err := json.Marshal(state)
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
		"unsettled Tool wait":         func(s *runState) { s.ToolDispatchID = "pending-tool"; s.ToolWait = &PendingTool{} },
		"unsettled deferred children": func(s *runState) { s.DeferredChildren = []deferredChildCall{{}} },
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

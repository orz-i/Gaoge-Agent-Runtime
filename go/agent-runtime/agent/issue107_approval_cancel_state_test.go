package agent

import (
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestIssue107ApprovalCancellationProofStateMatrix(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{runtime: runtime}
	call := tools.Call{ID: "call", ToolKey: "read", Name: "read_evidence", Arguments: json.RawMessage(`{}`)}
	checkpoint := &kernel.Checkpoint{
		ID: "checkpoint", Kind: "tool_approval", Status: kernel.CheckpointPending,
		Payload: json.RawMessage(`{"call":"call"}`),
	}
	binding, err := newPendingToolApproval(checkpoint, call)
	if err != nil {
		t.Fatal(err)
	}
	base := runState{PendingCalls: []tools.Call{call}, PendingApproval: binding, Budget: budget.Snapshot{Limits: budget.Limits{MaxLLMCalls: 4, MaxToolCalls: 4}}}
	makeSnapshot := func(state runState) kernel.Snapshot {
		t.Helper()
		encoded, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		cp := *checkpoint
		return kernel.Snapshot{Run: kernel.Run{
			ID: "run", Kind: RunKind, Status: kernel.RunStatusWaitingInput,
		}, Checkpoint: &cp, State: encoded}
	}
	if !runner.QuiescentApprovalCancellation(runtime, makeSnapshot(base)) {
		t.Fatal("exact pending Tool approval should be quiescent")
	}
	cases := map[string]func(*kernel.Snapshot){
		"already approved":       func(s *kernel.Snapshot) { s.Checkpoint.Status = kernel.CheckpointResolved },
		"wrong checkpoint":       func(s *kernel.Snapshot) { s.Checkpoint.ID = "foreign" },
		"no checkpoint":          func(s *kernel.Snapshot) { s.Checkpoint = nil },
		"already terminal":       func(s *kernel.Snapshot) { s.Run.Status = kernel.RunStatusCompleted },
		"running after approval": func(s *kernel.Snapshot) { s.Run.Status = kernel.RunStatusRunning },
		"other Run kind":         func(s *kernel.Snapshot) { s.Run.Kind = "workflow" },
		"has result": func(s *kernel.Snapshot) {
			s.Result = &kernel.Result{ContentType: "application/json", Content: json.RawMessage(`{}`)}
		},
		"unresolved Model": func(s *kernel.Snapshot) {
			var st runState
			if err := json.Unmarshal(s.State, &st); err != nil {
				t.Fatal(err)
			}
			st.ModelInvocations = []ModelInvocation{{ID: "model", Status: ModelInvocationPending}}
			s.State, _ = json.Marshal(st)
		},
		"completed but unconsumed Model": func(s *kernel.Snapshot) {
			var st runState
			if err := json.Unmarshal(s.State, &st); err != nil {
				t.Fatal(err)
			}
			st.ModelInvocations = []ModelInvocation{{ID: "model", Status: ModelInvocationCompleted}}
			s.State, _ = json.Marshal(st)
		},
		"unresolved Tool dispatch": func(s *kernel.Snapshot) {
			var st runState
			if err := json.Unmarshal(s.State, &st); err != nil {
				t.Fatal(err)
			}
			st.ToolDispatchID = "committed-external-effect"
			s.State, _ = json.Marshal(st)
		},
		"pending remote Tool": func(s *kernel.Snapshot) {
			var st runState
			if err := json.Unmarshal(s.State, &st); err != nil {
				t.Fatal(err)
			}
			st.ToolWait = &PendingTool{}
			s.State, _ = json.Marshal(st)
		},
		"no approval binding": func(s *kernel.Snapshot) {
			var st runState
			if err := json.Unmarshal(s.State, &st); err != nil {
				t.Fatal(err)
			}
			st.PendingApproval = nil
			s.State, _ = json.Marshal(st)
		},
		"no pending call": func(s *kernel.Snapshot) {
			var st runState
			if err := json.Unmarshal(s.State, &st); err != nil {
				t.Fatal(err)
			}
			st.PendingCalls = nil
			s.State, _ = json.Marshal(st)
		},
		"replaced Tool args": func(s *kernel.Snapshot) {
			var st runState
			if err := json.Unmarshal(s.State, &st); err != nil {
				t.Fatal(err)
			}
			st.PendingCalls[0].Arguments = json.RawMessage(`{"changed":true}`)
			s.State, _ = json.Marshal(st)
		},
		"invalid state": func(s *kernel.Snapshot) { s.State = json.RawMessage(`{`) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s := makeSnapshot(base)
			change(&s)
			if runner.QuiescentApprovalCancellation(runtime, s) {
				t.Fatal("uncertain Agent effect was promoted to physical cancellation")
			}
		})
	}
	foreign, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	if runner.QuiescentApprovalCancellation(foreign, makeSnapshot(base)) {
		t.Fatal("same-ID foreign Kernel was promoted")
	}
}

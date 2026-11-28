package agent

import (
	"encoding/json"
	"errors"
	"testing"

	runtimebudget "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestFrozenPendingDispatchReadsOriginalCallWithoutGrantingReplay(t *testing.T) {
	arguments := "{\"memberID\":\"analyst\",\"goal\":\"review\"}"
	state := runState{
		Budget:         runtimebudget.Snapshot{Limits: runtimebudget.Limits{MaxLLMCalls: 4, MaxToolCalls: 4}},
		PendingCalls:   []tools.Call{{ID: "delegate-1", ToolKey: "harness.delegate_agent", Arguments: json.RawMessage(arguments)}},
		ToolDispatchID: "delegate-1",
	}
	raw, err := encodeState(state)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := kernel.Snapshot{Run: kernel.Run{Kind: RunKind, Status: kernel.RunStatusRunning}, State: raw}
	call, pending, err := FrozenPendingDispatch(snapshot)
	if err != nil || !pending || call.ID != state.ToolDispatchID || call.ToolKey != state.PendingCalls[0].ToolKey {
		t.Fatalf("original Tool identity lost: call=%+v pending=%v err=%v", call, pending, err)
	}
	call.Arguments[0] = 'X'
	if string(snapshot.State) != string(raw) || string(state.PendingCalls[0].Arguments) != arguments {
		t.Fatal("returned Tool argument borrowed mutable original backing memory")
	}
	terminal := snapshot
	terminal.Run.Status = kernel.RunStatusCompleted
	if _, found, err := FrozenPendingDispatch(terminal); found || err != nil {
		t.Fatalf("terminal Agent allowed a pending physical dispatch: %v %v", found, err)
	}
	foreign := snapshot
	foreign.Run.Kind = kernel.RunKind("other")
	if _, found, err := FrozenPendingDispatch(foreign); found || err != nil {
		t.Fatalf("foreign RunKind authorized a Tool: %v %v", found, err)
	}
	state.ToolDispatchID = "unrelated"
	snapshot.State, err = encodeState(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := FrozenPendingDispatch(snapshot); found || !errors.Is(err, ErrToolOutcomeUnknown) {
		t.Fatalf("unmatched ToolDispatchID lost its no-replay fence: %v %v", found, err)
	}
	// The same immutable call is observable through its typed wait only.
	state.ToolDispatchID = state.PendingCalls[0].ID
	state.ToolWait = &PendingTool{PendingExecution: tools.PendingExecution{Kind: tools.PendingChildTerminal, OperationID: "operation", ChildRunID: "child"}, CallID: state.ToolDispatchID, ToolKey: state.PendingCalls[0].ToolKey, Generation: 1}
	snapshot.State, err = encodeState(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := FrozenPendingDispatch(snapshot); found || err != nil {
		t.Fatalf("typed wait was exposed as an undisposed intent: %v %v", found, err)
	}
	if observed, wait, found, err := FrozenChildWaitCall(snapshot); err != nil || !found || observed.ID != state.ToolDispatchID || wait.ChildRunID != "child" {
		t.Fatalf("original child wait lost: %+v %+v %v %v", observed, wait, found, err)
	}
	state.ToolDispatchID = ""
	snapshot.State, err = encodeState(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, found, err := FrozenChildWaitCall(snapshot); found || err == nil {
		t.Fatalf("wait without original dispatch was trusted: %v %v", found, err)
	}
	snapshot.State = json.RawMessage("{\"budget\":")
	if _, found, err := FrozenPendingDispatch(snapshot); found || err == nil {
		t.Fatalf("corrupted Agent state was trusted: %v %v", found, err)
	}
}

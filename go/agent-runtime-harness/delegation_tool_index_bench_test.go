package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Long historical Turns demonstrate read amplification in the
// original frozen Tool request lookup. The relevant original started Item
// is one item regardless of unrelated Timeline size.
func benchmarkPreparedDelegationStore(b *testing.B, size int) (*Runner, tools.ExecutionRequest) {
	b.Helper()
	store := NewMemoryStore()
	const turnID, parentInvocationID, parentRunID, callID = "depth-history", "parent", "parent-run", "delegate-call"
	for i := range size - 1 {
		if _, _, err := store.AppendItem(context.Background(), Item{
			ID: fmt.Sprintf("unrelated-%06d", i), TurnID: turnID,
			Kind: ItemArtifact, Status: ItemCompleted,
		}); err != nil {
			b.Fatal(err)
		}
	}
	inv := Invocation{ID: parentInvocationID, TurnID: turnID, ExecutionRefID: parentRunID, ExecutionClass: ExecutionAgent}
	store.invocations[inv.ID] = inv
	store.invocationExecutionRefs[parentRunID] = inv.ID
	id := delegationToolID(inv, callID)
	_, _, err := store.AppendItem(context.Background(), Item{
		ID:     stableID("hid", turnID, id, string(ItemStarted)),
		TurnID: turnID, Kind: ItemDelegation, Status: ItemStarted, InvocationID: parentInvocationID,
		Payload: json.RawMessage(`{"roleID":"reviewer","goal":"review the draft","delegationID":"` + id + `"}`),
	})
	if err != nil {
		b.Fatal(err)
	}
	return &Runner{store: store}, tools.ExecutionRequest{
		RunID: parentRunID, Call: tools.Call{ID: callID, ToolKey: DelegationToolKey, Arguments: json.RawMessage(`{}`)},
	}
}

func BenchmarkPreparedDelegationToolRequestMemoryScaling(b *testing.B) {
	for _, size := range []int{500, 2000, 10000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			runner, request := benchmarkPreparedDelegationStore(b, size)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				got, found, err := runner.preparedDelegationToolRequest(context.Background(), request)
				if err != nil || !found || got.RoleID != "reviewer" || got.Goal != "review the draft" {
					b.Fatalf("frozen request lost: %+v found=%v err=%v", got, found, err)
				}
			}
		})
	}
}

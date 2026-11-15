package harness

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestFrozenWaitingDelegationRejectsForgedToolAndAuthority(t *testing.T) {
	turn := Turn{ID: "turn-1"}
	owner := Invocation{ID: "owner", TurnID: turn.ID, ExecutionRefID: "parent-run"}
	call := tools.Call{ID: "delegate-1", ToolKey: DelegationToolKey,
		Arguments: json.RawMessage(`{"roleID":"analyst","goal":"review","toolKeys":["web"]}`)}
	id := delegationToolID(owner, call.ID)
	childID := stableID("hchild", owner.ExecutionRefID, id)
	frozen := handoff.Delegation{ID: id, RoleID: "analyst", RoleRevision: 1,
		MemberID: "analyst", ChildRunID: childID, Goal: "review",
		Status: handoff.StatusQueued, ToolKeys: []string{"web"}}
	payload := delegationItemPayload{
		DelegationID: id, MemberID: frozen.MemberID, ChildRunID: childID,
		Goal: frozen.Goal, ParentRunID: owner.ExecutionRefID, RoleID: frozen.RoleID,
		RoleRevision: frozen.RoleRevision, Status: handoff.StatusQueued, Execution: &frozen,
	}
	wire, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	waiting := Item{ID: stableID("hid", turn.ID, id, string(ItemWaiting)),
		TurnID: turn.ID, InvocationID: owner.ID, RunID: childID,
		Kind: ItemDelegation, Status: ItemWaiting, Seq: 8, Payload: wire}
	got, err := verifiedFrozenWaitingDelegation(waiting, turn, owner, owner.ExecutionRefID, call)
	if err != nil || !sameHandoffFrozenExecution(frozen, got) {
		t.Fatalf("original frozen waiting delegation rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*Item, *tools.Call)
	}{
		{"no-store-sequence", func(item *Item, _ *tools.Call) { item.Seq = 0 }},
		{"forged-item-id", func(item *Item, _ *tools.Call) { item.ID = "foreign" }},
		{"foreign-run", func(item *Item, _ *tools.Call) { item.RunID = "foreign" }},
		{"foreign-owner", func(item *Item, _ *tools.Call) { item.InvocationID = "foreign" }},
		{"foreign-turn", func(item *Item, _ *tools.Call) { item.TurnID = "foreign" }},
		{"started-not-waiting", func(item *Item, _ *tools.Call) { item.Status = ItemStarted }},
		{"injected-parent-item", func(item *Item, _ *tools.Call) { item.ParentItemID = "foreign" }},
		{"wrong-kind", func(item *Item, _ *tools.Call) { item.Kind = ItemDiagnostic }},
		{"wrong-original-tool", func(_ *Item, call *tools.Call) { call.ToolKey = "browser.execute" }},
		{"foreign-role", func(_ *Item, call *tools.Call) {
			call.Arguments = json.RawMessage(`{"roleID":"foreign","goal":"review","toolKeys":["web"]}`)
		}},
		{"foreign-member", func(_ *Item, call *tools.Call) {
			call.Arguments = json.RawMessage(`{"roleID":"analyst","memberID":"foreign","goal":"review","toolKeys":["web"]}`)
		}},
		{"foreign-goal", func(_ *Item, call *tools.Call) {
			call.Arguments = json.RawMessage(`{"roleID":"analyst","goal":"foreign","toolKeys":["web"]}`)
		}},
		{"ungranted-tool", func(_ *Item, call *tools.Call) {
			call.Arguments = json.RawMessage(`{"roleID":"analyst","goal":"review","toolKeys":["forbidden"]}`)
		}},
		{"duplicate-tool-argument", func(_ *Item, call *tools.Call) {
			call.Arguments = json.RawMessage(`{"roleID":"analyst","roleID":"other","goal":"review"}`)
		}},
		{"unknown-tool-argument", func(_ *Item, call *tools.Call) {
			call.Arguments = json.RawMessage(`{"roleID":"analyst","goal":"review","admin":true}`)
		}},
		{"ambiguous-waiting-payload", func(item *Item, _ *tools.Call) {
			item.Payload = append(append(json.RawMessage(nil), wire[:len(wire)-1]...), []byte(`,"goal":"forged"}`)...)
		}},
		{"forged-payload-parent", func(item *Item, _ *tools.Call) {
			changed := payload
			changed.ParentRunID = "foreign-parent"
			item.Payload, _ = json.Marshal(changed)
		}},
		{"forged-payload-role", func(item *Item, _ *tools.Call) {
			changed := payload
			changed.RoleID = "foreign-role"
			item.Payload, _ = json.Marshal(changed)
		}},
		{"forged-child-execution", func(item *Item, _ *tools.Call) {
			changed := payload
			copy := frozen
			copy.MemberID = "foreign-runner"
			changed.Execution = &copy
			item.Payload, _ = json.Marshal(changed)
		}},
		{"terminal-child-impersonation", func(item *Item, _ *tools.Call) {
			changed := payload
			copy := frozen
			copy.Status = handoff.StatusCompleted
			changed.Execution = &copy
			item.Payload, _ = json.Marshal(changed)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed, otherCall := waiting, tools.CloneCall(call)
			test.mutate(&changed, &otherCall)
			if _, err := verifiedFrozenWaitingDelegation(changed, turn, owner, owner.ExecutionRefID, otherCall); !errors.Is(err, ErrConflict) {
				t.Fatalf("foreign original authority accepted: %v", err)
			}
		})
	}
}

func TestFrozenWaitingDelegationStartedAdmissionIsSingleFlight(t *testing.T) {
	store := NewMemoryStore()
	original := Item{
		ID: "immutable-child-started", TurnID: "turn", RunID: "child", InvocationID: "owner",
		Kind: ItemDelegation, Status: ItemStarted, Payload: json.RawMessage("{}"),
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	var fresh atomic.Int64
	const competitors = 24
	var wg sync.WaitGroup
	errorsReceived := make(chan error, competitors)
	for range competitors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			returned, admitted, err := appendItemFactWithAdmission(context.Background(), store, nil, original)
			if err != nil {
				errorsReceived <- err
				return
			}
			if returned.ID != original.ID || returned.Seq == 0 {
				errorsReceived <- ErrConflict
				return
			}
			if admitted {
				fresh.Add(1)
			}
		}()
	}
	wg.Wait()
	close(errorsReceived)
	for err := range errorsReceived {
		t.Fatalf("concurrent immutable Started CAS failed: %v", err)
	}
	if fresh.Load() != 1 {
		t.Fatalf("original child was admitted %d times instead of once", fresh.Load())
	}
	reloaded, err := store.GetItem(t.Context(), original.ID)
	if err != nil || reloaded.Seq == 0 || reloaded.Status != ItemStarted {
		t.Fatalf("original Started Item lost authoritative Store sequence: %+v %v", reloaded, err)
	}
}

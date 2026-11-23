package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func indexedDelegationToolFixture(t *testing.T) (*MemoryStore, Invocation, tools.ExecutionRequest) {
	t.Helper()
	store := NewMemoryStore()
	invocation := Invocation{
		ID: "root-inv", TurnID: "original-turn",
		ExecutionRefID: "root-run",
	}
	store.invocations[invocation.ID] = invocation
	store.invocationExecutionRefs[invocation.ExecutionRefID] = invocation.ID
	return store, invocation, tools.ExecutionRequest{
		RunID: invocation.ExecutionRefID,
		Call:  tools.Call{ID: "original-tool", ToolKey: DelegationToolKey, Arguments: json.RawMessage(`{}`)},
	}
}

func appendOriginalDelegationToolItem(t *testing.T, store *MemoryStore, inv Invocation, callID string, status ItemStatus, goal string, turnID, invocationID string) {
	t.Helper()
	id := delegationToolID(inv, callID)
	payload, err := json.Marshal(delegationItemPayload{
		DelegationID: id, MemberID: "researcher", Goal: goal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.AppendItem(t.Context(), Item{
		ID:     stableID("hid", inv.TurnID, id, string(status)),
		TurnID: turnID, Kind: ItemDelegation, Status: status,
		InvocationID: invocationID, Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIndexedPreparedDelegationPreservesChronologicalOriginal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		order []ItemStatus
		goal  string
		found bool
	}{
		{"waiting-first", []ItemStatus{ItemWaiting, ItemStarted}, "waiting-goal", true},
		{"started-first", []ItemStatus{ItemStarted, ItemWaiting}, "started-goal", true},
		{"waiting-only", []ItemStatus{ItemWaiting}, "waiting-goal", true},
		{"started-only", []ItemStatus{ItemStarted}, "started-goal", true},
		{"absent", nil, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, inv, request := indexedDelegationToolFixture(t)
			for i := range 2000 {
				if _, _, err := store.AppendItem(t.Context(), Item{
					ID: fmt.Sprintf("unrelated-%05d", i), TurnID: inv.TurnID,
					Kind: ItemArtifact, Status: ItemCompleted,
				}); err != nil {
					t.Fatal(err)
				}
			}
			for _, status := range tc.order {
				appendOriginalDelegationToolItem(t, store, inv, request.Call.ID, status,
					string(status)+"-goal", inv.TurnID, inv.ID)
			}
			spy := &indexOnlyParentStore{Store: store}
			runner := &Runner{store: spy}
			got, found, err := runner.preparedDelegationToolRequest(t.Context(), request)
			startedID := stableID("hid", inv.TurnID, delegationToolID(inv, request.Call.ID), string(ItemStarted))
			waitingID := stableID("hid", inv.TurnID, delegationToolID(inv, request.Call.ID), string(ItemWaiting))
			if err != nil || found != tc.found || (found && (got.Goal != tc.goal || got.MemberID != "researcher" || got.callID != request.Call.ID)) ||
				spy.listCalls != 0 || !reflect.DeepEqual(spy.itemCalls, []string{startedID, waitingID}) {
				t.Fatalf("original evidence not preserved: got=%+v found=%v err=%v scans=%d indexed=%v", got, found, err, spy.listCalls, spy.itemCalls)
			}
		})
	}
}

func TestIndexedDelegationRecoveryRejectsForeignAndCorruptItems(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		status               ItemStatus
		turnID, invocationID string
		malformedPayload     bool
	}{
		{"foreign-turn", ItemWaiting, "different-turn", "root-inv", false},
		{"foreign-invocation", ItemStarted, "original-turn", "unrelated-inv", false},
		{"malformed-payload", ItemStarted, "original-turn", "root-inv", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, inv, request := indexedDelegationToolFixture(t)
			appendOriginalDelegationToolItem(t, store, inv, request.Call.ID, tc.status,
				"goal", tc.turnID, tc.invocationID)
			if tc.malformedPayload {
				itemID := stableID("hid", inv.TurnID, delegationToolID(inv, request.Call.ID), string(tc.status))
				item := store.itemIDs[itemID]
				item.Payload = json.RawMessage(`{"memberID":`)
				store.itemIDs[itemID] = item
			}
			spy := &indexOnlyParentStore{Store: store}
			_, found, err := (&Runner{store: spy}).preparedDelegationToolRequest(t.Context(), request)
			if err == nil || found || spy.listCalls != 0 {
				t.Fatalf("malformed original was authorized: found=%v err=%v scanned=%d", found, err, spy.listCalls)
			}
		})
	}
}

// VerifyCommittedTool must use the immutable, globally indexed Started Item.
// Its full original ChildRunner route and receipt checks are separately
// exercised by TestPR82DelegationToolReceiptRequiresRouteAndFrozenIdentity.
func TestIndexedDelegationReceiptLookupDoesNotScanOrTrustForeignTurn(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(fmt.Sprintf("foreign=%v", foreign), func(t *testing.T) {
			store, inv, request := indexedDelegationToolFixture(t)
			for i := range 1500 {
				if _, _, err := store.AppendItem(t.Context(), Item{
					ID: fmt.Sprintf("unrelated-%05d", i), TurnID: inv.TurnID,
					Kind: ItemArtifact, Status: ItemCompleted,
				}); err != nil {
					t.Fatal(err)
				}
			}
			if foreign {
				appendOriginalDelegationToolItem(t, store, inv, request.Call.ID, ItemStarted, "goal", "another-turn", inv.ID)
			}
			spy := &indexOnlyParentStore{Store: store}
			handler := NewDelegationToolHandler()
			if err := handler.Bind(&Runner{store: spy}); err != nil {
				t.Fatal(err)
			}
			got, err := handler.VerifyCommittedTool(t.Context(), request)
			startedID := stableID("hid", inv.TurnID, delegationToolID(inv, request.Call.ID), string(ItemStarted))
			if !errors.Is(err, agent.ErrToolOutcomeUnknown) || got.Receipt.Disposition == "committed" ||
				spy.listCalls != 0 || !reflect.DeepEqual(spy.itemCalls, []string{startedID}) {
				t.Fatalf("unverified Tool receipt advanced: result=%+v err=%v scanned=%d reads=%v", got, err, spy.listCalls, spy.itemCalls)
			}
		})
	}
}

// A physical Store error cannot be mistaken for an absent frozen intent.
type failingIndexedDelegationStore struct {
	Store
	err error
}

func (store failingIndexedDelegationStore) GetItem(context.Context, string) (Item, error) {
	return Item{}, store.err
}

// The previous paginated reader rejected non-increasing sequence numbers.
// Two distinct original records with the same sequence are not a valid
// chronological choice and must remain a conflict, not a policy shortcut.
func TestIndexedPreparedDelegationRejectsDuplicatedSequence(t *testing.T) {
	store, inv, request := indexedDelegationToolFixture(t)
	appendOriginalDelegationToolItem(t, store, inv, request.Call.ID, ItemWaiting, "waiting", inv.TurnID, inv.ID)
	appendOriginalDelegationToolItem(t, store, inv, request.Call.ID, ItemStarted, "started", inv.TurnID, inv.ID)
	waitingID := stableID("hid", inv.TurnID, delegationToolID(inv, request.Call.ID), string(ItemWaiting))
	startedID := stableID("hid", inv.TurnID, delegationToolID(inv, request.Call.ID), string(ItemStarted))
	item := store.itemIDs[startedID]
	item.Seq = store.itemIDs[waitingID].Seq
	store.itemIDs[startedID] = item
	spy := &indexOnlyParentStore{Store: store}
	_, found, err := (&Runner{store: spy}).preparedDelegationToolRequest(t.Context(), request)
	if found || !errors.Is(err, ErrConflict) || spy.listCalls != 0 {
		t.Fatalf("duplicate immutable sequence was accepted: found=%v err=%v scanned=%d", found, err, spy.listCalls)
	}
}

func TestIndexedDelegationRecoveryPropagatesStoreFailure(t *testing.T) {
	store, _, request := indexedDelegationToolFixture(t)
	readErr := errors.New("original item index unavailable")
	runner := &Runner{store: failingIndexedDelegationStore{Store: store, err: readErr}}
	if _, _, err := runner.preparedDelegationToolRequest(t.Context(), request); !errors.Is(err, readErr) {
		t.Fatalf("read failure turned into missing intent: %v", err)
	}
	handler := NewDelegationToolHandler()
	if err := handler.Bind(runner); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.VerifyCommittedTool(t.Context(), request); !errors.Is(err, readErr) {
		t.Fatalf("read failure turned into completed Tool receipt: %v", err)
	}
}

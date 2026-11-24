package budget_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestModelReceiptQualificationSurvivesStorageAndRejectsRebinding(t *testing.T) {
	c := newLedger(t, budget.Limits{MaxTotalTokens: 100, MaxLLMCalls: 3})
	request := model.Request{RunID: "root", InvocationID: "original", MaxOutputTokens: 10}
	hash, err := model.PhysicalRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	res := budget.Reservation{ID: "model:original", RunID: "root", RequestHash: hash, PhysicalRequestHash: hash, Requested: budget.Usage{LLMCalls: 1, InputTokens: 20, OutputTokens: 10, TotalTokens: 30}}
	if _, err = c.Reserve(t.Context(), "turn", res, true); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Dispatch(t.Context(), "turn", res.ID); err != nil {
		t.Fatal(err)
	}
	response := model.Response{ResponseID: "receipt", Content: "unqualified", Usage: &model.Usage{InputTokens: 11, OutputTokens: 11}, ToolCalls: []tools.Call{{ID: "call", ToolKey: "lookup", Arguments: json.RawMessage(`{"x": "<>&"}`)}}}
	ledger, err := c.SettleModel(t.Context(), "turn", request, response, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(ledger)
	if err != nil {
		t.Fatal(err)
	}
	var reopened budget.Ledger
	if err := json.Unmarshal(encoded, &reopened); err != nil {
		t.Fatal(err)
	}
	original := reopened.Reservations[res.ID]
	got, err := budget.DecodeModelReceipt(original)
	if !model.IsResponseRejectedError(err) || !errors.Is(err, budget.ErrInvalidUsage) || model.ClassifiedErrorCode(err) != "model.post_generation_failed" || !reflect.DeepEqual(got, model.BillingOnlyResponse(response)) {
		t.Fatalf("lost immutable qualification: %+v %v", got, err)
	}
	if reopened.View("").Usage.OutputTokens != 11 {
		t.Fatal("actual overrun was not charged")
	}
	for _, test := range []struct {
		name   string
		change func(*budget.Reservation)
	}{
		{"foreign invocation", func(r *budget.Reservation) { r.ID = "model:foreign" }},
		{"foreign owner", func(r *budget.Reservation) { r.RunID = "foreign" }},
		{"logical hash", func(r *budget.Reservation) { r.RequestHash = "different" }},
		{"physical hash", func(r *budget.Reservation) { r.PhysicalRequestHash = "different" }},
		{"changed quota", func(r *budget.Reservation) { r.Requested.OutputTokens++ }},
		{"changed charge", func(r *budget.Reservation) { r.Usage.OutputTokens++ }},
		{"missing evidence", func(r *budget.Reservation) { r.Receipt = json.RawMessage(`{"responseID":"receipt"}`) }},
		{"removed rejection", func(r *budget.Reservation) {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(r.Receipt, &obj); err != nil {
				t.Fatal(err)
			}
			delete(obj, "rejected")
			r.Receipt, err = json.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := original
			test.change(&changed)
			if _, err := budget.DecodeModelReceipt(changed); !errors.Is(err, budget.ErrLedgerConflict) || model.IsResponseRejectedError(err) {
				t.Fatalf("unsealed receipt was treated as authoritative: %v", err)
			}
		})
	}
	if _, err := c.SettleModel(t.Context(), "turn", request, model.Response{ResponseID: "receipt", Content: "accepted", Usage: response.Usage}, nil); !errors.Is(err, budget.ErrLedgerConflict) {
		t.Fatalf("reclassified original settlement: %v", err)
	}
	after, err := c.Store.LoadBudget(t.Context(), "turn")
	if err != nil || !reflect.DeepEqual(ledger, after) {
		t.Fatal("receipt reads/reclassification changed the ledger")
	}
}

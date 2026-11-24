package harnesssqlite_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestOriginalModelQualificationAndOpaqueReceiptAcrossSQLiteReopen(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "rejected"}[reject], func(t *testing.T) {
			path, host := newHost(t)
			ctx := t.Context()
			c := budget.Coordinator{Store: host}
			if _, err := c.Ensure(ctx, "turn", budget.Limits{MaxLLMCalls: 1, MaxTotalTokens: 100}); err != nil {
				t.Fatal(err)
			}
			if _, err := c.RegisterRun(ctx, "turn", "run", budget.RunBudget{}); err != nil {
				t.Fatal(err)
			}
			request := model.Request{RunID: "run", InvocationID: "original", MaxOutputTokens: 10}
			hash, err := model.PhysicalRequestHash(request)
			if err != nil {
				t.Fatal(err)
			}
			res := budget.Reservation{ID: "model:original", RunID: "run", RequestHash: hash, PhysicalRequestHash: hash, Requested: budget.Usage{LLMCalls: 1, InputTokens: 20, OutputTokens: 10, TotalTokens: 30}}
			if _, err = c.Reserve(ctx, "turn", res, true); err != nil {
				t.Fatal(err)
			}
			if _, err = c.Dispatch(ctx, "turn", res.ID); err != nil {
				t.Fatal(err)
			}
			response := model.Response{ResponseID: "response", Content: "  original  ", Usage: &model.Usage{InputTokens: 10, OutputTokens: 3},
				ToolCalls:      []tools.Call{{ID: "call", ToolKey: "lookup", Arguments: json.RawMessage(`{"n": 9007199254740993, "x":"<>&"}`)}},
				ProviderReplay: &model.ProviderReplay{Provider: "fixture", Format: "signed", Data: json.RawMessage(`{"original": "<>&", "signature":"opaque"}`)}}
			if reject {
				response.Usage.OutputTokens = 11
			}
			settled, err := c.SettleModel(ctx, "turn", request, response, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = host.Close(); err != nil {
				t.Fatal(err)
			}
			host = reopenHost(t, path)
			reopened, err := host.LoadBudget(ctx, "turn")
			if err != nil || reopened.Revision != settled.Revision || reopened.View("").Usage.OutputTokens != response.Usage.OutputTokens {
				t.Fatalf("original charge lost: %+v %v", reopened, err)
			}
			actual, receiptErr := budget.DecodeModelReceipt(reopened.Reservations[res.ID])
			if reject {
				if !model.IsResponseRejectedError(receiptErr) || !errors.Is(receiptErr, budget.ErrInvalidUsage) || !reflect.DeepEqual(actual, model.BillingOnlyResponse(response)) {
					t.Fatalf("reopened rejection was promoted: %+v %v", actual, receiptErr)
				}
			} else if receiptErr != nil || !reflect.DeepEqual(model.CloneResponse(actual), model.CloneResponse(response)) {
				t.Fatalf("reopened receipt altered original bytes: %+v %v", actual, receiptErr)
			}
		})
	}
}

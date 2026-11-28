package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type rejectedHostedCatalog struct{}

func (rejectedHostedCatalog) Resolve(_ context.Context, key, _ string) (model.HostedTool, bool, error) {
	return model.HostedTool{Key: key, DefinitionVersion: "v1"}, key == "native.search", nil
}

// Both native stores must account original provider effects before removing
// rejected execution payloads, including unidentifiable effects that cannot settle.
func RejectedHostedReceiptPreservesOriginalQuota(t *testing.T, factory func(*testing.T) NativeAuthorityFixture) {
	for _, anonymous := range []bool{false, true} {
		name := "identified"
		if anonymous {
			name = "anonymous"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			f := factory(t)
			runs := f.Kernel.(nativeAuthorityKernelPorts)
			original := model.Response{
				ResponseID: "rejected-original",
				Content:    "unauthorized assistant content",
				Usage:      &model.Usage{InputTokens: 7, OutputTokens: 3},
				HostedToolCalls: []model.HostedToolCall{
					{ID: "completed-native", ToolKey: "native.search", Status: "completed"},
					{ID: "pending-native", ToolKey: "native.search", Status: "in_progress"},
				},
			}
			if anonymous {
				original.HostedToolCalls[1].ID = ""
			}
			client := &nativeAuthorityModel{generate: func(r model.Request) (model.Response, error) {
				if len(r.HostedTools) != 1 || r.HostedToolCallLimit == nil || *r.HostedToolCallLimit != 2 {
					t.Fatalf("original physical quota not frozen: %+v", r)
				}
				return original, model.NewResponseRejectedError(model.NewClassifiedError(errors.New("semantic rejection"), "model.post_generation_failed"))
			}}
			options := nativeAuthorityOptions(t, runs, f.Host, client)
			options.HostedTools = rejectedHostedCatalog{}
			options.Session.Config.HostedToolGrants = []agent.HostedToolGrant{{Key: "native.search", DefinitionVersion: "v1"}}
			options.Session.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 3, MaxToolCalls: 2, MaxHostedToolCalls: 2}
			session, err := harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			_, promptErr := session.Prompt(ctx, "rejected-hosted", "original request")
			if promptErr == nil || len(client.requests) != 1 {
				t.Fatalf("expected one rejected physical call: %v calls=%d", promptErr, len(client.requests))
			}
			if f.Reopen != nil {
				k, _, _ := f.Reopen()
				runs = k.(nativeAuthorityKernelPorts)
			}
			snapshot, err := runs.Load(ctx, client.requests[0].RunID)
			if err != nil {
				t.Fatal(err)
			}
			l, err := runs.LoadBudget(ctx, snapshot.Run.BudgetScopeID)
			if err != nil {
				t.Fatalf("load original ledger (prompt=%v): %v", promptErr, err)
			}
			v := l.View("")
			wantUsage, wantReserved := 1, 1
			wantModelUsage, wantModelStatus := 1, budget.ReservationSettled
			if anonymous {
				wantUsage, wantReserved, wantModelUsage, wantModelStatus = 0, 2, 0, budget.ReservationHeld
			}
			if v.Usage.HostedToolCalls != wantUsage || v.Usage.ToolCalls != wantUsage || v.Reserved.HostedToolCalls != wantReserved || v.Reserved.ToolCalls != wantReserved ||
				v.Usage.LLMCalls != wantModelUsage || !v.UnknownUsage || len(client.requests) != 1 {
				t.Fatalf("original quota released: usage=%+v reserved=%+v unknown=%v calls=%d prompt=%v", v.Usage, v.Reserved, v.UnknownUsage, len(client.requests), promptErr)
			}
			res := l.Reservations["model:"+client.requests[0].InvocationID]
			if res.Status != wantModelStatus || !res.Dispatched {
				t.Fatalf("original model receipt: %+v", res)
			}
			hosted := map[string]budget.ReservationStatus{}
			for id, r := range l.Reservations {
				if strings.HasPrefix(id, "hosted:") {
					var call model.HostedToolCall
					if json.Unmarshal(r.Receipt, &call) != nil || call.ToolKey != "native.search" || !r.Dispatched || r.Requested.HostedToolCalls != 1 {
						t.Fatalf("invalid hosted receipt: %+v", r)
					}
					hosted[call.ID] = r.Status
				}
			}
			if anonymous {
				if len(hosted) != 0 {
					t.Fatalf("invented anonymous receipt: %v", hosted)
				}
				return
			}
			if len(hosted) != 2 || hosted["completed-native"] != budget.ReservationSettled || hosted["pending-native"] != budget.ReservationHeld {
				t.Fatalf("wrong original hosted receipts: %v", hosted)
			}
			billing := model.BillingOnlyResponse(original)
			receipt, receiptErr := budget.DecodeModelReceipt(res)
			if !model.IsResponseRejectedError(receiptErr) || !reflect.DeepEqual(receipt, billing) {
				t.Fatalf("rejected ledger qualification changed: %+v %v", receipt, receiptErr)
			}
			state, err := agent.ViewState(snapshot)
			if err != nil || len(state.ModelInvocations) != 1 {
				t.Fatalf("original model state: %+v %v", state.ModelInvocations, err)
			}
			inv := state.ModelInvocations[0]
			if inv.ErrorCode != "model.post_generation_failed" || !reflect.DeepEqual(model.CloneResponse(inv.Response), model.CloneResponse(billing)) {
				t.Fatalf("rejected model payload was authorized: %+v", inv)
			}
		})
	}
}

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// SamplingPlan freezes only the SharedBudget restrictions and numeric Context
// evidence for an admitted logical ModelInvocation. The original transcript,
// Catalog, Provider binding and InvocationID remain in Request, exactly once.
// A plan grants no physical permission: the ledger must still reserve it.
type SamplingPlan struct {
	LedgerID            string                          `json:"ledgerID"`
	RequestHash         string                          `json:"requestHash"`
	Requested           budget.Usage                    `json:"requested"`
	MaxOutputTokens     int64                           `json:"maxOutputTokens"`
	HostedToolCallLimit *int                            `json:"hostedToolCallLimit,omitempty"`
	OmitLocalTools      bool                            `json:"omitLocalTools,omitempty"`
	RequireToolCall     bool                            `json:"requireToolCall,omitempty"`
	Window              runtimecontext.ModelWindow      `json:"window"`
	Policy              runtimecontext.Policy           `json:"policy"`
	Accounting          runtimecontext.FrozenAccounting `json:"accounting"`
	Assessment          runtimecontext.Assessment       `json:"assessment"`
}

func cloneSamplingPlan(plan *SamplingPlan) *SamplingPlan {
	if plan == nil {
		return nil
	}
	copy := *plan
	if plan.HostedToolCallLimit != nil {
		limit := *plan.HostedToolCallLimit
		copy.HostedToolCallLimit = &limit
	}
	return &copy
}

func samplingRequestHash(request model.Request) (string, error) {
	raw, err := model.MarshalRequestForInputCount(model.CloneRequest(request))
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}

func (plan SamplingPlan) request(original model.Request) model.Request {
	request := model.CloneRequest(original)
	request.MaxOutputTokens = plan.MaxOutputTokens
	request.HostedToolCallLimit = cloneSamplingPlan(&plan).HostedToolCallLimit
	request.RequireToolCall = plan.RequireToolCall
	if plan.OmitLocalTools {
		request.Tools = nil
	}
	return model.CloneRequest(request)
}

func (plan SamplingPlan) reservation(original model.Request) budget.Reservation {
	return budget.Reservation{ID: "model:" + original.InvocationID, RunID: original.RunID,
		RequestHash: kernel.ManagedDigest(original), Requested: plan.Requested}
}

func (plan SamplingPlan) valid(original model.Request) bool {
	original = model.CloneRequest(original)
	if strings.TrimSpace(plan.LedgerID) == "" || strings.TrimSpace(plan.LedgerID) != plan.LedgerID ||
		!budget.ValidUsage(plan.Requested) || plan.Requested.LLMCalls != 1 ||
		plan.Requested.ToolCalls != plan.Requested.HostedToolCalls ||
		plan.Requested != (budget.Usage{LLMCalls: 1, ToolCalls: plan.Requested.ToolCalls,
			HostedToolCalls: plan.Requested.HostedToolCalls, InputTokens: plan.Requested.InputTokens,
			OutputTokens: plan.Requested.OutputTokens, TotalTokens: plan.Requested.TotalTokens}) ||
		(plan.Requested.InputTokens == 0) != (plan.Requested.OutputTokens == 0) ||
		(plan.Requested.OutputTokens > 0 && plan.Requested.OutputTokens != plan.MaxOutputTokens) ||
		(plan.Requested.HostedToolCalls > 0 && (plan.HostedToolCallLimit == nil ||
			*plan.HostedToolCallLimit != plan.Requested.HostedToolCalls || len(original.HostedTools) == 0)) ||
		!plan.Accounting.MatchesWindow(plan.Window) || !runtimecontext.WithinHardBudget(plan.Assessment, plan.Policy) {
		return false
	}
	request := plan.request(original)
	if !validDispatchModelRequest(original, request) {
		return false
	}
	hash, err := samplingRequestHash(request)
	if err != nil || hash != plan.RequestHash {
		return false
	}
	size, err := plan.Accounting.SerializedBytes(request)
	hard, hardErr := runtimecontext.EffectiveInputLimit(plan.Window, plan.Policy.MaxInputTokens)
	return err == nil && hardErr == nil && size == plan.Assessment.SerializedBytes && hard == plan.Assessment.HardInputTokens
}

func newSamplingPlan(ledger budget.Ledger, original, request model.Request, reservation budget.Reservation,
	window runtimecontext.ModelWindow, policy runtimecontext.Policy, frame runtimecontext.FrozenAccounting, assessment runtimecontext.Assessment) (*SamplingPlan, error) {
	hash, err := samplingRequestHash(request)
	if err != nil {
		return nil, err
	}
	plan := &SamplingPlan{LedgerID: ledger.ID, RequestHash: hash, Requested: reservation.Requested,
		MaxOutputTokens: request.MaxOutputTokens, HostedToolCallLimit: request.HostedToolCallLimit,
		OmitLocalTools: len(original.Tools) > 0 && len(request.Tools) == 0, RequireToolCall: request.RequireToolCall,
		Window: window, Policy: runtimecontext.NormalizePolicy(policy), Accounting: frame, Assessment: assessment}
	want := plan.reservation(original)
	if reservation.ID != want.ID || reservation.RunID != want.RunID || reservation.RequestHash != want.RequestHash ||
		reservation.Status != "" || reservation.Active || reservation.Dispatched || !plan.valid(original) ||
		!reflect.DeepEqual(plan.request(original), model.CloneRequest(request)) {
		return nil, ErrInvalidRequest
	}
	return cloneSamplingPlan(plan), nil
}

type samplingPlanKey struct{}

func samplingPlanFrom(ctx context.Context) *SamplingPlan {
	plan, _ := ctx.Value(samplingPlanKey{}).(*SamplingPlan)
	return plan
}

// The same selected model middleware owns both candidate planning and
// reservation. Run/Tool-only adapters do not narrow a sampling request.
func selectedSamplingBudget(dependencies Dependencies) *BoundedBudgetAdapter {
	for _, layer := range dependencies.ModelMiddleware {
		if adapter, ok := layer.(*BoundedBudgetAdapter); ok {
			return adapter
		}
	}
	return nil
}

package agent

import (
	"reflect"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// SamplingPlan freezes the physical request projection of a logical invocation.
// SharedBudget and Context evidence are present only when those authorities
// participated. The original transcript, Catalog and binding are stored once.
// A plan grants no physical permission; execution and quota fences still apply.
type SamplingPlan struct {
	LedgerID            string                          `json:"ledgerID,omitempty"`
	RequestHash         string                          `json:"requestHash"`
	Requested           budget.Usage                    `json:"requested,omitzero"`
	MaxOutputTokens     int64                           `json:"maxOutputTokens"`
	HostedToolCallLimit *int                            `json:"hostedToolCallLimit,omitempty"`
	LocalToolKeys       []string                        `json:"localToolKeys,omitempty"`
	HostedToolKeys      []string                        `json:"hostedToolKeys,omitempty"`
	OmitHostedTools     bool                            `json:"omitHostedTools,omitempty"`
	OmitLocalTools      bool                            `json:"omitLocalTools,omitempty"`
	RequireToolCall     bool                            `json:"requireToolCall,omitempty"`
	Window              runtimecontext.ModelWindow      `json:"window,omitzero"`
	Policy              runtimecontext.Policy           `json:"policy,omitzero"`
	Accounting          runtimecontext.FrozenAccounting `json:"accounting,omitzero"`
	Assessment          runtimecontext.Assessment       `json:"assessment,omitzero"`
}

func cloneSamplingPlan(plan *SamplingPlan) *SamplingPlan {
	if plan == nil {
		return nil
	}
	copy := *plan
	copy.LocalToolKeys = append([]string(nil), plan.LocalToolKeys...)
	copy.HostedToolKeys = append([]string(nil), plan.HostedToolKeys...)
	if plan.HostedToolCallLimit != nil {
		limit := *plan.HostedToolCallLimit
		copy.HostedToolCallLimit = &limit
	}
	return &copy
}

// Tool keys encode an ordered subset, without copying original schemas or
// provider replay. A nil list means unchanged; an explicit omit means empty.
func samplingToolKeys[T any](original, physical []T, key func(T) string) ([]string, bool) {
	if reflect.DeepEqual(original, physical) {
		return nil, false
	}
	if len(physical) == 0 {
		return nil, true
	}
	keys := make([]string, len(physical))
	for i, tool := range physical {
		keys[i] = key(tool)
	}
	return keys, false
}
func samplingTools[T any](original []T, keys []string, omit bool, key func(T) string) []T {
	if omit {
		return nil
	}
	if len(keys) == 0 {
		return original
	}
	result := make([]T, 0, len(keys))
	for _, k := range keys {
		for _, tool := range original {
			if key(tool) == k {
				result = append(result, tool)
				break
			}
		}
	}
	return result
}
func localSamplingKey(t tools.Definition) string  { return t.Key }
func hostedSamplingKey(t model.HostedTool) string { return t.Key }

func (plan SamplingPlan) request(original model.Request) model.Request {
	request := original
	request.MaxOutputTokens = plan.MaxOutputTokens
	request.HostedToolCallLimit = plan.HostedToolCallLimit
	request.RequireToolCall = plan.RequireToolCall
	request.Tools = samplingTools(request.Tools, plan.LocalToolKeys, plan.OmitLocalTools, localSamplingKey)
	request.HostedTools = samplingTools(request.HostedTools, plan.HostedToolKeys, plan.OmitHostedTools, hostedSamplingKey)
	return model.CloneRequest(request)
}

func newPhysicalSamplingPlan(original, request model.Request) (*SamplingPlan, error) {
	original, request = model.CloneRequest(original), model.CloneRequest(request)
	if !validDispatchModelRequest(original, request) {
		return nil, ErrInvalidRequest
	}
	hash, err := model.PhysicalRequestHash(request)
	if err != nil {
		return nil, err
	}
	plan := &SamplingPlan{RequestHash: hash, MaxOutputTokens: request.MaxOutputTokens,
		HostedToolCallLimit: request.HostedToolCallLimit, RequireToolCall: request.RequireToolCall}
	plan.LocalToolKeys, plan.OmitLocalTools = samplingToolKeys(original.Tools, request.Tools, localSamplingKey)
	plan.HostedToolKeys, plan.OmitHostedTools = samplingToolKeys(original.HostedTools, request.HostedTools, hostedSamplingKey)
	if !reflect.DeepEqual(plan.request(original), request) {
		return nil, ErrInvalidRequest
	}
	return cloneSamplingPlan(plan), nil
}

func (plan SamplingPlan) reservation(original model.Request) (budget.Reservation, error) {
	hash, err := model.PhysicalRequestHash(original)
	if err != nil {
		return budget.Reservation{}, err
	}
	return budget.Reservation{ID: "model:" + original.InvocationID, RunID: original.RunID, RequestHash: hash, PhysicalRequestHash: plan.RequestHash, Requested: plan.Requested}, nil
}

func (plan SamplingPlan) valid(original model.Request) bool {
	original = model.CloneRequest(original)
	request := plan.request(original)
	canonical, err := newPhysicalSamplingPlan(original, request)
	if err != nil || canonical.RequestHash != plan.RequestHash ||
		!reflect.DeepEqual(canonical.LocalToolKeys, plan.LocalToolKeys) || canonical.OmitLocalTools != plan.OmitLocalTools ||
		!reflect.DeepEqual(canonical.HostedToolKeys, plan.HostedToolKeys) || canonical.OmitHostedTools != plan.OmitHostedTools {
		return false
	}
	if plan.LedgerID == "" {
		if plan.Requested != (budget.Usage{}) {
			return false
		}
	} else if strings.TrimSpace(plan.LedgerID) != plan.LedgerID ||
		!budget.ValidUsage(plan.Requested) || plan.Requested.LLMCalls != 1 ||
		plan.Requested.ToolCalls != plan.Requested.HostedToolCalls ||
		plan.Requested != (budget.Usage{LLMCalls: 1, ToolCalls: plan.Requested.ToolCalls,
			HostedToolCalls: plan.Requested.HostedToolCalls, InputTokens: plan.Requested.InputTokens,
			OutputTokens: plan.Requested.OutputTokens, TotalTokens: plan.Requested.TotalTokens}) ||
		(plan.Requested.InputTokens == 0) != (plan.Requested.OutputTokens == 0) ||
		(plan.Requested.OutputTokens > 0 && plan.Requested.OutputTokens != plan.MaxOutputTokens) ||
		(plan.Requested.HostedToolCalls > 0 && (plan.HostedToolCallLimit == nil ||
			*plan.HostedToolCallLimit != plan.Requested.HostedToolCalls || len(request.HostedTools) == 0)) {
		return false
	}
	if plan.Window.ContextTokens == 0 {
		return plan.Window == (runtimecontext.ModelWindow{}) && plan.Policy == (runtimecontext.Policy{}) &&
			plan.Accounting == (runtimecontext.FrozenAccounting{}) && plan.Assessment == (runtimecontext.Assessment{})
	}
	if !plan.Accounting.MatchesWindow(plan.Window) || !runtimecontext.WithinHardBudget(plan.Assessment, plan.Policy) {
		return false
	}
	size, err := plan.Accounting.SerializedBytes(request)
	hard, hardErr := runtimecontext.EffectiveInputLimit(plan.Window, plan.Policy.MaxInputTokens)
	return err == nil && hardErr == nil && size == plan.Assessment.SerializedBytes && hard == plan.Assessment.HardInputTokens
}

func validFreshSamplingReservation(original model.Request, reservation budget.Reservation) bool {
	hash, err := model.PhysicalRequestHash(original)
	return err == nil && reservation.ID == "model:"+original.InvocationID && reservation.RunID == original.RunID &&
		reservation.RequestHash == hash && reservation.PhysicalRequestHash == "" && reservation.Status == "" &&
		!reservation.Active && !reservation.Dispatched && reservation.Dimension == "" &&
		reservation.Usage == (budget.Usage{}) && len(reservation.Receipt) == 0 &&
		reservation.Requested.LLMCalls == 1 && budget.ValidUsage(reservation.Requested)
}

func (plan *SamplingPlan) bindContext(window runtimecontext.ModelWindow, policy runtimecontext.Policy, frame runtimecontext.FrozenAccounting, assessment runtimecontext.Assessment) {
	plan.Window, plan.Policy, plan.Accounting, plan.Assessment = window, runtimecontext.NormalizePolicy(policy), frame, assessment
}

func newSamplingPlan(ledger budget.Ledger, original, request model.Request, reservation budget.Reservation,
	window runtimecontext.ModelWindow, policy runtimecontext.Policy, frame runtimecontext.FrozenAccounting, assessment runtimecontext.Assessment) (*SamplingPlan, error) {
	plan, err := newPhysicalSamplingPlan(original, request)
	if err != nil {
		return nil, err
	}
	plan.LedgerID, plan.Requested = ledger.ID, reservation.Requested
	if window.ContextTokens > 0 {
		plan.bindContext(window, policy, frame, assessment)
	}
	if ledger.ID == "" || !validFreshSamplingReservation(original, reservation) || !plan.valid(original) {
		return nil, ErrInvalidRequest
	}
	return cloneSamplingPlan(plan), nil
}

// The same selected model middleware owns both candidate planning and
// reservation. Run/Tool-only adapters do not narrow a sampling request.
func selectedSamplingBudget(dependencies Dependencies) *BoundedBudgetAdapter {
	if dependencies.BoundedBudget != nil {
		return dependencies.BoundedBudget
	}
	for _, layer := range dependencies.ModelMiddleware {
		if adapter, ok := layer.(*BoundedBudgetAdapter); ok {
			return adapter
		}
	}
	return nil
}

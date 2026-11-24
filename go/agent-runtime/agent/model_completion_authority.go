package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// modelCompletionAuthority binds one invocation to its first trusted physical
// or settled-replay receipt. Ordinary validation never registers a source.
type modelCompletionAuthority struct {
	mu            sync.Mutex
	named         bool
	request       model.Request
	plan          *SamplingPlan
	duplicate     func(string) bool
	observed      bool
	verified      bool
	invalid       bool
	id            string
	responseHash  string
	billingHash   string
	rejected      bool
	rejectionCode string
	settled       bool
}

// One invocation-scoped carrier keeps the plan, receipt authority and
// no-send nonce together across every Model middleware boundary.
type modelCallScope struct {
	completion *modelCompletionAuthority
	denial     *modelDenialScope
}

func modelCallFrom(ctx context.Context) *modelCallScope {
	call, _ := callbackscope.ModelCall(ctx).(*modelCallScope)
	return call
}
func (call *modelCallScope) samplingPlan() *SamplingPlan {
	if call == nil || call.completion == nil {
		return nil
	}
	return call.completion.plan
}

func hasModelCompletionReceipt(response model.Response, err error) bool {
	return err == nil || errors.Is(err, ErrModelLateResult) ||
		model.IsResponseRejectedError(err) && response.Usage != nil && model.ValidUsage(response.Usage)
}

func (authority *modelCompletionAuthority) reject() (model.Response, error) {
	authority.invalid = true
	return model.Response{}, ErrInvalidModelResponse
}

func (authority *modelCompletionAuthority) record(response model.Response, err error) (model.Response, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	return authority.recordLocked(response, err)
}

func (authority *modelCompletionAuthority) recordLocked(response model.Response, err error) (model.Response, error) {
	if authority.invalid || authority.observed {
		return authority.reject()
	}
	authority.observed = true
	if !hasModelCompletionReceipt(response, err) {
		return response, err
	}
	if authority.named && (response.ResponseID == "" || strings.TrimSpace(response.ResponseID) != response.ResponseID || authority.duplicate(response.ResponseID)) {
		return authority.reject()
	}
	digest, digestErr := model.ResponseEvidenceHash(response)
	billing, billingErr := model.ResponseEvidenceHash(model.BillingOnlyResponse(response))
	if digestErr != nil || billingErr != nil {
		return authority.reject()
	}
	authority.verified = true
	authority.id = response.ResponseID
	authority.responseHash, authority.billingHash = digest, billing
	authority.rejected = model.IsResponseRejectedError(err)
	if authority.rejected {
		authority.rejectionCode = model.ClassifiedErrorCode(err)
	}
	return response, err
}

func (authority *modelCompletionAuthority) replay(request model.Request, response model.Response, err error) (model.Response, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.plan == nil || !reflect.DeepEqual(authority.plan.request(authority.request), model.CloneRequest(request)) {
		return authority.reject()
	}
	return authority.recordLocked(response, err)
}

func (authority *modelCompletionAuthority) validate(response model.Response, err error) (model.Response, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	return authority.validateLocked(response, err, false)
}

// The native budget driver alone may latch the result of its committed
// settlement, including a new rejection. Outer layers can neither promote
// that result nor supply a successful result without the required settlement.
func (authority *modelCompletionAuthority) settle(response model.Response, err error) (model.Response, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	response, err = authority.validateLocked(response, err, true)
	if !authority.invalid && hasModelCompletionReceipt(response, err) {
		authority.settled = true
		if model.IsResponseRejectedError(err) {
			authority.rejected = true
			authority.rejectionCode = model.ClassifiedErrorCode(err)
		}
	}
	return response, err
}
func (authority *modelCompletionAuthority) validateFinal(response model.Response, err error) (model.Response, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.plan != nil && authority.plan.LedgerID != "" && !authority.settled && hasModelCompletionReceipt(response, err) {
		return authority.reject()
	}
	return authority.validateLocked(response, err, false)
}
func (authority *modelCompletionAuthority) validateLocked(response model.Response, err error, settlement bool) (model.Response, error) {
	if authority.invalid {
		return model.Response{}, ErrInvalidModelResponse
	}
	if !hasModelCompletionReceipt(response, err) {
		return response, err
	}
	rejected := model.IsResponseRejectedError(err)
	digest, digestErr := model.ResponseEvidenceHash(response)
	if !authority.verified || response.ResponseID != authority.id || digestErr != nil ||
		(digest != authority.responseHash && (!rejected || digest != authority.billingHash)) ||
		(authority.rejected && (!rejected || (!settlement && model.ClassifiedErrorCode(err) != authority.rejectionCode))) ||
		(authority.named && authority.duplicate(response.ResponseID)) {
		return authority.reject()
	}
	return response, err
}

package agent

import (
	"errors"
	"strings"
	"sync"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// modelCompletionAuthority binds one invocation to its first trusted physical
// or settled-replay receipt. Ordinary validation never registers a source.
type modelCompletionAuthority struct {
	mu        sync.Mutex
	named     bool
	request   model.Request
	duplicate func(string) bool
	observed  bool
	verified  bool
	invalid   bool
	id        string
	usage     *model.Usage
	rejected  bool
}

func hasModelCompletionReceipt(response model.Response, err error) bool {
	return err == nil || errors.Is(err, ErrModelLateResult) ||
		model.IsResponseRejectedError(err) && response.Usage != nil && validModelUsage(response.Usage)
}

func sameModelCompletionUsage(left, right *model.Usage) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
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
	if !authority.named {
		return response, err
	}
	if authority.invalid || authority.observed {
		return authority.reject()
	}
	authority.observed = true
	if !hasModelCompletionReceipt(response, err) {
		return response, err
	}
	if response.ResponseID == "" || strings.TrimSpace(response.ResponseID) != response.ResponseID ||
		authority.duplicate(response.ResponseID) {
		return authority.reject()
	}
	authority.verified = true
	authority.id = response.ResponseID
	authority.usage = model.CloneUsage(response.Usage)
	authority.rejected = model.IsResponseRejectedError(err)
	return response, err
}

func (authority *modelCompletionAuthority) replay(request model.Request, response model.Response, err error) (model.Response, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if request.RunID != authority.request.RunID || request.InvocationID != authority.request.InvocationID ||
		!validDispatchModelRequest(authority.request, request) {
		return authority.reject()
	}
	return authority.recordLocked(response, err)
}

func (authority *modelCompletionAuthority) validate(response model.Response, err error) (model.Response, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if !authority.named {
		return response, err
	}
	// Once identity or receipt evidence conflicts, swallowing/reclassifying
	// the error cannot restore completion authority.
	if authority.invalid {
		return model.Response{}, ErrInvalidModelResponse
	}
	if !hasModelCompletionReceipt(response, err) {
		return response, err
	}
	if !authority.verified || response.ResponseID != authority.id ||
		!sameModelCompletionUsage(response.Usage, authority.usage) ||
		authority.rejected && !model.IsResponseRejectedError(err) ||
		authority.duplicate(response.ResponseID) {
		return authority.reject()
	}
	return response, err
}

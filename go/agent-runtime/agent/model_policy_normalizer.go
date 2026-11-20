package agent

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// ModelPolicyNormalizer is an optional independent Host callback. Its
// returned binding is typed, but the function itself can detach effects;
// therefore a Runner configured with it does not obtain native deferred
// cancellation custody from the settled Agent callback alone.
type ModelPolicyNormalizer func(context.Context, model.Request) (model.PolicyBinding, error)

// SelectedModelPolicyClient owns normalization in the same selected Provider
// lifetime as Generate. It is not a separately configured Host callback, and
// its binding is sealed with the original logical Model invocation. Like
// Generate, implementations must not detach untracked work after returning.
// Pending invocation recovery never calls this operation a second time.
type SelectedModelPolicyClient interface {
	model.Client
	NormalizeModelPolicy(context.Context, model.Request) (model.PolicyBinding, error)
}

func (runner *Runner) normalizeModelPolicy(ctx context.Context, request model.Request) (model.Request, error) {
	if runner.modelPolicyNormalizer == nil {
		return request, nil
	}
	policy, err := runner.modelPolicyNormalizer(ctx, model.CloneRequest(request))
	runner.noteRootCallbackFailure(err)
	if err != nil {
		return model.Request{}, err
	}
	request.ModelOptions = cloneRawJSON(policy.ModelOptions)
	request.BindingRef = policy.BindingRef
	if model.ValidateRequestBinding(request) != nil || (len(request.ModelOptions) > 0 && jsoncontract.ValidateUniqueJSONObject(request.ModelOptions) != nil) {
		return model.Request{}, ErrInvalidRequest
	}
	return request, nil
}

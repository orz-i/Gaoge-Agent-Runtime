package agent

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// ModelPolicyNormalizer applies Host model-option policy once before the logical
// invocation is sealed. Its typed result cannot change model, tools or budgets.
// Pending invocation recovery never invokes this callback again.
type ModelPolicyNormalizer func(context.Context, model.Request) (model.PolicyBinding, error)

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

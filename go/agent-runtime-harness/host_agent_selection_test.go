package harness_test

import (
	"context"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Test-only Host adapter: this policy is deliberately not a Runtime Agent
// catalog. Real Hosts resolve names, credentials and authorized execution
// scopes before returning the immutable Agent request.
type hostSelectedAgentPolicy struct {
	MemberID       string
	MemberRevision string
	Agent          agent.StartRequest
}

func (p hostSelectedAgentPolicy) PrepareDelegation(_ context.Context, _ tools.ExecutionRequest, request harness.DelegateRequest) (harness.DelegateRequest, error) {
	if request.MemberID != p.MemberID {
		return harness.DelegateRequest{}, harness.ErrInvalidRequest
	}
	request.MemberRevision = p.MemberRevision
	request.Agent = p.Agent
	return request, nil
}

type hostDelegatePolicyFunc func(context.Context, tools.ExecutionRequest, harness.DelegateRequest) (harness.DelegateRequest, error)

func (f hostDelegatePolicyFunc) PrepareDelegation(ctx context.Context, execution tools.ExecutionRequest, request harness.DelegateRequest) (harness.DelegateRequest, error) {
	return f(ctx, execution, request)
}

// Test-only explicit Host allow policy for fixtures that test execution rather
// than member selection. Production must supply its own authorization policy.
type testDelegationPolicyChain []harness.DelegationToolPolicy

func (policies testDelegationPolicyChain) PrepareDelegation(ctx context.Context, execution tools.ExecutionRequest, request harness.DelegateRequest) (harness.DelegateRequest, error) {
	var err error
	for _, policy := range policies {
		request, err = policy.PrepareDelegation(ctx, execution, request)
		if err != nil {
			return harness.DelegateRequest{}, err
		}
	}
	return request, nil
}

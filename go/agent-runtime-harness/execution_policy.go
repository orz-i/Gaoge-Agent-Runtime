package harness

import "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"

const (
	defaultMaxChildRuns       = 32
	defaultMaxConcurrentRuns  = 8
	defaultMaxDelegationDepth = 4
	maxDelegationDepth        = 32
)

// ExecutionPolicy is deployment-owned Harness policy for new Turns. It is
// resolved when Runner is constructed and then frozen into each newly created
// Turn configuration. Persisted Turns are never rewritten during recovery.
type ExecutionPolicy struct {
	SharedBudget       budget.Limits
	MaxDelegationDepth int
}

// DefaultExecutionPolicy returns the zero-config Harness safety policy. Shared
// model/Tool/token dimensions remain unlimited; the defaults only bound runaway
// topology and concurrency.
func DefaultExecutionPolicy() ExecutionPolicy {
	return ExecutionPolicy{
		SharedBudget: budget.Limits{
			MaxChildRuns:      defaultMaxChildRuns,
			MaxConcurrentRuns: defaultMaxConcurrentRuns,
		},
		MaxDelegationDepth: defaultMaxDelegationDepth,
	}
}

// Resolve fills zero deployment values from Runtime defaults. Nonzero deployment
// values deliberately replace defaults because this policy is the hard owner,
// not a subordinate request.
func (policy ExecutionPolicy) Resolve() (ExecutionPolicy, error) {
	defaults := DefaultExecutionPolicy()
	shared, err := budget.ResolveLimits(defaults.SharedBudget, policy.SharedBudget)
	if err != nil {
		return ExecutionPolicy{}, ErrInvalidRequest
	}
	depth := policy.MaxDelegationDepth
	if depth == 0 {
		depth = defaults.MaxDelegationDepth
	}
	if depth < 1 || depth > maxDelegationDepth {
		return ExecutionPolicy{}, ErrInvalidRequest
	}
	return ExecutionPolicy{SharedBudget: shared, MaxDelegationDepth: depth}, nil
}

func (runner *Runner) resolveConfigExecutionPolicy(value ConfigSnapshot) (ConfigSnapshot, error) {
	if runner == nil {
		return ConfigSnapshot{}, ErrInvalidRequest
	}
	requestedDepth := value.DelegationPolicy.MaxDepth
	if requestedDepth < 0 || requestedDepth > maxDelegationDepth {
		return ConfigSnapshot{}, ErrInvalidRequest
	}
	if requestedDepth == 0 || requestedDepth > runner.execution.MaxDelegationDepth {
		requestedDepth = runner.execution.MaxDelegationDepth
	}
	value.DelegationPolicy.MaxDepth = requestedDepth

	if runner.budgets == nil {
		if value.SharedBudget != nil {
			return ConfigSnapshot{}, ErrInvalidRequest
		}
		return value, nil
	}
	requestedBudget := budget.Limits{}
	if value.SharedBudget != nil {
		requestedBudget = *value.SharedBudget
	}
	shared, err := budget.TightenLimits(runner.execution.SharedBudget, requestedBudget)
	if err != nil {
		return ConfigSnapshot{}, ErrInvalidRequest
	}
	value.SharedBudget = &shared
	return value, nil
}

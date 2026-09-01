package workflow

import runtimebudget "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"

// RuntimeBudget projects the Workflow dimensions that have the same meaning as
// the common runtime vocabulary. Workflow-specific activation/effect/segment
// limits remain owned by Workflow and are intentionally not flattened here.
func RuntimeBudget(view View) runtimebudget.Snapshot {
	childRunIDs := make(map[string]struct{})
	for _, effect := range view.Effects {
		if effect.ChildRunID != "" {
			childRunIDs[effect.ChildRunID] = struct{}{}
		}
	}
	return runtimebudget.Snapshot{
		Limits: runtimebudget.Limits{
			MaxStateBytes: view.Definition.Limits.MaxStateBytes,
			MaxCostUnits:  view.Definition.Policy.MaxCostUnits,
		},
		Usage: runtimebudget.Usage{
			StateBytes: view.Budget.StateBytes,
			ChildRuns:  len(childRunIDs),
			CostUnits:  view.Budget.CostUnitsUsed,
		},
	}
}

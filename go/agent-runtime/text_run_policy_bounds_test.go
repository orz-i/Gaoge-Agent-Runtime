package agentruntime

import "testing"

func TestTextRunPolicyUsesBoundedDefaults(t *testing.T) {
	service := &Engine{cfg: StaticConfigProvider(Config{})}
	policy := service.TextRunPolicy()
	if policy.PlanMaxSteps != 12 || policy.PlanMaxRevisions != 5 || policy.InteractionTTLHours != 168 || policy.OutputMaxPerRun != 50 {
		t.Fatalf("unexpected policy defaults: %+v", policy)
	}
}

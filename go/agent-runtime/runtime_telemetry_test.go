package agentruntime

import (
	"testing"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	testTelemetryRunID      = "run-ephemeral"
	testTelemetryWorkflowID = "workflow-review"
)

func TestRunTelemetryAgentIDUsesStableResourceIdentity(t *testing.T) {
	tests := []struct {
		name string
		run  model.Run
		want string
	}{
		{
			name: "agent manifest wins",
			run: model.Run{
				RunID:              testTelemetryRunID,
				AgentManifest:      model.ResourceRef{Kind: model.AgentManifestKind, ID: "agent-reviewer", Revision: "2"},
				WorkflowDefinition: model.ResourceRef{Kind: model.WorkflowDefinitionKind, ID: testTelemetryWorkflowID, Revision: "4"},
			},
			want: "agent-reviewer",
		},
		{
			name: "workflow definition fallback",
			run: model.Run{
				RunID:              testTelemetryRunID,
				WorkflowDefinition: model.ResourceRef{Kind: model.WorkflowDefinitionKind, ID: testTelemetryWorkflowID, Revision: "4"},
			},
			want: testTelemetryWorkflowID,
		},
		{name: "no stable identity", run: model.Run{RunID: testTelemetryRunID}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := runTelemetryAgentID(test.run); got != test.want {
				t.Fatalf("runTelemetryAgentID() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTelemetryAgentFieldsOmitsRunIdentityFallback(t *testing.T) {
	fields := telemetryAgentFields("", String("run.id", testTelemetryRunID))
	if len(fields) != 1 || fields[0].Key != "run.id" {
		t.Fatalf("unexpected telemetry fields: %#v", fields)
	}
}

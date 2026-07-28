package agentruntime

import (
	"strings"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func runTelemetryAgentID(run model.Run) string {
	if agentID := strings.TrimSpace(run.AgentManifest.ID); agentID != "" {
		return agentID
	}
	return strings.TrimSpace(run.WorkflowDefinition.ID)
}

func telemetryAgentFields(agentID string, fields ...LogField) []LogField {
	result := make([]LogField, 0, len(fields)+1)
	if agentID = strings.TrimSpace(agentID); agentID != "" {
		result = append(result, String("gen_ai.agent.id", agentID))
	}
	return append(result, fields...)
}

func runTelemetryFields(run model.Run, fields ...LogField) []LogField {
	return telemetryAgentFields(runTelemetryAgentID(run), fields...)
}

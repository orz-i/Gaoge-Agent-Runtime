package agentruntime

import (
	"encoding/json"
)

func measureToolDefinitionsBytes(tools []ToolDefinition) (int, error) {
	// Internal stable measure of frozen ToolDefinition list (not provider wire size).
	raw, err := json.Marshal(tools)
	if err != nil {
		return 0, err
	}
	return len(raw), nil
}

const (
	ErrorCodeUpstreamPayloadTooLarge  = "upstream_payload_too_large"
	errorCodeWorkspaceArtifactMissing = "workspace_artifact_missing"
)

package http

import (
	"encoding/json"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runfeed"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workbench"
)

const publicUnknownFailureCode = "execution.failed"

// PublicFailureCode is a bounded SDK HTTP failure fact. Arbitrary feature,
// Provider or user-supplied strings are not an error-code namespace grant.
func PublicFailureCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	switch code {
	case "run.cancelled", "run.deadline_exceeded",
		"agent.model_failed", "agent.model_retry_exhausted",
		"agent.tool_failed", "agent.tool_invalid", "agent.discovery_invalid",
		"agent.deadline_exceeded", "agent.llm_limit", "agent.tool_limit",
		"agent.shared_budget_exhausted", "agent.model_allowance_exhausted",
		"agent.tool_allowance_exhausted", "agent.total_tokens_budget",
		"agent.output_bytes_budget", "agent.model_invocation_invalid",
		"agent.model_invocation.pending", "agent.model_invocation.claimed",
		"agent.completion_validation_failed", "agent.usage_invalid",
		"agent.model_invocation.retryable", "agent.model_invocation.completed",
		"model.upstream_auth_failed", "model.upstream_access_denied",
		"model.upstream_quota_exhausted", "model.upstream_rate_limited",
		"model.upstream_unavailable", "model.upstream_timeout",
		"model.upstream_context_exceeded", "model.upstream_request_invalid",
		"model.upstream_request_rejected", "model.upstream_model_unavailable",
		"model.upstream_safety_rejected", "model_route_not_configured",
		"model.access_denied", "model.entitlement_denied", publicUnknownFailureCode:
		return code
	}
	if strings.HasPrefix(code, "agent.shared_") && strings.HasSuffix(code, "_budget") {
		dimension := strings.TrimSuffix(strings.TrimPrefix(code, "agent.shared_"), "_budget")
		switch dimension {
		case "llm_calls", "tool_calls", "input_tokens", "output_tokens",
			"total_tokens", "child_runs", "concurrent_runs", "output_bytes",
			"state_bytes", "cost_units":
			return code
		}
	}
	return publicUnknownFailureCode
}

// PublicFailureDetail never echoes the original SDK/kernel/provider error.
// Internal detail remains available to separately authorized diagnostics.
func PublicFailureDetail(code, detail string) string {
	code = PublicFailureCode(code)
	if code == "" && strings.TrimSpace(detail) == "" {
		return ""
	}
	switch code {
	case "run.cancelled":
		return "execution was cancelled"
	case "agent.model_allowance_exhausted", "agent.tool_allowance_exhausted":
		return "execution paused at its current call allowance"
	case "model.upstream_auth_failed":
		return "model provider authentication failed"
	case "model.upstream_access_denied", "model.access_denied":
		return "model access is denied"
	case "model.entitlement_denied", "model.upstream_quota_exhausted":
		return "model usage entitlement or quota is unavailable"
	case "model.upstream_rate_limited":
		return "model provider is limiting requests"
	case "model.upstream_unavailable", "model.upstream_timeout":
		return "model provider could not complete the request"
	case "model.upstream_context_exceeded", "model.upstream_request_invalid":
		return "model input must be adjusted"
	case "model.upstream_model_unavailable", "model_route_not_configured":
		return "model execution route is unavailable"
	case "agent.tool_failed":
		return "tool execution failed"
	default:
		return "execution could not be completed"
	}
}

// PublicRun is the owner-visible projection of one durable Run. It never
// changes the actual Kernel snapshot or state-machine error facts.
func PublicRun(run kernel.Run) kernel.Run {
	code := run.ErrorCode
	detail := run.ErrorDetail
	run.ErrorCode = PublicFailureCode(code)
	run.ErrorDetail = PublicFailureDetail(code, detail)
	return run
}

// PublicDiagnosticPayload strips *diagnostic-only* keys from structured
// optional feature payloads while preserving ordinary author-visible content,
// model text, tool arguments and results. Non-JSON payloads fail closed.
func PublicDiagnosticPayload(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return nil
	}
	stripDiagnosticFields(decoded)
	result, err := json.Marshal(decoded)
	if err != nil {
		return nil
	}
	return result
}

func stripDiagnosticFields(value any) {
	switch item := value.(type) {
	case []any:
		for _, entry := range item {
			stripDiagnosticFields(entry)
		}
	case map[string]any:
		for key, child := range item {
			normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
			switch normalized {
			case "errordetail", "errormessage", "errorpreview", "cancelerror",
				"upstreamdebug", "providererror", "providerresponse", "providerrequest",
				"requestbody", "responsebody", "rawerror", "stacktrace",
				"traceback", "debug", "exception":
				delete(item, key)
			default:
				stripDiagnosticFields(child)
			}
		}
	}
}

// PublicEventMessage replaces arbitrary error/journal descriptions at failure
// boundaries, without overwriting authored text and ordinary progress events.
func PublicEventMessage(eventType, status, message string) string {
	normalized := strings.ToLower(strings.TrimSpace(eventType))
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "failed" || status == "cancelled" ||
		strings.HasSuffix(normalized, ".failed") ||
		strings.HasSuffix(normalized, ".cancelled") ||
		strings.HasSuffix(normalized, ".error") ||
		strings.HasSuffix(normalized, ".rejected") {
		if status == "cancelled" || strings.HasSuffix(normalized, ".cancelled") {
			return PublicFailureDetail("run.cancelled", message)
		}
		return PublicFailureDetail(publicUnknownFailureCode, message)
	}
	return message
}

func publicKernelEvents(events []kernel.Event) []kernel.Event {
	result := make([]kernel.Event, len(events))
	for index, event := range events {
		event.Message = PublicEventMessage(event.Type, "", event.Message)
		event.Data = PublicDiagnosticPayload(event.Data)
		result[index] = event
	}
	return result
}

func publicRunFeedEvent(event runfeed.Event) runfeed.Event {
	event.Message = PublicEventMessage(event.Type, event.Status, event.Message)
	event.Data = PublicDiagnosticPayload(event.Data)
	return event
}

func publicWorkbench(detail workbench.Detail) workbench.Detail {
	detail.Overview.ErrorCode = PublicFailureCode(detail.Overview.ErrorCode)
	detail.Overview.ErrorDetail = PublicFailureDetail(detail.Run.ErrorCode, detail.Overview.ErrorDetail)
	detail.Run = PublicRun(detail.Run)
	detail.Diagnostics = append([]workbench.Diagnostic(nil), detail.Diagnostics...)
	for index := range detail.Diagnostics {
		if detail.Diagnostics[index].Message != "" {
			detail.Diagnostics[index].Message = "diagnostic unavailable"
		}
	}
	if detail.Checkpoint != nil {
		checkpoint := *detail.Checkpoint
		checkpoint.Payload = PublicDiagnosticPayload(checkpoint.Payload)
		checkpoint.Response = PublicDiagnosticPayload(checkpoint.Response)
		detail.Checkpoint = &checkpoint
	}
	detail.Timeline = append([]workbench.TimelineItem(nil), detail.Timeline...)
	for index := range detail.Timeline {
		event := &detail.Timeline[index]
		event.Title = PublicEventMessage(event.Kind, event.Status, event.Title)
		event.Summary = PublicEventMessage(event.Kind, event.Status, event.Summary)
		event.Data = PublicDiagnosticPayload(event.Data)
	}
	detail.Sections = append([]workbench.Section(nil), detail.Sections...)
	for index := range detail.Sections {
		detail.Sections[index].Content = PublicDiagnosticPayload(detail.Sections[index].Content)
	}
	if detail.Topology != nil {
		topology := *detail.Topology
		topology.Nodes = append([]workbench.TopologyNode(nil), topology.Nodes...)
		for index := range topology.Nodes {
			topology.Nodes[index].Data = PublicDiagnosticPayload(topology.Nodes[index].Data)
		}
		topology.Edges = append([]workbench.TopologyEdge(nil), topology.Edges...)
		for index := range topology.Edges {
			topology.Edges[index].Data = PublicDiagnosticPayload(topology.Edges[index].Data)
		}
		detail.Topology = &topology
	}
	return detail
}

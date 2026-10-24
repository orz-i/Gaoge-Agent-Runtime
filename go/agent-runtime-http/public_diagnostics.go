package http

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
		"agent.tool_deadline_exceeded", "agent.tool_wait_invalid",
		"agent.deadline_exceeded", "agent.llm_limit", "agent.tool_limit",
		"agent.shared_budget_exhausted", "agent.model_allowance_exhausted",
		"agent.tool_allowance_exhausted", "agent.total_tokens_budget",
		"agent.output_bytes_budget", "agent.model_invocation_invalid",
		"agent.model_invocation.pending", "agent.model_invocation.claimed",
		"agent.completion_validation_failed", "agent.usage_invalid",
		"agent.completion_repair_exhausted", "agent.model_response_rejected", "model.post_generation_failed",
		"agent.model_invocation.retryable", "agent.model_invocation.completed",
		"model.upstream_auth_failed", "model.upstream_access_denied",
		"model.upstream_quota_exhausted", "model.upstream_rate_limited",
		"model.upstream_unavailable", "model.upstream_timeout",
		"model.upstream_context_exceeded", "model.upstream_request_invalid",
		"model.upstream_request_rejected", "model.upstream_model_unavailable",
		"model.upstream_safety_rejected", "model_route_not_configured",
		"model.access_denied", "model.entitlement_denied",
		"context.artifact_payload_too_large", "context.input_budget_exceeded", publicUnknownFailureCode:
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
	case "agent.completion_repair_exhausted":
		return "model could not produce an acceptable final answer within its repair allowance"
	case "agent.model_response_rejected":
		return "completed model response violated its response or tool contract"
	case "model.post_generation_failed":
		return "model generation completed but its local receipt or artifacts could not be saved"
	case "agent.tool_deadline_exceeded":
		return "external tool result did not arrive before its deadline"
	case "context.artifact_payload_too_large":
		return "context archive exceeds the 4 MiB storage limit; shorten the input or start a new conversation"
	case "context.input_budget_exceeded":
		return "context request exceeds its token or serialized size limit; shorten the input or start a new conversation"
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

// PublicDiagnosticPayload strips diagnostic-only and provider-private replay
// fields from structured feature payloads while preserving ordinary
// author-visible model text, Tool arguments and results. ProviderReplay and
// PreparedInput contain private continuation evidence and frozen host input;
// neither may cross a public HTTP projection, even inside business content.
// Non-JSON payloads fail closed.
func PublicDiagnosticPayload(value json.RawMessage) json.RawMessage {
	if !json.Valid(value) {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil
	}
	stripDiagnosticFields(decoded, false)
	result, err := json.Marshal(decoded)
	if err != nil {
		return nil
	}
	return result
}

// The feature-owned state is opaque. Generic root-level "error"/"failure"
// objects are SDK diagnostic state and are not exported to ordinary owners.
// Under explicit business payload keys, preserve ordinary tool/model outcomes
// while still dropping known diagnostic sub-fields.
func stripDiagnosticFields(value any, businessContent bool) {
	switch item := value.(type) {
	case []any:
		for _, entry := range item {
			stripDiagnosticFields(entry, businessContent)
		}
	case map[string]any:
		for key, child := range item {
			normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
			switch normalized {
			case "providerreplay", "preparedinput":
				// Private host input and signed/encrypted evidence are persisted
				// for recovery but never readable through public HTTP JSON, including
				// nested messages, model/selector invocation inputs and business payloads.
				delete(item, key)
				continue
			case "error", "errors":
				if !businessContent {
					delete(item, key)
					continue
				}
			case "failure":
				if !businessContent {
					if fact, ok := child.(map[string]any); ok {
						for field, raw := range fact {
							switch strings.ToLower(field) {
							case "detail", "message", "error":
								delete(fact, field)
							case "code":
								if code, valid := raw.(string); valid {
									fact[field] = PublicFailureCode(code)
								} else {
									delete(fact, field)
								}
							}
						}
						stripDiagnosticFields(fact, false)
					} else {
						delete(item, key)
					}
					continue
				}
			case "errorcode":
				if !businessContent {
					if code, ok := child.(string); ok {
						item[key] = PublicFailureCode(code)
					} else {
						delete(item, key)
					}
					continue
				}
			case "toolwait", "externalsignals":
				if !businessContent {
					delete(item, key)
					continue
				}
			case "errordetail", "errormessage", "errorpreview", "cancelerror",
				"diagnostic", "diagnostics", "privatedebug", "upstreamdebug",
				"providererror", "providerresponse", "providerrequest",
				"requestbody", "responsebody", "rawerror", "stacktrace",
				"traceback", "debug", "exception", "headers", "requestheaders",
				"responseheaders", "authorization", "apikey", "accesstoken",
				"secret", "credentials":
				delete(item, key)
				continue
			}
			nextBusiness := businessContent
			switch normalized {
			case "result", "results", "output", "outputs", "items", "content",
				"toolresult", "toolresults", "userdata", "input":
				nextBusiness = true
			}
			stripDiagnosticFields(child, nextBusiness)
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

// Tool lifecycle events are not generic business JSON: Agent publishes the
// complete model-authored Tool Call at tool.started. A readable Run Feed must
// never turn that private execution intent into an HTTP data channel.
// Keep a stable opaque call ID and a canonical, bounded Tool key only.
func publicToolFeedKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 160 {
		return ""
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '.' || char == '-' || char == '_' || char == ':' {
			continue
		}
		return ""
	}
	return value
}

func publicToolCallID(runID, callID string) string {
	if strings.TrimSpace(callID) == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(runID + "\x00" + callID))
	return "call_" + hex.EncodeToString(digest[:8])
}

func publicToolRunFeedData(event runfeed.Event) json.RawMessage {
	var fields struct {
		ID          string `json:"id"`
		CallID      string `json:"callID"`
		ToolKey     string `json:"toolKey"`
		ExecutionID string `json:"executionID"`
	}
	if json.Unmarshal(event.Data, &fields) != nil {
		return nil
	}
	if fields.CallID == "" {
		fields.CallID = fields.ID
	}
	if fields.CallID == "" {
		fields.CallID = fields.ExecutionID
	}
	result, err := json.Marshal(struct {
		CallID  string `json:"callID,omitempty"`
		ToolKey string `json:"toolKey,omitempty"`
		Status  string `json:"status"`
	}{
		CallID: publicToolCallID(event.RunID, fields.CallID), ToolKey: publicToolFeedKey(fields.ToolKey),
		Status: func() string {
			switch event.Type {
			case runfeed.EventToolRequested:
				return "requested"
			case runfeed.EventToolStarted:
				return "running"
			case "tool.pending":
				return "pending"
			default:
				if event.Message != "" {
					return "needs_correction"
				}
				return "completed"
			}
		}(),
	})
	if err != nil {
		return nil
	}
	return result
}

func publicDiscoveryProgress(value json.RawMessage) json.RawMessage {
	var data struct {
		Code  string `json:"code"`
		Count int    `json:"count"`
	}
	if json.Unmarshal(value, &data) != nil || data.Count < 0 || data.Count > 5 {
		return nil
	}
	title, summary := "Tool discovery", ""
	switch data.Code {
	case "tool_discovery.empty":
		if data.Count != 0 {
			return nil
		}
		summary = "No matching authorized tools found"
	case "tool_discovery.matched":
		if data.Count == 0 {
			return nil
		}
		summary = fmt.Sprintf("Matched %d authorized tools", data.Count)
	default:
		return nil
	}
	projected, err := json.Marshal(struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}{title, summary})
	if err != nil {
		return nil
	}
	return projected
}

// The Tool approval checkpoint contains the complete private arguments in
// Payload. Only a known, pending Tool approval gets an owner-visible identity;
// arbitrary interaction plugins cannot inject their opaque payloads here.
func publicRunFeedApprovalData(event runfeed.Event) json.RawMessage {
	var checkpoint struct {
		ID      string          `json:"id"`
		Kind    string          `json:"kind"`
		Status  string          `json:"status"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(event.Data, &checkpoint) != nil || checkpoint.Kind != "tool_approval" ||
		checkpoint.Status != "pending" || checkpoint.ID == "" || len(checkpoint.ID) > 128 ||
		publicToolFeedKey(checkpoint.ID) != checkpoint.ID {
		return nil
	}
	var approval struct {
		ToolCallID string `json:"toolCallID"`
		ToolKey    string `json:"toolKey"`
	}
	if json.Unmarshal(checkpoint.Payload, &approval) != nil {
		return nil
	}
	result, err := json.Marshal(struct {
		ID         string `json:"id"`
		Kind       string `json:"kind"`
		Status     string `json:"status"`
		ToolCallID string `json:"toolCallID,omitempty"`
		ToolKey    string `json:"toolKey,omitempty"`
	}{
		ID: checkpoint.ID, Kind: checkpoint.Kind, Status: checkpoint.Status,
		ToolCallID: publicToolCallID(event.RunID, approval.ToolCallID),
		ToolKey:    publicToolFeedKey(approval.ToolKey),
	})
	if err != nil {
		return nil
	}
	return result
}

func publicRunFeedEvent(event runfeed.Event) runfeed.Event {
	switch event.Type {
	case "activity.user":
		event.Data = publicDiscoveryProgress(event.Data)
		event.Message, event.Delta = "", ""
	case runfeed.EventToolRequested, runfeed.EventToolStarted, runfeed.EventToolCompleted, "tool.pending":
		event.Data = publicToolRunFeedData(event)
		// Untrusted model arguments or error text must never escape through
		// Message, Delta or an opaque nested provider field.
		event.Message, event.Delta = "", ""
	case runfeed.EventInteractionRequired:
		event.Data = publicRunFeedApprovalData(event)
		event.Message, event.Delta = "", ""
	case runfeed.EventModelStarted, runfeed.EventModelDelta, runfeed.EventModelCompleted:
		// Model Delta is rendered from the explicit author-visible delta field;
		// its opaque provider metadata may contain internal reasoning.
		event.Data = nil
		event.Message = ""
	default:
		event.Message = PublicEventMessage(event.Type, event.Status, event.Message)
		event.Data = PublicDiagnosticPayload(event.Data)
	}
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
	return workbench.RehashProjection(detail)
}

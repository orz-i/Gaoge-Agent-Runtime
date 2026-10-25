package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const (
	replayProbeToolKey  = "baseline.probe_echo"
	replayProbeToolName = "baseline_probe_echo"
	replayProbePrompt   = "Call baseline_probe_echo exactly once with value=probe. After its result arrives, return only the JSON object {\"ok\":true} under the API output schema."
)

var replayProbeToolSchema = json.RawMessage(`{"type":"object","properties":{"value":{"type":"string","enum":["probe"]}},"required":["value"],"additionalProperties":false}`)

// toolProbeCheck belongs only to OpenAI Responses compatibility admission.
// It does not use the Agent's catalog/Tool executor or produce side effects.
type toolProbeCheck struct {
	Status              string                  `json:"status"`
	Stage               string                  `json:"stage"`
	FailureCode         string                  `json:"failureCode,omitempty"`
	ModelCalls          int                     `json:"modelCalls"`
	HTTPRequests        int                     `json:"httpRequests"`
	Usage               usageCoverage           `json:"usage"`
	ReasoningSeen       bool                    `json:"reasoningSeen"`
	EncryptedReplaySeen bool                    `json:"encryptedReplaySeen"`
	HTTPStatus          int                     `json:"httpStatus,omitempty"`
	ResponseKind        preflightResponseKind   `json:"responseKind,omitempty"`
	ResponseDebug       *preflightResponseDebug `json:"responseDebug,omitempty"`
}

func baselineToolReplayProbe(ctx context.Context, config settings) (probe toolProbeCheck) {
	probe.Status, probe.Stage, probe.Usage = "failed", "tool_call", usageCoverage{Complete: true}
	observedClient, observation := newPreflightHTTPClient(config.httpClient)
	config.httpClient = observedClient
	defer func() {
		probe.HTTPRequests = observation.dispatchedRequests()
		if probe.Status != "passed" {
			probe.HTTPStatus, probe.ResponseKind = observation.snapshot()
			probe.ResponseDebug = observation.responseDebugSnapshot("", config.apiKey)
		}
	}()
	client, err := config.newModel()
	if err != nil {
		probe.FailureCode = failureCode(err, "provider_configuration_failed")
		return
	}
	streaming, ok := client.(model.StreamingClient)
	if !ok {
		probe.FailureCode = "tool_probe_stream_unavailable"
		return
	}
	definition := tools.Definition{
		Key: replayProbeToolKey, Name: replayProbeToolName,
		Description: "Return the provided harmless probe token. This is only a local compatibility check.",
		InputSchema: replayProbeToolSchema,
	}
	contract := &model.OutputContract{Name: "baseline_probe", Schema: probeSchema}
	firstRequest := model.Request{
		RunID: "baseline-tool-probe", InvocationID: "baseline-tool-probe-1",
		Model: config.routedModel(), MaxOutputTokens: config.outputTokens,
		Messages: []model.Message{{Role: model.RoleUser, Content: replayProbePrompt}},
		Tools:    []tools.Definition{definition}, RequireToolCall: true, OutputContract: contract,
	}
	callCtx, cancel := context.WithTimeout(ctx, modelTimeout)
	probe.ModelCalls++
	first, err := streaming.GenerateStream(callCtx, firstRequest, nil)
	cancel()
	probe.Usage.add(numericUsage(first.Usage))
	if errors.Is(err, openai.ErrReplayUnavailable) {
		probe.ReasoningSeen = true
	}
	if err != nil {
		probe.FailureCode = failureCode(err, "tool_probe_provider_failed")
		return
	}
	if first.Refused || first.Continue || len(first.HostedToolCalls) != 0 ||
		len(first.Artifacts) != 0 || len(first.ToolCalls) != 1 ||
		first.ToolCalls[0].ToolKey != replayProbeToolKey ||
		first.ToolCalls[0].Name != replayProbeToolName ||
		first.ToolCalls[0].ID == "" {
		probe.FailureCode = "tool_probe_call_invalid"
		return
	}
	call := first.ToolCalls[0]
	validator, err := jsoncontract.Compile(replayProbeToolSchema)
	if err != nil || validator.Validate(call.Arguments) != nil {
		probe.FailureCode = "tool_probe_arguments_invalid"
		return
	}
	probe.ReasoningSeen, probe.EncryptedReplaySeen = inspectProbeReplay(first.ProviderReplay)

	probe.Stage = "continuation"
	secondRequest := model.Request{
		RunID: "baseline-tool-probe", InvocationID: "baseline-tool-probe-2",
		Model: config.routedModel(), MaxOutputTokens: config.outputTokens,
		OutputContract: contract, Tools: []tools.Definition{definition},
		Messages: []model.Message{
			firstRequest.Messages[0],
			{Role: model.RoleAssistant, Content: first.Content, ToolCalls: first.ToolCalls, ProviderReplay: first.ProviderReplay},
			{Role: model.RoleTool, ToolCallID: call.ID, Content: `{"value":"probe"}`},
		},
	}
	continuationCtx, continuationCancel := context.WithTimeout(ctx, modelTimeout)
	probe.ModelCalls++
	final, err := streaming.GenerateStream(continuationCtx, secondRequest, nil)
	continuationCancel()
	probe.Usage.add(numericUsage(final.Usage))
	if err != nil {
		probe.FailureCode = failureCode(err, "tool_probe_provider_failed")
		return
	}
	finalValidator, err := jsoncontract.Compile(probeSchema)
	if err != nil || finalValidator.Validate([]byte(final.Content)) != nil ||
		final.Refused || final.Continue || len(final.ToolCalls) != 0 ||
		len(final.HostedToolCalls) != 0 || len(final.Artifacts) != 0 {
		probe.FailureCode = "tool_probe_completion_invalid"
		return
	}
	probe.Status, probe.Stage = "passed", "completed"
	return
}

// Inspection is evidence-only. Provider replay remains opaque and is never
// copied into preflight events, console output or persisted baseline reports.
func inspectProbeReplay(replay *model.ProviderReplay) (reasoning, encrypted bool) {
	if replay == nil || replay.Provider != "openai" {
		return false, false
	}
	var items []struct {
		Type             string `json:"type"`
		EncryptedContent string `json:"encrypted_content"`
	}
	decoder := json.NewDecoder(bytes.NewReader(replay.Data))
	if decoder.Decode(&items) != nil || decoder.Decode(new(any)) != io.EOF {
		return false, false
	}
	for _, item := range items {
		if item.Type == "reasoning" {
			reasoning = true
			encrypted = encrypted || item.EncryptedContent != ""
		}
	}
	return
}

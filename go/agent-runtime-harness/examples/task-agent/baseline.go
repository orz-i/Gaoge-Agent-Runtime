package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type baselineCase struct {
	id     string
	source sourceFixture
}

func baselineCases() []baselineCase {
	return []baselineCase{
		{id: "release-lime", source: defaultSource()},
		{id: "release-violet", source: sourceFixture{code: "VIOLET-83", window: "Friday 09:15-09:45 UTC"}},
	}
}

type measurementProfile struct {
	Sampling                     string                `json:"sampling"`
	SampleTimeoutMs              int64                 `json:"sampleTimeoutMs"`
	PreflightTimeoutMs           int64                 `json:"preflightTimeoutMs"`
	MaxLLMCalls                  int                   `json:"maxLLMCalls"`
	MaxToolCalls                 int                   `json:"maxToolCalls"`
	ArtifactReadInstructionLimit int                   `json:"artifactReadInstructionLimit"`
	ModelTimeoutMs               int64                 `json:"modelTimeoutMs"`
	ToolTimeoutMs                int64                 `json:"toolTimeoutMs"`
	MaxAttempts                  int                   `json:"maxAttempts"`
	MaxConcurrency               int                   `json:"maxConcurrency"`
	LeaseDurationMs              int64                 `json:"leaseDurationMs"`
	ContextPolicy                runtimecontext.Policy `json:"contextPolicy"`
	OracleVersion                string                `json:"oracleVersion"`
	Recovery                     string                `json:"recovery"`
	ModelCallCounting            string                `json:"modelCallCounting"`
	ToolCallCounting             string                `json:"toolCallCounting"`
	DurationClock                string                `json:"durationClock"`
	LatencyPercentile            string                `json:"latencyPercentile"`
	UsageAccounting              string                `json:"usageAccounting"`
}

func baselineProfile() measurementProfile {
	return measurementProfile{
		Sampling: "sequential-round-robin", SampleTimeoutMs: sampleTimeout.Milliseconds(),
		PreflightTimeoutMs: modelTimeout.Milliseconds(), MaxLLMCalls: maxModelCalls, MaxToolCalls: maxToolCalls,
		ModelTimeoutMs: modelTimeout.Milliseconds(), ToolTimeoutMs: toolTimeout.Milliseconds(),
		MaxAttempts: 1, MaxConcurrency: 1, LeaseDurationMs: leaseDuration.Milliseconds(), ContextPolicy: taskContextPolicy(), ArtifactReadInstructionLimit: artifactReadLimit,
		OracleVersion: "evidence-draft-recovery-v1", Recovery: "in-process-store-clock-expiry",
		ModelCallCounting: "adapter-invocations", ToolCallCounting: "registered-handler-invocations",
		DurationClock: "wall-monotonic-ms", LatencyPercentile: "nearest-rank", UsageAccounting: "final-response-known-subtotal",
	}
}

type startEvent struct {
	Type             string              `json:"type"`
	SchemaVersion    int                 `json:"schemaVersion"`
	TaskVersion      string              `json:"taskVersion"`
	DatasetHash      string              `json:"datasetHash"`
	Profile          measurementProfile  `json:"profile"`
	Configuration    publicConfiguration `json:"configuration"`
	RequestedSamples int                 `json:"requestedSamples"`
}

// The manifest hashes the real fixtures, instructions, tool/output contracts
// and execution policy. Model identity and host-supplied capacity remain in the
// separate configuration so the task can be compared across models.
func baselineStart(configuration publicConfiguration) (startEvent, error) {
	profile := baselineProfile()
	type caseManifest struct {
		ID         string `json:"id"`
		SourceHash string `json:"sourceHash"`
	}
	manifest := struct {
		TaskVersion  string                       `json:"taskVersion"`
		Prompt       string                       `json:"prompt"`
		Instructions string                       `json:"instructions"`
		OutputSchema json.RawMessage              `json:"outputSchema"`
		Tools        []tools.Definition           `json:"tools"`
		ToolPolicies []harness.ToolPolicySnapshot `json:"toolPolicies"`
		Cases        []caseManifest               `json:"cases"`
		Profile      measurementProfile           `json:"profile"`
	}{TaskVersion: baselineTaskVersion, Prompt: taskPrompt, Instructions: taskInstructions, OutputSchema: answerSchema, ToolPolicies: taskToolPolicies(), Profile: profile}
	for _, item := range baselineCases() {
		manifest.Cases = append(manifest.Cases, caseManifest{ID: item.id, SourceHash: digest([]byte(item.source.document()))})
	}
	for _, registration := range (&taskState{}).registrations() {
		manifest.Tools = append(manifest.Tools, registration.Definition)
	}
	manifest.Tools = append(manifest.Tools, harness.ContextArtifactToolRegistration(nil).Definition)
	body, err := json.Marshal(manifest)
	if err != nil {
		return startEvent{}, err
	}
	return startEvent{
		Type: "start", SchemaVersion: baselineSchemaVersion, TaskVersion: baselineTaskVersion, DatasetHash: digest(body),
		Profile: profile, Configuration: configuration, RequestedSamples: len(manifest.Cases) * configuration.Repetitions,
	}, nil
}

type preflightEvent struct {
	Type        string      `json:"type"`
	Status      string      `json:"status"`
	FailureCode string      `json:"failureCode,omitempty"`
	DurationMs  int64       `json:"durationMs"`
	Usage       *tokenUsage `json:"usage,omitempty"`
}

var probeSchema = json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean","enum":[true]}},"required":["ok"],"additionalProperties":false}`)

func baselinePreflight(ctx context.Context, config settings) (event preflightEvent) {
	started := time.Now()
	event.Type, event.Status = "preflight", "failed"
	defer func() { event.DurationMs = time.Since(started).Milliseconds() }()
	probeCtx, cancel := context.WithTimeout(ctx, modelTimeout)
	defer cancel()
	client, err := config.newModel()
	if err != nil {
		event.FailureCode = failureCode(err, "provider_configuration_failed")
		return event
	}
	capability, ok := client.(model.CapabilityProvider)
	if !ok {
		event.FailureCode = "capability_unavailable"
		return event
	}
	available, err := capability.Capabilities(probeCtx, config.routedModel())
	if err != nil || !available.StructuredJSONSchema {
		event.FailureCode = failureCode(errors.Join(err, model.ErrCapabilityUnavailable), "capability_unavailable")
		return event
	}
	response, err := client.Generate(probeCtx, model.Request{
		RunID: "baseline-preflight", InvocationID: "baseline-preflight", Model: config.routedModel(),
		Messages:        []model.Message{{Role: model.RoleUser, Content: "Say that this check passed in a plain sentence. The response must still obey the API's output schema."}},
		OutputContract:  &model.OutputContract{Name: "baseline_probe", Schema: probeSchema},
		MaxOutputTokens: config.outputTokens,
	})
	event.Usage = numericUsage(response.Usage)
	if err != nil {
		event.FailureCode = failureCode(err, "provider_failed")
		return event
	}
	if response.Refused {
		event.FailureCode = "output_refused"
		return event
	}
	validator, err := jsoncontract.Compile(probeSchema)
	if err != nil || validator.Validate(json.RawMessage(response.Content)) != nil || response.Continue ||
		len(response.ToolCalls) != 0 || len(response.HostedToolCalls) != 0 || len(response.Artifacts) != 0 {
		event.FailureCode = "strict_schema_probe_failed"
		return event
	}
	event.Status = "passed"
	return event
}

type baselineSample struct {
	SampleID          string                `json:"sampleID"`
	CaseID            string                `json:"caseID"`
	Repetition        int                   `json:"repetition"`
	Status            string                `json:"status"`
	Stage             string                `json:"stage"`
	FailureCode       string                `json:"failureCode,omitempty"`
	DurationMs        int64                 `json:"durationMs"`
	ModelCalls        int                   `json:"modelCalls"`
	ToolCalls         int                   `json:"toolCalls"`
	LookupCalls       int                   `json:"lookupCalls"`
	ArtifactReads     int                   `json:"artifactReads"`
	DraftWrites       int                   `json:"draftWrites"`
	OraclePassed      bool                  `json:"oraclePassed"`
	RecoveryChecks    recoveryChecks        `json:"recoveryChecks"`
	Usage             usageCoverage         `json:"usage"`
	ModelDurationsMs  []int64               `json:"modelDurationsMs"`
	ToolExecutions    []toolExecutionMetric `json:"toolExecutions"`
	ContextGeneration int                   `json:"contextGeneration"`
	RunID             string                `json:"runID,omitempty"`
	TurnID            string                `json:"turnID,omitempty"`
}

func sampleMetrics(report taskReport, caseID, sampleID string, repetition int) baselineSample {
	return baselineSample{
		SampleID: sampleID, CaseID: caseID, Repetition: repetition, Status: report.Status, Stage: report.Stage,
		FailureCode: report.FailureCode, DurationMs: report.DurationMs, ModelCalls: report.ModelCalls, ToolCalls: report.ToolCalls,
		LookupCalls: report.LookupCalls, ArtifactReads: report.ArtifactReads, DraftWrites: report.DraftWrites,
		OraclePassed: report.OraclePassed, RecoveryChecks: report.RecoveryChecks, Usage: report.Usage,
		ModelDurationsMs: append([]int64{}, report.ModelDurationsMs...), ToolExecutions: append([]toolExecutionMetric{}, report.ToolExecutions...),
		ContextGeneration: report.ContextGeneration, RunID: report.RunID, TurnID: report.TurnID,
	}
}

type baselineSummary struct {
	Requested   int           `json:"requested"`
	Attempted   int           `json:"attempted"`
	Passed      int           `json:"passed"`
	Failed      int           `json:"failed"`
	Skipped     int           `json:"skipped"`
	SuccessRate *float64      `json:"successRate"`
	Complete    bool          `json:"complete"`
	DurationMs  durationStats `json:"durationMs"`
	Usage       usageCoverage `json:"usage"`
	ModelCalls  int           `json:"modelCalls"`
	ToolCalls   int           `json:"toolCalls"`
}

func summarize(requested int, samples []baselineSample, preflightPassed bool) baselineSummary {
	result := baselineSummary{Requested: requested, Attempted: len(samples), Skipped: requested - len(samples), Usage: usageCoverage{Complete: true}}
	var elapsed []int64
	for _, sample := range samples {
		if sample.Status == "passed" {
			result.Passed++
		} else {
			result.Failed++
		}
		result.ModelCalls += sample.ModelCalls
		result.ToolCalls += sample.ToolCalls
		result.Usage.merge(sample.Usage)
		elapsed = append(elapsed, sample.DurationMs)
	}
	if result.Attempted > 0 {
		rate := float64(result.Passed) / float64(result.Attempted)
		result.SuccessRate = &rate
	}
	result.Complete = preflightPassed && result.Attempted == result.Requested
	result.DurationMs = durations(elapsed)
	return result
}

// runBaseline writes one complete JSON object per event. Completed samples are
// flushed by Encoder immediately; provider failure never suppresses the report.
func runBaseline(ctx context.Context, input io.Reader, output io.Writer) int {
	configuration, public, err := readBaselineInput(input)
	if err != nil {
		return configurationError(output)
	}
	start, err := baselineStart(public)
	if err != nil {
		return configurationError(output)
	}
	encoder := json.NewEncoder(output)
	if encoder.Encode(start) != nil {
		return 1
	}
	config := configuration.settings()
	preflight := baselinePreflight(ctx, config)
	if encoder.Encode(preflight) != nil {
		return 1
	}
	var samples []baselineSample
	if preflight.Status == "passed" {
		for repetition := 1; repetition <= configuration.Repetitions && ctx.Err() == nil; repetition++ {
			for _, item := range baselineCases() {
				if ctx.Err() != nil {
					break
				}
				config.sampleID = fmt.Sprintf("%s-%02d", item.id, repetition)
				sampleCtx, cancel := context.WithTimeout(ctx, sampleTimeout)
				report, _ := runTask(sampleCtx, config, config.newModel, item.source)
				cancel()
				sample := sampleMetrics(report, item.id, config.sampleID, repetition)
				samples = append(samples, sample)
				if encoder.Encode(struct {
					Type   string         `json:"type"`
					Sample baselineSample `json:"sample"`
				}{Type: "sample", Sample: sample}) != nil {
					return 1
				}
			}
		}
	}
	summary := summarize(start.RequestedSamples, samples, preflight.Status == "passed")
	if encoder.Encode(struct {
		Type    string          `json:"type"`
		Summary baselineSummary `json:"summary"`
	}{Type: "summary", Summary: summary}) != nil {
		return 1
	}
	if !summary.Complete || summary.Failed != 0 || ctx.Err() != nil {
		return 1
	}
	return 0
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func unitBaselineInput() baselineInput {
	return baselineInput{
		SchemaVersion: 1, Provider: "openai", Endpoint: "https://provider.invalid/v1/responses",
		APIKey: "private-fixture-key", Model: "gpt-4.1", ContextTokens: 8192, MaxOutputTokens: 512, Repetitions: 1,
	}
}

func TestBaselineInvalidInputEmitsOnlySafeConfigurationFailure(t *testing.T) {
	for _, change := range []func(*baselineInput){
		func(input *baselineInput) { input.Repetitions = 21 },
		func(input *baselineInput) { input.ContextTokens = 512 },
		func(input *baselineInput) { input.Endpoint += "?api_key=" + input.APIKey },
		func(input *baselineInput) { input.Endpoint = "http://provider.invalid/v1/responses" },
		func(input *baselineInput) { input.Model = input.APIKey },
		func(input *baselineInput) { input.APIKey += "\n" },
	} {
		input := unitBaselineInput()
		change(&input)
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		if code := runBaseline(t.Context(), bytes.NewReader(body), &output); code != 2 ||
			strings.Contains(output.String(), "private-fixture-key") || strings.Count(output.String(), "\n") != 1 ||
			!strings.Contains(output.String(), `"failureCode":"invalid_configuration"`) {
			t.Fatalf("unsafe or missing configuration report: code=%d output=%s", code, output.String())
		}
	}
	for _, body := range []string{
		`{"schemaVersion":1,"schemaVersion":1}`,
		`{"unknown":"private-fixture-key"}`,
		strings.Repeat("x", maxBaselineInputBytes+1),
	} {
		var output bytes.Buffer
		if code := runBaseline(t.Context(), strings.NewReader(body), &output); code != 2 {
			t.Fatalf("unbounded or ambiguous input accepted: code=%d", code)
		}
	}
}

func TestBaselineManifestIsComparableAcrossModelConfigurations(t *testing.T) {
	first, err := baselineStart(publicConfiguration{Provider: "openai", Model: "first-model", ContextTokens: 8192, MaxOutputTokens: 512, Repetitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := baselineStart(publicConfiguration{Provider: "anthropic", Model: "other-model", ContextTokens: 32768, MaxOutputTokens: 1024, Repetitions: 3})
	if err != nil {
		t.Fatal(err)
	}
	if first.DatasetHash != second.DatasetHash || first.RequestedSamples != 2 || second.RequestedSamples != 6 ||
		first.Profile.MaxLLMCalls != taskLimits().MaxLLMCalls || first.Profile.MaxToolCalls != taskLimits().MaxToolCalls ||
		first.Profile.ContextPolicy != taskContextPolicy() || first.Profile.ArtifactReadInstructionLimit != 128 {
		t.Fatalf("manifest mixed model settings with task policy: first=%+v second=%+v", first, second)
	}
	cases := baselineCases()
	if digest([]byte(cases[0].source.document())) == digest([]byte(cases[1].source.document())) {
		t.Fatal("fixed cases accidentally contain the same evidence")
	}
}

func TestBaselineUsageKeepsUnknownCoverageAndDoesNotDoubleCountDetails(t *testing.T) {
	known := numericUsage(&model.Usage{InputTokens: 31, OutputTokens: 13, CacheReadTokens: 9, ReasoningTokens: 4, Speed: "private-fixture-key"})
	usage := usageCoverage{Complete: true}
	usage.add(known)
	usage.add(nil)
	result := summarize(3, []baselineSample{
		{Status: "passed", DurationMs: 10, ModelCalls: 2, Usage: usage},
		{Status: "failed", DurationMs: 30, ModelCalls: 1, Usage: usageCoverage{MissingCalls: 1}},
	}, true)
	if result.Usage.KnownCalls != 1 || result.Usage.MissingCalls != 2 || result.Usage.Complete ||
		result.Usage.Known == nil || result.Usage.Known.InputTokens != 31 || result.Usage.Known.OutputTokens != 13 ||
		result.Attempted != 2 || result.Failed != 1 || result.Skipped != 1 || result.Complete ||
		result.SuccessRate == nil || *result.SuccessRate != 0.5 || result.DurationMs.P95 != 30 {
		t.Fatalf("invalid partial accounting: %+v", result)
	}
	raw, err := json.Marshal(result)
	if err != nil || bytes.Contains(raw, []byte("private-fixture-key")) {
		t.Fatal("provider usage metadata escaped numeric projection")
	}
	unknown := usageCoverage{Complete: true}
	unknown.add(nil)
	if unknown.Known != nil || unknown.Complete || unknown.MissingCalls != 1 {
		t.Fatalf("missing receipt fabricated zero usage: %+v", unknown)
	}
}

type errorFixtureModel struct {
	scriptedModel
	cancel context.CancelFunc
}

func (provider errorFixtureModel) Generate(ctx context.Context, _ model.Request) (model.Response, error) {
	if provider.cancel != nil {
		provider.cancel()
		return model.Response{}, ctx.Err()
	}
	return model.Response{}, errors.New("private-fixture-key in untrusted upstream error")
}

func TestBaselineFailureAndCancellationRetainPartialMetrics(t *testing.T) {
	for _, cancelRun := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		provider := errorFixtureModel{}
		if cancelRun {
			provider.cancel = cancel
		}
		report, err := runTask(ctx, scriptedSettings(t), func() (model.Client, error) { return provider, nil }, defaultSource())
		cancel()
		if err == nil || report.Status != "failed" || report.ModelCalls != 1 || report.ToolCalls != 0 ||
			report.Usage.MissingCalls != 1 || report.Usage.Known != nil || report.OraclePassed {
			t.Fatalf("failure dropped observations: %+v err=%v", report, err)
		}
		if cancelRun && report.FailureCode != "cancelled" {
			t.Fatalf("cancellation was mislabeled: %+v", report)
		}
		raw, encodeErr := json.Marshal(sampleMetrics(report, "release-lime", "release-lime-01", 1))
		if encodeErr != nil || bytes.Contains(raw, []byte("private-fixture-key")) || bytes.Contains(raw, []byte("answer")) {
			t.Fatalf("unsafe failure sample: %s error=%v", raw, encodeErr)
		}
	}
}

type extraReadModel struct {
	scriptedModel
	read bool
}

func (provider *extraReadModel) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	if saved, ok := toolMessage(request.Messages, draftKey); ok && !provider.read {
		provider.read = true
		var answer taskAnswer
		if err := json.Unmarshal([]byte(saved), &answer); err != nil {
			return model.Response{}, err
		}
		return toolCall("new-read-after-resume", harness.ContextArtifactToolKey, map[string]any{
			"artifactID": answer.ArtifactID, "query": "RELEASE R-17", "limit": artifactReadLimit,
		})
	}
	return provider.scriptedModel.Generate(ctx, request)
}

func TestBaselineSeparatesNewModelToolIntentFromRuntimeReplay(t *testing.T) {
	instances := 0
	report, err := runTask(t.Context(), scriptedSettings(t), func() (model.Client, error) {
		instances++
		if instances == 1 {
			return scriptedModel{}, nil
		}
		return &extraReadModel{}, nil
	}, defaultSource())
	if err == nil || report.FailureCode != "extra_tool_after_resume" || !report.RecoveryChecks.CommittedToolsNotReplayed ||
		report.RecoveryChecks.NoExtraToolsAfterResume || report.ToolCalls != 4 || report.ArtifactReads != 2 ||
		report.DraftWrites != 1 || !report.OraclePassed {
		t.Fatalf("new model choice was mislabeled as runtime replay: report=%+v err=%v", report, err)
	}
}

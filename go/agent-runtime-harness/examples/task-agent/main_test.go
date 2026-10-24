package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func scriptedSettings(t *testing.T) settings {
	t.Helper()
	config := settings{mode: "scripted"}
	if err := config.resolve(); err != nil {
		t.Fatal(err)
	}
	return config
}

func TestTaskAgentArchivesEvidenceAndResumesWithoutRepeatingTools(t *testing.T) {
	for _, source := range []sourceFixture{defaultSource(), {code: "VIOLET-83", window: "Friday 09:15-09:45 UTC"}} {
		t.Run(source.code, func(t *testing.T) {
			config := scriptedSettings(t)
			instances := 0
			result, err := runTask(t.Context(), config, func() (model.Client, error) {
				instances++
				return scriptedModel{}, nil
			}, source)
			if err != nil {
				t.Fatal(err)
			}
			if instances != 2 || result.Mode != "deterministic_runtime_acceptance" ||
				result.ModelCalls != 4 || result.LookupCalls != 1 || result.ArtifactReads != 1 ||
				result.DraftWrites != 1 || result.ContextGeneration < 2 ||
				result.Answer.DeploymentCode != source.code || result.Answer.Window != source.window {
				t.Fatalf("task acceptance mismatch: instances=%d report=%+v", instances, result)
			}
		})
	}
}

type wrongAnswerModel struct{ scriptedModel }

func (provider wrongAnswerModel) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	response, err := provider.scriptedModel.Generate(ctx, request)
	if err != nil || len(response.ToolCalls) != 0 {
		return response, err
	}
	var answer taskAnswer
	if err = json.Unmarshal([]byte(response.Content), &answer); err != nil {
		return model.Response{}, err
	}
	answer.DeploymentCode = "INVENTED"
	content, err := json.Marshal(answer)
	response.Content = string(content)
	return response, err
}

func TestTaskOracleRejectsPlausibleButIncorrectFinalAnswer(t *testing.T) {
	_, err := runTask(t.Context(), scriptedSettings(t), func() (model.Client, error) {
		return wrongAnswerModel{}, nil
	}, defaultSource())
	if err == nil || !strings.Contains(err.Error(), "oracle: answer, source facts and single saved draft disagree") {
		t.Fatalf("expected evidence oracle rejection, got %v", err)
	}
}

func TestLiveSettingsRequireExplicitModelCapacityAndOutputLimit(t *testing.T) {
	for _, config := range []settings{
		{mode: "live", provider: "openai", modelID: "host-selected"},
		{mode: "live", provider: "openai", modelID: "host-selected", contextTokens: 8192},
		{mode: "live", provider: "openai", modelID: "host-selected", contextTokens: 4096, outputTokens: 512},
	} {
		if err := config.resolve(); err == nil {
			t.Fatalf("accepted unknown or insufficient model limits: %+v", config)
		}
	}
	// Configuration validation does not construct a provider, read a key, or
	// make any model call.
	config := settings{mode: "live", provider: "openai", modelID: "host-selected", contextTokens: 8192, outputTokens: 512}
	if err := config.resolve(); err != nil {
		t.Fatal(err)
	}
}

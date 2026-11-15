package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestQualityTreatmentSeparatesWorkloadFromExperimentalInstructions(t *testing.T) {
	base := publicConfiguration{Provider: "openai", Suite: qualitySuite, Model: "gpt-4.1",
		ContextTokens: 16384, MaxOutputTokens: 512, Repetitions: 1}
	current, err := baselineStart(base)
	if err != nil {
		t.Fatal(err)
	}
	// This is the untouched CURRENT main quality manifest at db54e8e4.
	// Historical 2026-11-08 pilot hashes belong to an earlier code/profile
	// and must never be reused as the candidate's baseline identity.
	const currentDatasetHash = "sha256:90a6b0b4505c6d29dafa39f26dac39589e78c905245f5a58a6a0404dea97bda2"
	if current.DatasetHash != currentDatasetHash {
		t.Fatalf("the original quality manifest changed: got %s", current.DatasetHash)
	}
	generic := base
	generic.QualityTreatment = genericNavigationTreatment
	variant, err := baselineStart(generic)
	if err != nil {
		t.Fatal(err)
	}
	if current.WorkloadHash == "" || current.TreatmentHash == "" ||
		current.WorkloadHash != variant.WorkloadHash ||
		current.TreatmentHash == variant.TreatmentHash ||
		current.DatasetHash == variant.DatasetHash ||
		current.Profile != variant.Profile || current.RequestedSamples != variant.RequestedSamples {
		t.Fatalf("experiment changed workload/Oracle or failed to rotate treatment: current=%+v generic=%+v", current, variant)
	}
	if strings.Contains(taskInstructionsForTreatment(qualitySuite, genericNavigationTreatment), "LIME-42") ||
		strings.Contains(taskInstructionsForTreatment(qualitySuite, genericNavigationTreatment), "DECOY-") {
		t.Fatal("experimental guidance leaked fixture facts")
	}
	other := base
	other.Provider, other.Model, other.MaxOutputTokens = "anthropic", "different", 1024
	cross, err := baselineStart(other)
	if err != nil || cross.WorkloadHash != current.WorkloadHash ||
		cross.TreatmentHash == current.TreatmentHash {
		t.Fatalf("Provider/model treatment incorrectly redefined workload: %v %+v", err, cross)
	}
	legacy, err := baselineStart(publicConfiguration{Provider: "openai"})
	if err != nil || legacy.WorkloadHash == current.WorkloadHash {
		t.Fatal("the different historical two-case workload shared the quality identity")
	}
	for _, bad := range []publicConfiguration{
		{Suite: qualitySuite, QualityTreatment: "other"},
		{QualityTreatment: genericNavigationTreatment},
	} {
		if _, err := baselineStart(bad); err == nil {
			t.Fatalf("invalid treatment accepted: %+v", bad)
		}
	}
}

// A/B mechanics use the same real Agent, Tool, Context and interruption
// recovery path. The scripted model is source-blind and ignores experiment
// wording, so passing is NOT evidence of improved real-model reasoning.
func TestGenericTreatmentPreservesAgentToolContextRecovery(t *testing.T) {
	for _, item := range qualityCases() {
		t.Run(item.id, func(t *testing.T) {
			config := settings{mode: "scripted", suite: qualitySuite,
				qualityTreatment: genericNavigationTreatment, sampleID: item.id + "-generic"}
			if err := config.resolve(); err != nil {
				t.Fatal(err)
			}
			report, err := runTask(t.Context(), config, func() (model.Client, error) {
				return scriptedModel{}, nil
			}, item.source)
			if err != nil || report.Status != "passed" || !report.OraclePassed ||
				report.ModelCalls != 4 || report.ToolCalls != 3 ||
				report.ArtifactReads != 1 || report.DraftWrites != 1 ||
				!report.RecoveryChecks.InterruptionObserved ||
				!report.RecoveryChecks.SameRun ||
				!report.RecoveryChecks.SameTurn ||
				!report.RecoveryChecks.CommittedToolsNotReplayed ||
				!report.RecoveryChecks.TerminalResumeNoOp {
				t.Fatalf("experiment changed physical path: status=%s error=%s models=%d tools=%d reads=%d drafts=%d oracle=%v recovery=%+v err=%v",
					report.Status, report.FailureCode, report.ModelCalls, report.ToolCalls,
					report.ArtifactReads, report.DraftWrites, report.OraclePassed, report.RecoveryChecks, err)
			}
		})
	}
}

func TestQualityTreatmentInputIsExplicitAndCannotBeRebound(t *testing.T) {
	for _, config := range []settings{
		{mode: "scripted", suite: qualitySuite, qualityTreatment: "typo"},
		{mode: "scripted", qualityTreatment: genericNavigationTreatment},
	} {
		if err := config.resolve(); err == nil {
			t.Fatalf("direct Host settings accepted an invalid treatment: %+v", config)
		}
	}
	valid := unitBaselineInput()
	valid.Suite = qualitySuite
	valid.QualityTreatment = genericNavigationTreatment
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	input, public, err := readBaselineInput(bytes.NewReader(raw))
	if err != nil || input.QualityTreatment != genericNavigationTreatment ||
		public.QualityTreatment != genericNavigationTreatment ||
		input.settings().qualityTreatment != genericNavigationTreatment {
		t.Fatalf("explicit Host variant did not survive input admission: %v", err)
	}
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"legacy", func() []byte { copy := valid; copy.Suite = ""; data, _ := json.Marshal(copy); return data }()},
		{"other", func() []byte {
			copy := valid
			copy.QualityTreatment = "untrusted"
			data, _ := json.Marshal(copy)
			return data
		}()},
		{"null", bytes.Replace(raw, []byte("\"qualityTreatment\":\"generic-navigation-v1\""), []byte("\"qualityTreatment\":null"), 1)},
		{"casefold_alias", bytes.Replace(raw, []byte("\"qualityTreatment\""), []byte("\"QualityTreatment\""), 1)},
		{"duplicate", bytes.Replace(raw, []byte("\"qualityTreatment\":\"generic-navigation-v1\""),
			[]byte("\"qualityTreatment\":\"generic-navigation-v1\",\"qualityTreatment\":\"generic-navigation-v1\""), 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := readBaselineInput(bytes.NewReader(tc.body)); err == nil {
				t.Fatal("unsafe or ambiguous treatment accepted")
			}
		})
	}
}

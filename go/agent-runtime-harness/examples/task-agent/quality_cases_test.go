package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestQualitySuiteManifestIsDistinctDeterministicAndModelIndependent(t *testing.T) {
	cases := qualityCases()
	if len(cases) != 40 || len(qualityGroups) != 5 {
		t.Fatalf("invalid quality corpus size: %d", len(cases))
	}
	ids, hashes, facts := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, item := range cases {
		if ids[item.id] || !strings.Contains(item.source.document(), item.source.record()) ||
			!strings.HasPrefix(item.source.document(), "audit log:") ||
			strings.Index(item.source.document(), item.source.record()) <= 2000 {
			t.Fatalf("invalid quality case %s", item.id)
		}
		ids[item.id] = true
		contentHash := digest([]byte(item.source.document()))
		if hashes[contentHash] || facts[item.source.record()] {
			t.Fatalf("quality case duplicate evidence: %s", item.id)
		}
		hashes[contentHash], facts[item.source.record()] = true, true
	}
	for _, group := range qualityGroups {
		for i := 1; i <= 8; i++ {
			// IDs and case categories are an immutable report contract.
			id := group + "-0" + string(rune('0'+i))
			if !ids[id] {
				t.Fatalf("missing group member: %s", id)
			}
		}
	}
	first, err := baselineStart(publicConfiguration{Provider: "openai", Suite: qualitySuite, Model: "first", Repetitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := baselineStart(publicConfiguration{Provider: "openai", Suite: qualitySuite, Model: "second", Repetitions: 3})
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := baselineStart(publicConfiguration{Provider: "openai", Repetitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	const pinnedLegacyOpenAIDatasetHash = "sha256:e4a782b32d69f7d7e7d25df60ef3416b4c950315e30d5cf716cd05fe2e0c4203"
	if first.DatasetHash != second.DatasetHash || first.DatasetHash == legacy.DatasetHash ||
		legacy.DatasetHash != pinnedLegacyOpenAIDatasetHash ||
		first.TaskVersion != qualityTaskVersion || legacy.TaskVersion != baselineTaskVersion ||
		first.RequestedSamples != 40 || second.RequestedSamples != 120 || legacy.RequestedSamples != 2 {
		t.Fatalf("wrong or unstable dataset identity: quality=%+v second=%+v legacy=%+v", first, second, legacy)
	}
}

func TestQualityScriptedCommandEmitsContentFreeCalibrationAndRespectsCancellation(t *testing.T) {
	var output bytes.Buffer
	if code := runCommand(t.Context(), []string{"-mode=quality-scripted"}, nil, &output); code != 0 {
		t.Fatalf("offline calibration exited %d", code)
	}
	var result struct {
		Suite       string           `json:"suite"`
		Status      string           `json:"status"`
		Mode        string           `json:"mode"`
		DatasetHash string           `json:"datasetHash"`
		Samples     []baselineSample `json:"samples"`
		Summary     baselineSummary  `json:"summary"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	const pinnedScriptedDatasetHash = "sha256:941168211fd24478c6ef64fafd2a581116c3c42def54a227885ab5479e3032f2"
	if result.Suite != qualitySuite || result.Mode != "scripted_oracle_calibration" ||
		result.Status != "passed" || result.Summary.Requested != 40 || result.Summary.Passed != 40 ||
		result.Summary.ModelCalls != 160 || result.Summary.ToolCalls != 120 || len(result.Samples) != 40 ||
		result.DatasetHash != pinnedScriptedDatasetHash || bytes.Contains(output.Bytes(), []byte("DEPLOYMENT_CODE=")) ||
		bytes.Contains(output.Bytes(), []byte("audit log:")) || bytes.Contains(output.Bytes(), []byte("private-fixture-key")) {
		t.Fatalf("invalid or leaking offline calibration: suite=%s status=%s requested=%d passed=%d", result.Suite, result.Status, result.Summary.Requested, result.Summary.Passed)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	output.Reset()
	if code := runCommand(ctx, []string{"-mode=quality-scripted"}, nil, &output); code == 0 {
		t.Fatal("cancelled calibration reported success")
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || result.Summary.Skipped != 40 || result.Summary.Attempted != 0 || result.Status != "failed" {
		t.Fatalf("cancelled calibration invented task outcomes: %s err=%v", output.String(), err)
	}
	output.Reset()
	if code := runCommand(t.Context(), []string{"-mode=quality-scripted", "-provider=openai"}, nil, &output); code == 0 || bytes.Contains(output.Bytes(), []byte("evidence-40-v1")) {
		t.Fatal("offline command accepted live provider flags")
	}
}

func TestQualitySuiteWireAdmissionUsesExactOptionalSuiteAndBoundedRepetitions(t *testing.T) {
	input := unitBaselineInput()
	input.Suite = qualitySuite
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	_, public, err := readBaselineInput(bytes.NewReader(body))
	if err != nil || public.Suite != qualitySuite {
		t.Fatalf("quality suite wire input rejected: %+v err=%v", public, err)
	}
	for _, bad := range [][]byte{
		bytes.Replace(body, []byte(`"suite":"evidence-40-v1"`), []byte(`"suite":""`), 1),
		bytes.Replace(body, []byte(`"suite":"evidence-40-v1"`), []byte(`"suite":null`), 1),
		bytes.Replace(body, []byte(`"suite":"evidence-40-v1"`), []byte(`"Suite":"evidence-40-v1"`), 1),
		bytes.Replace(body, []byte(`"suite":"evidence-40-v1"`), []byte(`"suite":"unknown"`), 1),
	} {
		if _, _, err := readBaselineInput(bytes.NewReader(bad)); err == nil {
			t.Fatalf("invalid suite wire admitted: %s", bad)
		}
	}
	input.Repetitions = 4
	body, err = json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := readBaselineInput(bytes.NewReader(body)); err == nil {
		t.Fatal("quality suite exceeded bounded repetition budget")
	}
	input.Suite = ""
	input.Repetitions = 4
	body, err = json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, public, err = readBaselineInput(bytes.NewReader(body)); err != nil || public.Suite != "" {
		t.Fatalf("historical two-case wire regressed: %+v err=%v", public, err)
	}
}

func TestQualitySuiteAllFortyCasesPassOfflineExecutionAndRecoveryOracle(t *testing.T) {
	config := settings{mode: "scripted"}
	if err := config.resolve(); err != nil {
		t.Fatal(err)
	}
	for _, item := range qualityCases() {
		t.Run(item.id, func(t *testing.T) {
			config.sampleID = "quality-" + item.id
			report, err := runTask(t.Context(), config, func() (model.Client, error) {
				return scriptedModel{}, nil
			}, item.source)
			if err != nil || report.Status != "passed" || !report.OraclePassed ||
				report.LookupCalls != 1 || report.ArtifactReads < 1 || report.DraftWrites != 1 ||
				report.ToolCalls != 3 || report.ModelCalls != 4 || report.ContextGeneration < 2 ||
				!report.RecoveryChecks.InterruptionObserved || !report.RecoveryChecks.LeaseBlocked ||
				!report.RecoveryChecks.CommittedToolsNotReplayed || !report.RecoveryChecks.NoExtraToolsAfterResume ||
				!report.RecoveryChecks.TerminalResumeNoOp {
				t.Fatalf("quality fixture failed actual Agent execution/oracle: status=%s stage=%s err=%v model=%d tool=%d checks=%+v",
					report.Status, report.Stage, err, report.ModelCalls, report.ToolCalls, report.RecoveryChecks)
			}
		})
	}
}

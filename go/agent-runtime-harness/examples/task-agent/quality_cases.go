package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// qualitySuite is an opt-in 40-case task-family benchmark. It varies evidence
// values, schedule formats, surrounding distractions, multilingual content and
// document layout while keeping the original actual Agent/Tool/Context/Run
// recovery oracle. These are 40 instances of ONE task family, not 40 distinct
// product capabilities or proof of general coding/reasoning performance.
const (
	qualitySuite       = "evidence-40-v1"
	qualityTaskVersion = "release-evidence-quality-v1"
)

var qualityGroups = []string{"code", "window", "distractor", "unicode", "layout"}

func qualityCases() []baselineCase {
	cases := make([]baselineCase, 0, 40)
	windows := []string{
		"Monday 01:05-01:35 UTC", "Tuesday 17:00-17:45 UTC",
		"Wednesday 03:10-03:50 UTC", "Thursday 23:15-23:50 UTC",
		"Friday 05:20-05:55 UTC", "Saturday 14:30-15:15 UTC",
		"Sunday 07:40-08:20 UTC", "Monday 18:00-18:30 UTC",
	}
	for _, group := range qualityGroups {
		for n := 1; n <= 8; n++ {
			source := sourceFixture{code: fmt.Sprintf("%s-%03d", group, n*17+41), window: windows[n-1]}
			switch group {
			case "code":
				source.code = fmt.Sprintf("BUILD-%dX-%02d", n*23+19, n)
			case "window":
				source.window = []string{
					"2026-12-01 09:05–09:35 UTC", "2026-12-02T18:15Z–18:55Z",
					"2026-12-03 13:30–14:00 +05:30", "2026-12-04 21:10–21:40 UTC",
					"2026-12-05 00:00–00:30 UTC", "2026-12-06 11:45–12:15 UTC",
					"2026-12-07 16:20–16:50 UTC", "2026-12-08 07:00–07:30 UTC",
				}[n-1]
			case "distractor":
				source.prelude = fmt.Sprintf("RELEASE R-16 DEPLOYMENT_CODE=DECOY-%d; WINDOW=NOT-THE-TARGET;\nRELEASE R-18 DEPLOYMENT_CODE=DECOY-%d; WINDOW=IGNORE;\n", n, n+10)
				source.epilogue = fmt.Sprintf("RELEASE R-19 DEPLOYMENT_CODE=STALE-%d; WINDOW=IRRELEVANT;\n", n)
			case "unicode":
				source.code = []string{"核验-甲", "ΔΟΚΙΜΗ-02", "تحقق-03", "確認-04", "검증-05", "ПРОВЕРКА-06", "प्रमाण-07", "VALIDACIÓN-08"}[n-1]
				source.prelude = "多语种日志：αβγ；検証済み； التحقق；\n"
			case "layout":
				source.prelude = fmt.Sprintf("ARCHIVE INDEX ID=%02d STATUS=ready;\n", n)
				source.epilogue = fmt.Sprintf("TRAILER ID=%02d CODE=UNRELATED;\n", n)
			}
			cases = append(cases, baselineCase{id: fmt.Sprintf("%s-%02d", group, n), source: source})
		}
	}
	return cases
}

func selectedBaselineCases(suite string) ([]baselineCase, string) {
	if suite == qualitySuite {
		return qualityCases(), qualityTaskVersion
	}
	return baselineCases(), baselineTaskVersion
}

// runScriptedQuality is an offline oracle/infrastructure calibration, not a
// substitute for a paid real-model quality measurement. It runs the same 40
// Agent/Tool/Context/Recovery tasks with a scripted model and emits no prompts,
// source records or secrets: only content-free metrics and dataset identity.
func runScriptedQuality(ctx context.Context, output io.Writer) int {
	config := settings{mode: "scripted", suite: qualitySuite}
	if err := config.resolve(); err != nil {
		return configurationError(output)
	}
	start, err := baselineStart(publicConfiguration{Provider: "scripted", Suite: qualitySuite, Repetitions: 1})
	if err != nil {
		return configurationError(output)
	}
	var samples []baselineSample
	for _, item := range qualityCases() {
		if ctx.Err() != nil {
			break
		}
		config.sampleID = item.id + "-01"
		report, _ := runTask(ctx, config, func() (model.Client, error) { return scriptedModel{}, nil }, item.source)
		samples = append(samples, sampleMetrics(report, item.id, config.sampleID, 1))
	}
	summary := summarize(start.RequestedSamples, samples, ctx.Err() == nil)
	status := "passed"
	if !summary.Complete || summary.Failed != 0 {
		status = "failed"
	}
	if err := json.NewEncoder(output).Encode(struct {
		Suite       string           `json:"suite"`
		TaskVersion string           `json:"taskVersion"`
		DatasetHash string           `json:"datasetHash"`
		Mode        string           `json:"mode"`
		Status      string           `json:"status"`
		Samples     []baselineSample `json:"samples"`
		Summary     baselineSummary  `json:"summary"`
	}{Suite: qualitySuite, TaskVersion: start.TaskVersion, DatasetHash: start.DatasetHash,
		Mode: "scripted_oracle_calibration", Status: status, Samples: samples, Summary: summary}); err != nil {
		return 1
	}
	if status != "passed" {
		return 1
	}
	return 0
}

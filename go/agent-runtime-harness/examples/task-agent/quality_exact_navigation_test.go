package main

import (
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// The user supplies the exact record ID, not its deployment facts. A native
// exact query should deliver original evidence in one read rather than making
// a candidate list or sequential offset scanning a precondition. This is a
// source-blind model/real-Agent mechanics test, NOT a real-model success claim.
func TestQualityExactIdentifierRetrievalPreservesDraftToolBudget(t *testing.T) {
	if taskInstructionsForSuite("") != taskInstructions {
		t.Fatal("quality-only navigation rewrote historical two-case instructions")
	}
	instructions := taskInstructionsForSuite(qualitySuite)
	for _, guidance := range []string{
		"literal RELEASE R-17", "offset=0", "limit=128",
		"save_draft next", "reserve at least one call for save_draft",
		"Normalized_hints are NOT evidence", "never infer target facts",
	} {
		if !strings.Contains(instructions, guidance) {
			t.Fatalf("quality guidance missing required action/budget boundary: %q", guidance)
		}
	}
	if maxToolCalls != 10 || maxModelCalls != 12 {
		t.Fatalf("instruction update cannot secretly raise limits: model=%d tool=%d", maxModelCalls, maxToolCalls)
	}
	for _, item := range qualityCases() {
		t.Run(item.id, func(t *testing.T) {
			if strings.Contains(instructions, item.source.code) || strings.Contains(instructions, item.source.window) {
				t.Fatal("quality instructions contain hidden deployment facts")
			}
			config := settings{mode: "scripted", suite: qualitySuite, sampleID: item.id + "-exact-identifier"}
			if err := config.resolve(); err != nil {
				t.Fatal(err)
			}
			report, err := runTask(t.Context(), config, func() (model.Client, error) {
				return scriptedModel{}, nil
			}, item.source)
			if err != nil || report.Status != "passed" || !report.OraclePassed ||
				report.LookupCalls != 1 || report.ArtifactReads != 1 ||
				report.DraftWrites != 1 || report.ToolCalls != 3 || report.ModelCalls != 4 ||
				report.ToolCalls >= maxToolCalls ||
				report.ArtifactReadStrategy == nil ||
				report.ArtifactReadStrategy.Queried != 1 ||
				report.ArtifactReadStrategy.OffsetOnly != 0 ||
				report.ArtifactReadStrategy.CandidatePages != 0 ||
				!report.RecoveryChecks.InterruptionObserved ||
				!report.RecoveryChecks.LeaseBlocked ||
				!report.RecoveryChecks.SameRun ||
				!report.RecoveryChecks.SameTurn ||
				!report.RecoveryChecks.CommittedToolsNotReplayed {
				t.Fatalf("exact source query did not leave budget for one verified draft: status=%s stage=%s failure=%s model=%d tool=%d reads=%d draft=%d oracle=%v err=%v",
					report.Status, report.Stage, report.FailureCode,
					report.ModelCalls, report.ToolCalls, report.ArtifactReads,
					report.DraftWrites, report.OraclePassed, err)
			}
			if !strings.Contains(taskInstructionsForSuite(qualitySuite), "source evidence") {
				t.Fatal("quality navigation must use original evidence, never candidate-only proof")
			}
		})
	}
}

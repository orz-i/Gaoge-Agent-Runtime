package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// A deterministic, source-blind fake Model: only the actual Tool results may
// supply facts. This tests the real Agent/Store/Tool/Context/recovery path for
// broad or imperfect queries, without teaching the runtime a benchmark answer.
type candidateNavigationModel struct {
	query string
}

func (candidateNavigationModel) Capabilities(context.Context, string) (model.Capabilities, error) {
	return model.Capabilities{StructuredJSONSchema: true}, nil
}

func (fake candidateNavigationModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	if saved, ok := toolMessage(request.Messages, draftKey); ok {
		var answer taskAnswer
		if err := json.Unmarshal([]byte(saved), &answer); err != nil {
			return model.Response{}, err
		}
		encoded, err := json.Marshal(answer)
		return model.Response{Content: string(encoded)}, err
	}
	if archived, ok := toolMessage(request.Messages, harness.ContextArtifactToolKey); ok {
		var page struct {
			ArtifactID string `json:"artifactID"`
			Content    string `json:"content"`
			SearchMode string `json:"searchMode"`
			Matches    []struct {
				Offset  int    `json:"offset"`
				Preview string `json:"preview"`
			} `json:"matches"`
		}
		if err := json.Unmarshal([]byte(archived), &page); err != nil {
			return model.Response{}, err
		}
		if len(page.Matches) > 0 {
			if page.Content != "" {
				return model.Response{}, errors.New("candidate search leaked full evidence")
			}
			for _, candidate := range page.Matches {
				if strings.Contains(candidate.Preview, "RELEASE R-17") {
					return toolCall("read-original-candidate", harness.ContextArtifactToolKey,
						map[string]any{"artifactID": page.ArtifactID, "offset": candidate.Offset, "limit": 128,
							"query": "RELEASE R-17"})
				}
			}
			return model.Response{}, fmt.Errorf("bounded candidates do not include requested release: mode=%s candidates=%+v", page.SearchMode, page.Matches)
		}
		fact := recordPattern.FindStringSubmatch(page.Content)
		if len(fact) != 3 {
			return model.Response{}, fmt.Errorf("exact re-read did not expose target record")
		}
		return toolCall("save-once", draftKey, map[string]string{
			"release": "R-17", "deploymentCode": fact[1], "window": fact[2], "artifactID": page.ArtifactID,
		})
	}
	if archive, ok := toolMessage(request.Messages, lookupKey); ok {
		marker := artifactPattern.FindStringSubmatch(archive)
		if len(marker) != 2 {
			return model.Response{}, errors.New("dossier was not archived")
		}
		return toolCall("inspect-candidates", harness.ContextArtifactToolKey, map[string]any{
			"artifactID": marker[1], "query": fake.query, "limit": 128,
		})
	}
	return toolCall("lookup-target", lookupKey, map[string]string{"release": "R-17"})
}

func TestQualityCandidatesRecoverAllDistractorsViaRealAgentAndRecovery(t *testing.T) {
	for _, query := range []string{"RELEASE", "deploymentCode", "deployment"} {
		for _, item := range qualityCases() {
			if !strings.HasPrefix(item.id, "distractor-") {
				continue
			}
			t.Run(item.id+"/"+query, func(t *testing.T) {
				config := settings{mode: "scripted", suite: qualitySuite}
				if err := config.resolve(); err != nil {
					t.Fatal(err)
				}
				config.sampleID = item.id + "-" + query
				report, err := runTask(t.Context(), config, func() (model.Client, error) {
					return candidateNavigationModel{query: query}, nil
				}, item.source)
				if err != nil || report.Status != "passed" || !report.OraclePassed ||
					report.LookupCalls != 1 || report.ArtifactReads != 2 || report.DraftWrites != 1 ||
					report.ModelCalls != 5 || report.ToolCalls != 4 ||
					!report.RecoveryChecks.InterruptionObserved || !report.RecoveryChecks.SameRun ||
					!report.RecoveryChecks.SameTurn || !report.RecoveryChecks.LeaseBlocked ||
					!report.RecoveryChecks.CommittedToolsNotReplayed ||
					!report.RecoveryChecks.NoExtraToolsAfterResume ||
					!report.RecoveryChecks.TerminalResumeNoOp {
					t.Fatalf("candidate navigation failed real Agent/Oracle/recovery: status=%s stage=%s code=%s models=%d tools=%d reads=%d drafts=%d oracle=%t err=%v",
						report.Status, report.Stage, report.FailureCode, report.ModelCalls,
						report.ToolCalls, report.ArtifactReads, report.DraftWrites, report.OraclePassed, err)
				}
			})
		}
	}
}

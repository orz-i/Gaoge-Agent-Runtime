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

// This deterministic model deliberately starts with an inefficient offset-only
// read. It sees only real model-visible Tool responses and uses a generic
// searchHint to switch to candidate navigation, without seeing fixture source
// bytes or being given the target answer.
type offsetFirstNavigationModel struct{}

func (offsetFirstNavigationModel) Capabilities(context.Context, string) (model.Capabilities, error) {
	return model.Capabilities{StructuredJSONSchema: true}, nil
}

func (offsetFirstNavigationModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	if saved, ok := toolMessage(request.Messages, draftKey); ok {
		var answer taskAnswer
		if err := json.Unmarshal([]byte(saved), &answer); err != nil {
			return model.Response{}, err
		}
		b, err := json.Marshal(answer)
		return model.Response{Content: string(b)}, err
	}
	if archived, ok := toolMessage(request.Messages, harness.ContextArtifactToolKey); ok {
		var page struct {
			ArtifactID string `json:"artifactID"`
			Content    string `json:"content"`
			SearchHint string `json:"searchHint"`
			Matches    []struct {
				Offset  int    `json:"offset"`
				Preview string `json:"preview"`
			} `json:"matches"`
		}
		if err := json.Unmarshal([]byte(archived), &page); err != nil {
			return model.Response{}, err
		}
		if strings.Contains(page.SearchHint, "query") {
			return toolCall("search-after-offset-hint", harness.ContextArtifactToolKey, map[string]any{
				"artifactID": page.ArtifactID, "query": "RELEASE", "limit": 128})
		}
		if len(page.Matches) > 0 {
			for _, candidate := range page.Matches {
				if strings.Contains(candidate.Preview, "RELEASE R-17") {
					return toolCall("read-selected-source", harness.ContextArtifactToolKey, map[string]any{
						"artifactID": page.ArtifactID, "query": "RELEASE R-17", "offset": candidate.Offset, "limit": 128})
				}
			}
			return model.Response{}, fmt.Errorf("target absent from bounded candidate previews")
		}
		fact := recordPattern.FindStringSubmatch(page.Content)
		if len(fact) != 3 {
			return model.Response{}, fmt.Errorf("no exact original source evidence after navigation")
		}
		return toolCall("write-verified-draft", draftKey, map[string]string{
			"release": "R-17", "deploymentCode": fact[1], "window": fact[2], "artifactID": page.ArtifactID})
	}
	if archive, ok := toolMessage(request.Messages, lookupKey); ok {
		marker := artifactPattern.FindStringSubmatch(archive)
		if len(marker) != 2 {
			return model.Response{}, fmt.Errorf("expected actual archived dossier reference")
		}
		return toolCall("first-offset-only", harness.ContextArtifactToolKey, map[string]any{
			"artifactID": marker[1], "offset": 0, "limit": 8000})
	}
	return toolCall("lookup-release", lookupKey, map[string]string{"release": "R-17"})
}

func TestQualityLongArtifactOffsetHintCorrectsRealAgentToSourceEvidence(t *testing.T) {
	config := settings{mode: "scripted", suite: qualitySuite}
	if err := config.resolve(); err != nil {
		t.Fatal(err)
	}
	config.sampleID = "distractor-05-offset-hint"
	var source sourceFixture
	for _, item := range qualityCases() {
		if item.id == "distractor-05" {
			source = item.source
			break
		}
	}
	if source.code == "" {
		t.Fatal("fixture missing")
	}
	report, err := runTask(t.Context(), config, func() (model.Client, error) {
		return offsetFirstNavigationModel{}, nil
	}, source)
	modes := report.ArtifactReadStrategy
	if err != nil || report.Status != "passed" || !report.OraclePassed ||
		report.LookupCalls != 1 || report.DraftWrites != 1 || report.ArtifactReads != 3 ||
		modes == nil || modes.Queried != 2 || modes.OffsetOnly != 1 ||
		modes.CandidatePages != 1 || modes.HintedOffsetPages != 1 ||
		modes.NormalizedHintPages != 0 || !report.RecoveryChecks.CommittedToolsNotReplayed ||
		!report.RecoveryChecks.SameRun || !report.RecoveryChecks.LeaseBlocked {
		t.Fatalf("Agent did not switch from offset paging to bounded search and committed evidence: status=%s stage=%s code=%s reads=%d tools=%d modes=%+v err=%v",
			report.Status, report.Stage, report.FailureCode, report.ArtifactReads, report.ToolCalls, modes, err)
	}
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
					report.ArtifactReadStrategy == nil ||
					report.ArtifactReadStrategy.Queried != 2 ||
					report.ArtifactReadStrategy.OffsetOnly != 0 ||
					report.ArtifactReadStrategy.CandidatePages != 1 ||
					report.ArtifactReadStrategy.HintedOffsetPages != 0 ||
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

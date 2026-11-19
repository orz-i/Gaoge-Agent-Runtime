package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestAcceptanceCommandRunsDistinctArtifactOracles(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	var output bytes.Buffer
	if code := runCommand(ctx, []string{"-mode=acceptance"}, nil, &output); code != 0 {
		t.Fatalf("acceptance command failed: code=%d output=%s", code, output.String())
	}
	var report acceptanceReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Version != acceptanceVersion || !report.FixtureOnly || report.Mode != "fixture" || report.Requested != 3 || report.Attempted != 3 {
		t.Fatalf("fixture tasks were mislabeled as model-quality evidence: %+v", report)
	}
	for _, result := range report.Results {
		if !result.AgentCompleted || !result.OutcomePassed || len(result.TaskHash) != 64 || result.ArtifactSHA256 == "" || len(result.Checks) < 2 {
			t.Fatalf("task lacks an independently checked artifact: %+v", result)
		}
	}
}

func TestAcceptanceCodeOracleRejectsPublicTestOverfit(t *testing.T) {
	task := &acceptanceTask{name: "code", directory: t.TempDir()}
	if _, err := task.prepareCode(); err != nil {
		t.Fatal(err)
	}
	// This passes all three visible cases but is not a correct Clamp function.
	overfit := "package sample\nfunc Clamp(value, low, high int) int { if value == 5 { return 5 }; if value == 20 { return 10 }; return 0 }"
	content, err := normalizeAcceptanceCode(overfit)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(task.artifactPath("clamp.go"), content, 0600); err != nil {
		t.Fatal(err)
	}
	passed, err := task.testCode(t.Context(), false)
	if err != nil || !passed {
		t.Fatalf("negative fixture must pass public tests first: passed=%t err=%v output=%s", passed, err, task.lastTestOutput)
	}
	task.publicTestsRan = true
	checks := make(map[string]bool)
	if _, err = task.verifyCode(t.Context(), checks); err != nil {
		t.Fatal(err)
	}
	if checks["independent_held_out_tests_pass"] {
		t.Fatal("a hardcoded public-test answer passed independent acceptance")
	}
}

func TestAcceptanceCodeGrammarDoesNotExecuteArbitraryModelCode(t *testing.T) {
	for _, source := range []string{
		"package sample; import \"os\"; func Clamp(value, low, high int) int { os.Exit(0); return 0 }",
		"package sample; func Clamp(value, low, high int) int { for {}; return 0 }",
		"package sample; func Clamp(value, low, high int) int { return Clamp(value,low,high) }",
		"package sample; var global = 1; func Clamp(value, low, high int) int { return global }",
		"package sample; func Clamp(value, low, high int) int { go panic(1); return value }",
		"package sample; func Clamp(value, low, high int) int { return *(&value) }",
	} {
		if _, err := normalizeAcceptanceCode(source); err == nil {
			t.Fatal("effectful or unbounded model code was accepted")
		}
	}
	content, err := normalizeAcceptanceCode("//go:nosplit\n" + acceptanceFixedCode)
	if err != nil || strings.Contains(string(content), "//go:") {
		t.Fatalf("compiler directives were preserved: %v", err)
	}
}

type wrongDocumentFixture struct{}

func (wrongDocumentFixture) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	response, err := (acceptanceFixtureModel{task: "documents"}).Generate(ctx, request)
	for index := range response.ToolCalls {
		if response.ToolCalls[index].ToolKey == "save_answer" {
			response.ToolCalls[index].Arguments = json.RawMessage(`{"quantity":30,"delivery":"2099-08-01","source":"D-01"}`)
		}
	}
	return response, err
}

func TestCompletedAgentWithWrongArtifactFailsTaskOutcome(t *testing.T) {
	result := runAcceptanceTask(t.Context(), "documents", wrongDocumentFixture{}, "fixture", 131072, 512)
	if !result.AgentCompleted || result.OutcomePassed || result.Failure != "task_outcome_failed" || result.ArtifactSHA256 == "" {
		t.Fatalf("runtime completion was confused with task correctness: %+v", result)
	}
}

func TestAcceptanceLiveIsExplicitAndDoesNotChangeLegacyModes(t *testing.T) {
	for _, args := range [][]string{
		{"-mode=acceptance", "-provider=openai", "-model=not-used", "-context-tokens=32768", "-max-output-tokens=512"},
		{"-mode=acceptance", "-allow-live-calls"},
		{"-mode=scripted", "-acceptance-case=code"},
		{"-mode=quality-scripted", "-allow-live-calls"},
		{"-mode=acceptance", "-acceptance-case=unknown"},
	} {
		var output bytes.Buffer
		if code := runCommand(t.Context(), args, nil, &output); code == 0 || strings.Contains(output.String(), "provider_live") {
			t.Fatalf("invalid/unapproved live configuration accepted: args=%v code=%d", args, code)
		}
	}
}

type acceptanceNetworkTrap struct{ requests int }

func (trap *acceptanceNetworkTrap) RoundTrip(*http.Request) (*http.Response, error) {
	trap.requests++
	return nil, errors.New("unexpected acceptance Provider request")
}

func TestAcceptanceWithoutLiveConfirmationNeverTouchesProviderTransport(t *testing.T) {
	trap := &acceptanceNetworkTrap{}
	t.Setenv("OPENAI_API_KEY", "fixture-only-not-a-real-key")
	t.Setenv("GAOGE_MODEL_PROFILES_FILE", "")
	config := settings{mode: "acceptance", provider: "openai", modelID: "fixture-model", contextTokens: 131072, outputTokens: 512,
		httpClient: &http.Client{Transport: trap}, bindingScope: "acceptance-test"}
	var output bytes.Buffer
	if runAcceptanceCommand(t.Context(), config, "documents", false, &output) == 0 || trap.requests != 0 {
		t.Fatalf("unapproved sampling reached transport: requests=%d", trap.requests)
	}
	if strings.Contains(output.String(), "fixture-only-not-a-real-key") {
		t.Fatal("configuration error disclosed a credential")
	}
}

func TestAcceptanceTaskIdentityIncludesExecutionProfile(t *testing.T) {
	first := runAcceptanceTask(t.Context(), "documents", acceptanceFixtureModel{task: "documents"}, "fixture", 131072, 512)
	second := runAcceptanceTask(t.Context(), "documents", acceptanceFixtureModel{task: "documents"}, "fixture", 131072, 1024)
	if !first.OutcomePassed || !second.OutcomePassed || first.TaskHash == second.TaskHash || len(first.TaskHash) != 64 {
		t.Fatal("different output budgets shared the same declared task/profile identity")
	}
}

func TestAcceptanceUnavailableToolchainIsNotAStartedModelTask(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var output bytes.Buffer
	if runCommand(t.Context(), []string{"-mode=acceptance", "-acceptance-case=code"}, nil, &output) == 0 {
		t.Fatal("missing Go toolchain was accepted")
	}
	var report acceptanceReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Processed != 1 || report.Attempted != 0 || len(report.Results) != 1 ||
		report.Results[0].AgentStarted || report.Results[0].Failure != "fixture_toolchain_unavailable" {
		t.Fatalf("setup failure was promoted into a real attempted task: %+v", report)
	}
}

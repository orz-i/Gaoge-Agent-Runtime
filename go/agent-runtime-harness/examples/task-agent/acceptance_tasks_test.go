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
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
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
	if report.Version != acceptanceVersion || !report.FixtureOnly || report.Mode != "fixture" || report.Requested != 7 || report.Attempted != 7 || report.Processed != 7 || report.Attempts != 1 || report.GoVersion == "" {
		t.Fatalf("fixture tasks were mislabeled as model-quality evidence: %+v", report)
	}
	if !equalAcceptanceStrings(report.Cases, []string{"code", "code-negative", "code-degenerate", "documents", "documents-distractors", "approval", "approval-denied"}) ||
		!equalAcceptanceCounts(report.TaskFamilyPlannedCounts, map[string]int{"code-repair": 3, "document-evidence": 2, "approval-control": 2}) ||
		!equalAcceptanceCounts(report.TaskFamilyProcessedCounts, report.TaskFamilyPlannedCounts) {
		t.Fatalf("planned/processed case and family counts are inconsistent: %+v", report)
	}
	for _, result := range report.Results {
		if !result.AgentCompleted || !result.OutcomePassed || len(result.TaskHash) != 64 || len(result.Checks) < 2 {
			t.Fatalf("task lacks an independently checked artifact: %+v", result)
		}
		if result.Case == "approval-denied" {
			if result.ArtifactSHA256 != "" || result.ToolCallsConsumed != 0 || !result.Checks["denial_created_no_artifact"] || !result.Checks["denial_has_rejection_receipt"] {
				t.Fatalf("denial was reported as a completed write: %+v", result)
			}
		} else if result.ArtifactSHA256 == "" {
			t.Fatalf("missing actual output artifact: %+v", result)
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

func TestAcceptancePublicTestsMustCoverFinalSavedSource(t *testing.T) {
	task := &acceptanceTask{name: "code", directory: t.TempDir()}
	registrations, err := task.prepareCode()
	if err != nil {
		t.Fatal(err)
	}
	handlers := make(map[string]tools.Handler)
	for _, registration := range registrations {
		handlers[registration.Definition.Key] = registration.Handler
	}
	write := func(id, source string) {
		t.Helper()
		args, marshalErr := json.Marshal(map[string]string{"source": source})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, writeErr := handlers["write_code"].Execute(t.Context(), tools.ExecutionRequest{RunID: "test", Call: tools.Call{ID: id, ToolKey: "write_code", Arguments: args}}); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	write("write-1", acceptanceFixedCode)
	if _, err := handlers["run_tests"].Execute(t.Context(), tools.ExecutionRequest{RunID: "test", Call: tools.Call{ID: "test-1", ToolKey: "run_tests", Arguments: json.RawMessage(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	if !task.publicTestsPassedCurrent {
		t.Fatalf("correct fixture failed public tests: %s", task.lastTestOutput)
	}
	variant, _ := acceptanceCodeCase("code-negative")
	write("write-2", variant.expected) // still correct, but not the tested source
	checks := map[string]bool{}
	if _, err := task.verifyCode(t.Context(), checks); err != nil {
		t.Fatal(err)
	}
	if !checks["independent_held_out_tests_pass"] || checks["public_tests_passed_final_source"] {
		t.Fatalf("stale public-test evidence was attached to a new source: %+v", checks)
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
	return (wrongDocumentTaskFixture{task: "documents"}).Generate(ctx, request)
}

type wrongDocumentTaskFixture struct{ task string }

func (fixture wrongDocumentTaskFixture) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	response, err := acceptanceFixtureModel(fixture).Generate(ctx, request)
	for index := range response.ToolCalls {
		if response.ToolCalls[index].ToolKey == "save_answer" {
			response.ToolCalls[index].Arguments = json.RawMessage(`{"quantity":42,"delivery":"2099-08-05","source":"D-03:P1"}`)
		}
	}
	return response, err
}

func TestAcceptanceTaskVariantsHaveDifferentFrozenIdentities(t *testing.T) {
	ctx := t.Context()
	tasks := []struct {
		name   string
		client model.Client
	}{
		{"code-negative", acceptanceFixtureModel{task: "code-negative"}},
		{"code-degenerate", acceptanceFixtureModel{task: "code-degenerate"}},
		{"documents-distractors", acceptanceFixtureModel{task: "documents-distractors"}},
	}
	seen := map[string]bool{}
	for _, item := range tasks {
		result := runAcceptanceTask(ctx, item.name, item.client, "fixture", "", "", 131072, 512)
		if !result.OutcomePassed || seen[result.TaskHash] {
			t.Fatalf("variant identity duplicated or failed: %s %+v", item.name, result)
		}
		seen[result.TaskHash] = true
	}
}

func TestCompletedAgentWithWrongArtifactFailsTaskOutcome(t *testing.T) {
	result := runAcceptanceTask(t.Context(), "documents", wrongDocumentFixture{}, "fixture", "", "", 131072, 512)
	if !result.AgentCompleted || result.OutcomePassed || result.Failure != "task_outcome_failed" || result.ArtifactSHA256 == "" {
		t.Fatalf("runtime completion was confused with task correctness: %+v", result)
	}
}

func TestAcceptanceUnsignedDocumentDistractorFailsTaskOutcome(t *testing.T) {
	result := runAcceptanceTask(t.Context(), "documents-distractors", wrongDocumentTaskFixture{task: "documents-distractors"}, "fixture", "", "", 131072, 512)
	if !result.AgentCompleted || result.OutcomePassed || result.Failure != "task_outcome_failed" || result.ArtifactSHA256 == "" ||
		!result.Checks["distractors_reviewed"] || result.Checks["final_facts_and_original_reference"] {
		t.Fatalf("plausible unsigned decoy passed the independent Oracle: %+v", result)
	}
}

func TestAcceptanceReportRejectsPlannedProcessedAndOutcomeTampering(t *testing.T) {
	var output bytes.Buffer
	if code := runCommand(t.Context(), []string{"-mode=acceptance"}, nil, &output); code != 0 {
		t.Fatalf("fixture report failed: %s", output.String())
	}
	var original acceptanceReport
	if err := json.Unmarshal(output.Bytes(), &original); err != nil {
		t.Fatal(err)
	}
	mutations := []struct {
		name string
		edit func(*acceptanceReport)
	}{
		{"planned case substitution", func(r *acceptanceReport) { r.Cases[0] = "documents" }},
		{"processed family count", func(r *acceptanceReport) { r.TaskFamilyProcessedCounts["code-repair"]++ }},
		{"family count omitted", func(r *acceptanceReport) { delete(r.TaskFamilyPlannedCounts, "approval-control") }},
		{"outcome with false oracle", func(r *acceptanceReport) { r.Results[0].Checks["independent_held_out_tests_pass"] = false }},
		{"unknown Oracle label", func(r *acceptanceReport) { r.Results[0].Checks["modelSaysCorrect"] = true }},
		{"fixture with Provider identity", func(r *acceptanceReport) { r.EndpointHash = strings.Repeat("a", 64) }},
		{"result order", func(r *acceptanceReport) { r.Results[0], r.Results[1] = r.Results[1], r.Results[0] }},
		{"successful denial claimed a write", func(r *acceptanceReport) { r.Results[6].ToolCallsConsumed = 1 }},
		{"missing failure classification", func(r *acceptanceReport) { r.Results[6].OutcomePassed = false }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			copy := original
			copy.Cases = append([]string(nil), original.Cases...)
			copy.TaskFamilyPlannedCounts = cloneIntCounts(original.TaskFamilyPlannedCounts)
			copy.TaskFamilyProcessedCounts = cloneIntCounts(original.TaskFamilyProcessedCounts)
			copy.Results = append([]acceptanceResult(nil), original.Results...)
			copy.Results[0].Checks = cloneBoolChecks(original.Results[0].Checks)
			mutation.edit(&copy)
			if err := validateAcceptanceReport(copy); err == nil {
				t.Fatal("tampered acceptance report was accepted")
			}
		})
	}
}

func TestAcceptancePartialReportPreservesUnrunFamilies(t *testing.T) {
	count := map[string]int{"code-repair": 1}
	report := acceptanceReport{Version: acceptanceVersion, Mode: "fixture", FixtureOnly: true, Requested: 3,
		Cases: []string{"code", "documents", "approval"}, Processed: 1, Provider: "fixture", Model: "acceptance-fixture",
		OutputLimit: 1024, GoVersion: "go-test", ModelLimit: acceptanceModelLimit, ToolLimit: acceptanceToolLimit,
		InputBytes: acceptanceInputBytes, Context: 131072, Attempts: 1,
		TaskFamilies:              []string{"code-repair", "document-evidence", "approval-control"},
		TaskFamilyPlannedCounts:   map[string]int{"code-repair": 1, "document-evidence": 1, "approval-control": 1},
		TaskFamilyProcessedCounts: count, Results: []acceptanceResult{{Case: "code", Family: "code-repair", Failure: "fixture_toolchain_unavailable"}}}
	if err := validateAcceptanceReport(report); err != nil {
		t.Fatalf("honest partial report was rejected: %v", err)
	}
	report.Processed, report.Attempted = 3, 3
	if err := validateAcceptanceReport(report); err == nil {
		t.Fatal("unrun cases were counted as attempted")
	}
}

func TestAcceptanceStartedFailureCanStopBeforeArtifactOracle(t *testing.T) {
	var output bytes.Buffer
	if code := runCommand(t.Context(), []string{"-mode=acceptance"}, nil, &output); code != 0 {
		t.Fatalf("fixture acceptance run failed: %s", output.String())
	}
	var report acceptanceReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	failed := report.Results[0]
	failed.AgentCompleted = false
	failed.OutcomePassed = false
	failed.Failure = "agent_did_not_complete"
	failed.Checks = map[string]bool{}
	report.Results = []acceptanceResult{failed}
	report.Processed, report.Attempted = 1, 1
	report.TaskFamilyProcessedCounts = map[string]int{"code-repair": 1}
	if err := validateAcceptanceReport(report); err != nil {
		t.Fatalf("pre-Oracle execution failure was rejected or re-scored: %v", err)
	}
	failed.Checks = map[string]bool{"untrustedCause": true}
	report.Results[0] = failed
	if err := validateAcceptanceReport(report); err == nil {
		t.Fatal("unknown failure evidence was rendered as a task outcome")
	}
}

func TestAcceptanceStopsAfterFirstFailedOutcomeAndKeepsOriginalDenominator(t *testing.T) {
	var output bytes.Buffer
	if code := runCommand(t.Context(), []string{"-mode=acceptance"}, nil, &output); code != 0 {
		t.Fatalf("fixture report failed: %s", output.String())
	}
	var report acceptanceReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	failed := report.Results[0]
	failed.OutcomePassed = false
	failed.Failure = "task_outcome_failed"
	partial := report
	partial.Results = []acceptanceResult{failed}
	partial.Processed = 1
	partial.Attempted = 1
	partial.TaskFamilyProcessedCounts = map[string]int{"code-repair": 1}
	if err := validateAcceptanceReport(partial); err != nil {
		t.Fatalf("honest stop-on-failure report was rejected: %v", err)
	}
	partial.Results = report.Results[:2]
	partial.Processed, partial.Attempted = 2, 2
	partial.TaskFamilyProcessedCounts = map[string]int{"code-repair": 2}
	if err := validateAcceptanceReport(partial); err == nil {
		t.Fatal("report accepted work after the first failed case")
	}
}

func cloneIntCounts(source map[string]int) map[string]int {
	result := make(map[string]int, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneBoolChecks(source map[string]bool) map[string]bool {
	result := make(map[string]bool, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func TestAcceptanceLiveIsExplicitAndDoesNotChangeLegacyModes(t *testing.T) {
	for _, args := range [][]string{
		{"-mode=acceptance", "-provider=openai", "-model=not-used", "-context-tokens=32768", "-max-output-tokens=512"},
		{"-mode=acceptance", "-allow-live-calls"},
		{"-mode=acceptance", "-allow-live-calls", "-provider=openai", "-model=not-used", "-context-tokens=32768", "-max-output-tokens=512"},
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

func TestAcceptanceBindingIdentitiesAreOfflineAndSnapshotOwned(t *testing.T) {
	profiles := model.StaticModelProfiles{"fixture-model": {StructuredJSONSchema: true, EffortLevels: []string{"low"}}}
	calls := 0
	config := settings{provider: "openai", modelID: "fixture-model", modelProfiles: model.ModelProfileSourceFunc(func() map[string]model.Capabilities {
		calls++
		return profiles
	})}
	frozen, hash, err := acceptanceModelProfiles(config)
	if err != nil || calls != 1 || !validAcceptanceHash(hash) {
		t.Fatalf("model profile was not snapshotted once: %v calls=%d", err, calls)
	}
	profiles["fixture-model"].EffortLevels[0] = "high"
	if frozen["fixture-model"].EffortLevels[0] != "low" {
		t.Fatal("frozen model profile aliases the Host source")
	}
	first, err := acceptanceEndpointHash(config)
	if err != nil {
		t.Fatal(err)
	}
	config.endpoint = "https://gateway.invalid/v1/responses"
	second, err := acceptanceEndpointHash(config)
	if err != nil || first == second || !validAcceptanceHash(second) {
		t.Fatal("distinct endpoints were not distinguished without HTTP")
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
	first := runAcceptanceTask(t.Context(), "documents", acceptanceFixtureModel{task: "documents"}, "fixture", "", "", 131072, 512)
	second := runAcceptanceTask(t.Context(), "documents", acceptanceFixtureModel{task: "documents"}, "fixture", "", "", 131072, 1024)
	if !first.OutcomePassed || !second.OutcomePassed || first.TaskHash == second.TaskHash || len(first.TaskHash) != 64 {
		t.Fatal("different output budgets shared the same declared task/profile identity")
	}
	repeated := runAcceptanceTask(t.Context(), "documents", acceptanceFixtureModel{task: "documents"}, "fixture", "", "", 131072, 512)
	if !repeated.OutcomePassed || repeated.TaskHash != first.TaskHash {
		t.Fatal("same task identity changed with a fresh temporary workspace")
	}
	for _, identity := range []struct{ model, profile, endpoint string }{
		{"different-model", "", ""},
		{"fixture", strings.Repeat("a", 64), ""},
		{"fixture", "", strings.Repeat("b", 64)},
	} {
		result := runAcceptanceTask(t.Context(), "documents", acceptanceFixtureModel{task: "documents"}, identity.model, identity.profile, identity.endpoint, 131072, 512)
		if !result.OutcomePassed || result.TaskHash == first.TaskHash {
			t.Fatal("model/profile/endpoint change did not rotate the declared task hash")
		}
	}
}

func TestAcceptanceUnavailableToolchainIsNotAStartedModelTask(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	result := runAcceptanceTask(t.Context(), "code", acceptanceFixtureModel{task: "code"}, "fixture", "", "", 131072, 1024)
	if result.AgentStarted || result.Failure != "fixture_toolchain_unavailable" {
		t.Fatalf("toolchain setup failure was promoted into an Agent attempt: %+v", result)
	}
	build := strings.Repeat("a", 40)
	dirty := false
	count := acceptanceFamilyCounts([]string{"code"})
	report := acceptanceReport{Version: acceptanceVersion, Mode: "fixture", FixtureOnly: true, Requested: 1,
		Cases: []string{"code"}, Processed: 1, Provider: "fixture", Model: "acceptance-fixture", OutputLimit: 1024,
		SourceRevision: build, SourceDirty: &dirty, GoVersion: "go-test", ModelLimit: acceptanceModelLimit,
		ToolLimit: acceptanceToolLimit, InputBytes: acceptanceInputBytes, Context: 131072, Attempts: 1,
		TaskFamilies: acceptanceFamilies([]string{"code"}), TaskFamilyPlannedCounts: count,
		TaskFamilyProcessedCounts: count, Results: []acceptanceResult{result}}
	if err := validateAcceptanceReport(report); err != nil {
		t.Fatalf("pre-Agent failure report was not represented honestly: %v", err)
	}
	var output bytes.Buffer
	if code := runCommand(t.Context(), []string{"-mode=acceptance", "-acceptance-case=code"}, nil, &output); code != 1 {
		t.Fatalf("setup failure did not produce the bounded acceptance report: code=%d output=%s", code, output.String())
	}
	if err := json.Unmarshal(output.Bytes(), &report); err != nil || report.Attempted != 0 || report.Processed != 1 || report.Results[0].Failure != "fixture_toolchain_unavailable" {
		t.Fatalf("command lost the pre-Agent setup failure: err=%v output=%s", err, output.String())
	}
}

type dishonestDenialFixture struct{}

func (dishonestDenialFixture) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	response, err := (acceptanceFixtureModel{task: "approval-denied"}).Generate(ctx, request)
	if len(response.ToolCalls) == 0 {
		response.Content = `{"status":"denied","written":true}`
	}
	return response, err
}

func TestAcceptanceDenialOracleRejectsContradictoryFinalAnswer(t *testing.T) {
	result := runAcceptanceTask(t.Context(), "approval-denied", dishonestDenialFixture{}, "fixture", "", "", 131072, 1024)
	if !result.AgentCompleted || result.OutcomePassed || result.Failure != "task_outcome_failed" || result.ToolCallsConsumed != 0 ||
		!result.Checks["denial_has_rejection_receipt"] || !result.Checks["denial_created_no_artifact"] || result.Checks["answer_reports_denial"] {
		t.Fatalf("a false claim of writing after rejection passed: %+v", result)
	}
}

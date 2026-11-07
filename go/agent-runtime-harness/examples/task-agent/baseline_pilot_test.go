package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func runQualityPilotFixture(t *testing.T, badFinal bool) (int, []map[string]any, *baselineHTTPFixture, string) {
	t.Helper()
	certifyBaselineModelForTest(t, "private/baseline-verified")
	fixture := &baselineHTTPFixture{
		t: t, protocol: "openai", path: "/pilot", model: "private/baseline-verified", wrongFinal: badFinal,
	}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	input := baselineHTTPInput("openai", server.URL+"/pilot", false)
	input["suite"] = qualitySuite
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	code := runCommand(t.Context(), []string{"-mode=baseline-pilot-stdin"}, bytes.NewReader(body), &output)
	return code, decodeProbeOnlyEvents(t, output.Bytes()), fixture, output.String()
}

func TestQualityPilotHasHardSingleCaseCeilingAndRealAgentOracle(t *testing.T) {
	code, events, fixture, text := runQualityPilotFixture(t, false)
	start := baselineEventsOfType(events, "start")
	preflight := baselineEventsOfType(events, "preflight")
	samples := baselineEventsOfType(events, "sample")
	summary := baselineEventsOfType(events, "summary")
	if code != 0 || len(start) != 1 || len(preflight) != 1 || len(samples) != 1 || len(summary) != 1 ||
		start[0]["pilotOnly"] != true || start[0]["pilotCaseID"] != pilotCaseID ||
		start[0]["requestedSamples"] != float64(40) || preflight[0]["status"] != "passed" ||
		samples[0]["caseID"] != pilotCaseID || samples[0]["repetition"] != float64(1) ||
		samples[0]["status"] != "passed" || summary[0]["requested"] != float64(40) ||
		summary[0]["attempted"] != float64(1) || summary[0]["passed"] != float64(1) ||
		summary[0]["skipped"] != float64(39) || summary[0]["complete"] != false ||
		strings.Contains(text, baselineHTTPFakeKey) {
		t.Fatalf("pilot had invalid execution boundary: exit=%d events=%+v", code, events)
	}
	fixture.mu.Lock()
	probes, toolProbe, taskCalls, answers := fixture.probes, fixture.probeStreams, fixture.streams, len(fixture.answers)
	fixture.mu.Unlock()
	if probes != 1 || toolProbe != 2 || taskCalls != 4 || answers != 1 {
		t.Fatalf("pilot crossed single-Task boundary: strict=%d replay=%d model=%d draftAnswer=%d", probes, toolProbe, taskCalls, answers)
	}
}

func TestQualityPilotFailureRemainsOneAttemptAndCannotContinue(t *testing.T) {
	code, events, fixture, _ := runQualityPilotFixture(t, true)
	samples := baselineEventsOfType(events, "sample")
	summary := baselineEventsOfType(events, "summary")
	if code == 0 || len(samples) != 1 || samples[0]["caseID"] != pilotCaseID ||
		samples[0]["status"] != "failed" || len(summary) != 1 ||
		summary[0]["attempted"] != float64(1) || summary[0]["failed"] != float64(1) ||
		summary[0]["skipped"] != float64(39) || summary[0]["complete"] != false {
		t.Fatalf("failed single Task escaped pilot cap: exit=%d events=%+v", code, events)
	}
	fixture.mu.Lock()
	calls := fixture.streams
	fixture.mu.Unlock()
	if calls != 4 {
		t.Fatalf("failed pilot attempted extra tasks, model calls=%d", calls)
	}
}

func TestQualityPilotRejectsOtherSuitesRepetitionsAndFlagsBeforeProvider(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
		args   []string
	}{
		{"legacy", func(_ map[string]any) {}, nil},
		{"repetitions2", func(v map[string]any) { v["suite"] = qualitySuite; v["repetitions"] = 2 }, nil},
		{"unknownSuite", func(v map[string]any) { v["suite"] = "unknown" }, nil},
		{"untrustedPilotMode", func(v map[string]any) { v["suite"] = qualitySuite; v["pilotOnly"] = true }, nil},
		{"providerFlag", func(v map[string]any) { v["suite"] = qualitySuite }, []string{"-provider=openai"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := baselineHTTPInput("openai", "https://example.invalid/v1/responses", true)
			tc.mutate(input)
			encoded, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			args := append([]string{"-mode=baseline-pilot-stdin"}, tc.args...)
			var output bytes.Buffer
			code := runCommand(t.Context(), args, bytes.NewReader(encoded), &output)
			events := decodeProbeOnlyEvents(t, output.Bytes())
			if code == 0 || len(events) != 1 || events[0]["type"] != "error" ||
				events[0]["failureCode"] != "invalid_configuration" ||
				len(baselineEventsOfType(events, "sample")) != 0 ||
				len(baselineEventsOfType(events, "preflight")) != 0 {
				t.Fatalf("invalid pilot entered Provider: exit=%d events=%+v", code, events)
			}
		})
	}
}

func TestQualityPilotPreflightFailureNeverStartsOneRealTask(t *testing.T) {
	t.Setenv(hostModelProfilesFileEnv, "")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	input := baselineHTTPInput("openai", server.URL+"/no-task", false)
	input["suite"] = qualitySuite
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	code := runCommand(t.Context(), []string{"-mode=baseline-pilot-stdin"}, bytes.NewReader(encoded), &output)
	events := decodeProbeOnlyEvents(t, output.Bytes())
	pref := baselineEventsOfType(events, "preflight")
	summaries := baselineEventsOfType(events, "summary")
	if code == 0 || len(pref) != 1 || pref[0]["status"] != "failed" ||
		pref[0]["failureCode"] != "capability_unavailable" ||
		len(summaries) != 1 || summaries[0]["attempted"] != float64(0) ||
		summaries[0]["skipped"] != float64(40) || summaries[0]["complete"] != false ||
		len(baselineEventsOfType(events, "sample")) != 0 || requests.Load() != 0 {
		t.Fatalf("failed pilot preflight launched a Task or fabricated results: code=%d requests=%d events=%+v",
			code, requests.Load(), events)
	}
}

func TestQualityPilotBudgetAllowsBoundedOpaqueReasoningReplayWithoutWeakeningModelLimit(t *testing.T) {
	certifyBaselineModelForTest(t, "private/baseline-verified")
	fixture := &baselineHTTPFixture{
		t: t, protocol: "openai", path: "/long-replay", model: "private/baseline-verified",
		replayCipherBytes: 12000,
	}
	server := httptest.NewServer(fixture)
	defer server.Close()
	input := baselineHTTPInput("openai", server.URL+"/long-replay", false)
	input["suite"] = qualitySuite
	input["contextTokens"] = 65536 // Verified Host capacity; never infer this from the model string.
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	code := runCommand(t.Context(), []string{"-mode=baseline-pilot-stdin"}, bytes.NewReader(body), &output)
	events := decodeProbeOnlyEvents(t, output.Bytes())
	start := baselineEventsOfType(events, "start")
	samples := baselineEventsOfType(events, "sample")
	summaries := baselineEventsOfType(events, "summary")
	fixture.mu.Lock()
	modelRequests, toolsProbe := fixture.streams, fixture.probeStreams
	fixture.mu.Unlock()
	if code != 0 || len(start) != 1 || len(samples) != 1 ||
		samples[0]["status"] != "passed" || len(summaries) != 1 ||
		summaries[0]["attempted"] != float64(1) || summaries[0]["skipped"] != float64(39) ||
		modelRequests != 4 || toolsProbe != 2 ||
		start[0]["profile"].(map[string]any)["contextPolicy"].(map[string]any)["maxInputTokens"] != float64(32768) {
		t.Fatalf("safe opaque replay exceeded benchmark's artificial service limit: exit=%d taskRequests=%d events=%+v", code, modelRequests, events)
	}
}
func TestQualityPilotFailsClosedWhenOpaqueReplayExceedsServiceLimit(t *testing.T) {
	certifyBaselineModelForTest(t, "private/baseline-verified")
	fixture := &baselineHTTPFixture{
		t: t, protocol: "openai", path: "/oversize-replay", model: "private/baseline-verified",
		replayCipherBytes: 40000,
	}
	server := httptest.NewServer(fixture)
	defer server.Close()
	input := baselineHTTPInput("openai", server.URL+"/oversize-replay", false)
	input["suite"] = qualitySuite
	input["contextTokens"] = 1050000
	input["maxOutputTokens"] = 512
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	code := runCommand(t.Context(), []string{"-mode=baseline-pilot-stdin"}, bytes.NewReader(body), &output)
	events := decodeProbeOnlyEvents(t, output.Bytes())
	samples := baselineEventsOfType(events, "sample")
	summaries := baselineEventsOfType(events, "summary")
	fixture.mu.Lock()
	dispatched := fixture.streams
	fixture.mu.Unlock()
	if code == 0 || len(samples) != 1 ||
		samples[0]["failureCode"] != "context_budget_exceeded" ||
		samples[0]["status"] != "failed" || samples[0]["draftWrites"] != float64(0) ||
		len(summaries) != 1 || summaries[0]["attempted"] != float64(1) ||
		summaries[0]["skipped"] != float64(39) || dispatched != 2 {
		t.Fatalf("oversized signed replay bypassed Host budget: exit=%d requests=%d failure=%v",
			code, dispatched, samples)
	}
}

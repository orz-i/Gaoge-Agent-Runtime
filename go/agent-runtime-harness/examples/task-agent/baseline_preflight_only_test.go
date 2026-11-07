package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func decodeProbeOnlyEvents(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var events []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var event map[string]any
		if err := decoder.Decode(&event); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("invalid preflight-only JSONL: %v", err)
		}
		events = append(events, event)
	}
	return events
}

func TestQualityPreflightOnlyPassesWithoutAnyTaskExecution(t *testing.T) {
	certifyBaselineModelForTest(t, "private/baseline-verified")
	fixture := &baselineHTTPFixture{
		t: t, protocol: "openai", path: "/probe-only", model: "private/baseline-verified",
	}
	server := httptest.NewServer(fixture)
	defer server.Close()
	input := baselineHTTPInput("openai", server.URL+"/probe-only", false)
	input["suite"] = qualitySuite
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	code := runCommand(t.Context(), []string{"-mode=baseline-preflight-stdin"}, bytes.NewReader(body), &output)
	events := decodeProbeOnlyEvents(t, output.Bytes())
	start, preflights := baselineEventsOfType(events, "start"), baselineEventsOfType(events, "preflight")
	summaries, samples := baselineEventsOfType(events, "summary"), baselineEventsOfType(events, "sample")
	if code != 0 || len(events) != 3 || len(start) != 1 || start[0]["preflightOnly"] != true ||
		start[0]["requestedSamples"] != float64(40) || len(preflights) != 1 ||
		preflights[0]["status"] != "passed" || len(summaries) != 1 ||
		summaries[0]["complete"] != false || summaries[0]["attempted"] != float64(0) ||
		summaries[0]["skipped"] != float64(40) || summaries[0]["successRate"] != nil ||
		len(samples) != 0 || strings.Contains(output.String(), baselineHTTPFakeKey) {
		t.Fatalf("preflight-only execution entered tasks or mislabeled scoring: exit=%d events=%v", code, events)
	}
	fixture.mu.Lock()
	probes, probeStreams, taskStreams, answers := fixture.probes, fixture.probeStreams, fixture.streams, len(fixture.answers)
	fixture.mu.Unlock()
	if probes != 1 || probeStreams != 2 || taskStreams != 0 || answers != 0 {
		t.Fatalf("probe-only crossed actual task boundary: json=%d toolSSE=%d task=%d answers=%d", probes, probeStreams, taskStreams, answers)
	}
}

func TestQualityPreflightOnlyFailedCapabilityCannotCreateTaskSamples(t *testing.T) {
	t.Setenv(hostModelProfilesFileEnv, "")
	var physicalRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		physicalRequests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	input := baselineHTTPInput("openai", server.URL+"/never-call", false)
	input["suite"] = qualitySuite
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	code := runCommand(t.Context(), []string{"-mode=baseline-preflight-stdin"}, bytes.NewReader(body), &output)
	events := decodeProbeOnlyEvents(t, output.Bytes())
	pref := baselineEventsOfType(events, "preflight")
	summary := baselineEventsOfType(events, "summary")
	if code == 0 || len(events) != 3 || len(pref) != 1 ||
		pref[0]["failureCode"] != "capability_unavailable" ||
		len(summary) != 1 || summary[0]["attempted"] != float64(0) ||
		summary[0]["skipped"] != float64(40) || len(baselineEventsOfType(events, "sample")) != 0 ||
		physicalRequests.Load() != 0 {
		t.Fatalf("failed preflight-only result authorized tasks: exit=%d requests=%d", code, physicalRequests.Load())
	}
	output.Reset()
	if code := runCommand(t.Context(), []string{"-mode=baseline-preflight-stdin", "-provider=openai"}, bytes.NewReader(body), &output); code == 0 ||
		len(decodeProbeOnlyEvents(t, output.Bytes())) != 1 ||
		physicalRequests.Load() != 0 {
		t.Fatal("probe-only command permitted side-effecting Provider flag override")
	}
}

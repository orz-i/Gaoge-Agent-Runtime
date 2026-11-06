package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// This Host explicitly certifies its selected model's strict-output probe.
// No public model IDs are embedded in the SDK. Explicit effort and its
// omission remain frozen through preflight, Tool replay and recovery.
func TestBaselineExternallySelectedModelProfileAcrossRecovery(t *testing.T) {
	for _, effort := range []string{"", "high"} {
		t.Run("effort="+effort, func(t *testing.T) {
			fixture := &baselineHTTPFixture{
				t: t, protocol: "openai", path: "/v1/responses", model: "gpt-6.1-terra",
			}
			var observed atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					http.Error(w, "bad request", http.StatusBadRequest)
					return
				}
				var payload map[string]json.RawMessage
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Error(err)
				}
				var reasoning struct {
					Effort string `json:"effort"`
				}
				if len(payload["reasoning"]) != 0 {
					if err := json.Unmarshal(payload["reasoning"], &reasoning); err != nil {
						t.Error(err)
					}
				}
				if reasoning.Effort != effort || (effort == "" && len(payload["reasoning"]) != 0) {
					t.Errorf("baseline changed or injected frozen reasoning: got=%q expected=%q raw=%s", reasoning.Effort, effort, payload["reasoning"])
				}
				observed.Add(1)
				r.Body = io.NopCloser(bytes.NewReader(body))
				fixture.ServeHTTP(w, r)
			}))
			defer server.Close()
			input := baselineHTTPInput("openai", server.URL+fixture.path, true)
			input["model"] = "gpt-6.1-terra"
			if effort != "" {
				input["reasoningEffort"] = effort
			}
			code, events, _ := executeHTTPBaseline(t, input)
			if code != 0 || observed.Load() == 0 {
				t.Fatalf("known public Terra failed admission/task recovery: code=%d observed=%d events=%+v", code, observed.Load(), events)
			}
			preflight := baselineEventsOfType(events, "preflight")
			samples := baselineEventsOfType(events, "sample")
			if len(preflight) != 1 || preflight[0]["status"] != "passed" || len(samples) != 2 {
				t.Fatalf("known public model failed native schema/Tool replay baseline: preflight=%+v samples=%+v", preflight, samples)
			}
			for _, sample := range samples {
				if sample["status"] != "passed" {
					t.Fatalf("public-model baseline recovery did not pass: %+v", sample)
				}
			}
		})
	}
}
